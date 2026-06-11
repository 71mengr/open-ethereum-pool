package proxy

import (
        "bytes"
        "encoding/hex"
        "log"
        "math/big"
        "strings"
)

var (
        maxUint256       = new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil)
        maxRandomXTarget = new(big.Int).Sub(new(big.Int).Set(maxUint256), big.NewInt(1))
)

func randomXHashDifficulty(hash []byte) *big.Int {
        if len(hash) == 0 {
                return big.NewInt(0)
        }
        hashBig := new(big.Int).SetBytes(reverseBytes(hash))
        if hashBig.Sign() == 0 {
                return big.NewInt(0)
        }
        return new(big.Int).Div(maxUint256, hashBig)
}

func randomXStratumTarget(diff int64) string {
        if diff <= 0 {
                diff = 1000
        }
        target := new(big.Int).Div(maxRandomXTarget, big.NewInt(diff))
        targetBytes := target.FillBytes(make([]byte, 32))
        return hex.EncodeToString(reverseBytes(targetBytes[:4]))
}

func nonceBytesToHex(nonce []byte) string {
        nonce8 := make([]byte, 8)
        if len(nonce) > len(nonce8) {
                nonce = nonce[len(nonce)-len(nonce8):]
        }
        copy(nonce8[len(nonce8)-len(nonce):], nonce)
        return "0x" + hex.EncodeToString(nonce8)
}

func randomXHashMeetsTarget(hash []byte, targetHex string) bool {
        target := hexToBytes(targetHex)
        if len(hash) == 0 || len(target) == 0 {
                return false
        }
        return bytes.Compare(reverseBytes(hash), reverseBytes(target)) <= 0
}

func (s *ProxyServer) verifyRandomXShare(t *BlockTemplate, seedHash, headerHash, nonce, submittedMixDigest []byte, targetDiff *big.Int) (bool, []byte, []byte, error) {
        s.randomxMu.Lock()
        if s.randomxManager == nil {
                s.randomxManager = NewRandomXManager()
        }
        manager := s.randomxManager
        s.randomxMu.Unlock()

        if t == nil || len(seedHash) != 32 || len(headerHash) != 32 {
                return false, nil, nil, nil
        }

        // REJECT ZERO MIX DIGEST - No exceptions
        if isZeroBytes(submittedMixDigest) {
                log.Printf("❌ REJECTED: Zero mix digest submitted (no proof of work)")
                return false, nil, nil, nil
        }

        epochLength := s.config.Proxy.RandomX.EpochLength
        if epochLength == 0 {
                epochLength = 2048
        }
        epoch := t.Height / epochLength

        cache, err := manager.GetCache(epoch, seedHash)
        if err != nil {
                log.Printf("Failed to get RandomX cache for epoch %d: %v", epoch, err)
                return false, nil, nil, err
        }

        // 8-byte big-endian nonce (standard)
        nonce8 := make([]byte, 8)
        if len(nonce) > len(nonce8) {
                nonce = nonce[len(nonce)-len(nonce8):]
        }
        copy(nonce8[len(nonce8)-len(nonce):], nonce)

        expectedHash, err := cache.ComputeHash(headerHash, nonce8)
        if err != nil {
                log.Printf("ComputeHash error: %v", err)
                return false, nil, nil, err
        }

        hashDiff := randomXHashDifficulty(expectedHash)

        // Compare computed hash with submitted mix digest
        if !bytes.Equal(expectedHash, submittedMixDigest) {
                log.Printf("❌ Hash mismatch - submitted: %x | computed: %x", submittedMixDigest[:8], expectedHash[:8])
                return false, expectedHash, nonce8, nil
        }

        // Check difficulty
        if hashDiff.Cmp(targetDiff) >= 0 {
                log.Printf("✓ Share verified | Diff: %s >= Target: %s", hashDiff.String(), targetDiff.String())
                return true, expectedHash, nonce8, nil
        }

        log.Printf("✗ Share below target | Hash: %s < Target: %s", hashDiff.String(), targetDiff.String())
        return false, expectedHash, nonce8, nil
}

// ==================== SHARE PROCESSING ====================

func (s *ProxyServer) processShare(login, id, ip string, t *BlockTemplate, params []string) (bool, bool) {
        return s.processRandomXShare(login, id, ip, t, params)
}

func (s *ProxyServer) processRandomXShare(login, id, ip string, t *BlockTemplate, params []string) (bool, bool) {
        if len(params) < 3 {
                log.Printf("Invalid params from %s@%s", login, ip)
                return false, false
        }

        nonceHex := params[0]
        minerHeaderHex := params[1]
        mixDigestHex := params[2]

        log.Printf("RandomX share | %s@%s | nonce=%s header=%s... mix=%s...",
                login, ip, nonceHex, shortHex(minerHeaderHex, 16), shortHex(mixDigestHex, 16))

        // CRITICAL: Use template header, not miner's submitted one
        headerHash := hexToBytes(t.Header)
        if len(headerHash) == 0 {
                headerHash = hexToBytes(minerHeaderHex)
        }

        seedHash := hexToBytes(t.Seed)
        nonce := hexToBytes(nonceHex)
        mixDigest := hexToBytes(mixDigestHex)

        // Check for zero mix digest early
        if isZeroBytes(mixDigest) {
                log.Printf("❌ REJECTED: Zero mix digest - miner must provide valid RandomX proof")
                return false, false
        }

        poolDiff := big.NewInt(s.GetPoolShareDifficulty())

        valid, verifiedHash, verifiedNonce, err := s.verifyRandomXShare(t, seedHash, headerHash, nonce, mixDigest, poolDiff)
        if err != nil {
                log.Printf("Verification error: %v", err)
                return false, false
        }
        
        if !valid {
                if len(verifiedHash) > 0 {
                        hashDiff := randomXHashDifficulty(verifiedHash)
                        log.Printf("REJECTED | Hash Diff: %s < Pool Diff: %s", hashDiff.String(), poolDiff.String())
                }
                return false, false
        }

        // Prepare verified params
        verifiedNonceHex := nonceBytesToHex(verifiedNonce)
        verifiedMixHex := "0x" + hex.EncodeToString(verifiedHash)
        hashDiff := randomXHashDifficulty(verifiedHash)

        log.Printf("✅ ACCEPTED share | Hash Diff: %s | Pool: %s", hashDiff.String(), poolDiff.String())

        // Check if block candidate
        isBlock := randomXHashMeetsTarget(verifiedHash, t.Target)
        if !isBlock && t.Difficulty != nil && t.Difficulty.Sign() > 0 {
                isBlock = hashDiff.Cmp(t.Difficulty) >= 0
        }

        if isBlock {
                log.Printf("�� Block candidate detected! Submitting to daemon...")
                formattedParams := []string{verifiedNonceHex, "0x" + hex.EncodeToString(headerHash), verifiedMixHex}
                ok, err := s.rpc().SubmitBlock(formattedParams)
                if err != nil {
                        log.Printf("Block submission error: %v", err)
                } else if !ok {
                        log.Printf("Block rejected by daemon, accepting as share")
                } else {
                        log.Printf("✅ BLOCK FOUND AND ACCEPTED! Height: %d", t.Height)
                        go s.fetchBlockTemplate()
                        exist, _ := s.backend.WriteBlock(login, id, formattedParams,
                                hashDiff.Int64(), t.Difficulty.Int64(), t.Height, s.hashrateExpiration)
                        return exist, true
                }
        }

        // Regular share
        formattedParams := []string{verifiedNonceHex, "0x" + hex.EncodeToString(headerHash), verifiedMixHex}
        exist, err := s.backend.WriteShare(login, id, formattedParams,
                hashDiff.Int64(), t.Height, s.hashrateExpiration)
        if err != nil {
                log.Printf("WriteShare error: %v", err)
                return false, false
        }
        return exist, true
}

// Helper functions
func reverseBytes(b []byte) []byte {
        out := make([]byte, len(b))
        for i := range b {
                out[i] = b[len(b)-1-i]
        }
        return out
}

func hexToBytes(s string) []byte {
        s = strings.TrimPrefix(s, "0x")
        b, _ := hex.DecodeString(s)
        return b
}

func isZeroBytes(b []byte) bool {
        for _, v := range b {
                if v != 0 {
                        return false
                }
        }
        return true
}

func shortHex(s string, n int) string {
        if len(s) > n {
                return s[:n]
        }
        return s
}
