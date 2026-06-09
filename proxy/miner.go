package proxy

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"log"
	"math/big"
	"strings"
)

var maxUint256 = new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil)

func randomXHashDifficulty(hash []byte) *big.Int {
	if len(hash) == 0 {
		return big.NewInt(0)
	}
	// RandomX difficulty treats hash as little-endian
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
	target := new(big.Int).Div(maxUint256, big.NewInt(diff))
	// XMRig expects 4-byte little-endian target (most common)
	targetBytes := target.FillBytes(make([]byte, 8))
	return hex.EncodeToString(reverseBytes(targetBytes[:4]))
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

	epochLength := s.config.Proxy.RandomX.EpochLength
	if epochLength == 0 {
		epochLength = 2048
	}
	epoch := t.Height / epochLength

	cache, err := manager.GetCache(epoch, seedHash)
	if err != nil {
		log.Printf("Failed to get RandomX cache: %v", err)
		return false, nil, nil, err
	}

	// 8-byte big-endian nonce (standard)
	nonce8 := make([]byte, 8)
	copy(nonce8[8-len(nonce):], nonce)

	expectedHash, err := cache.ComputeHash(headerHash, nonce8)
	if err != nil {
		log.Printf("ComputeHash error: %v", err)
		return false, nil, nil, err
	}

	hashDiff := randomXHashDifficulty(expectedHash)

	if bytes.Equal(expectedHash, submittedMixDigest) || isZeroBytes(submittedMixDigest) {
		if hashDiff.Cmp(targetDiff) >= 0 {
			log.Printf("✓ Share verified | Diff: %s >= Target: %s", hashDiff, targetDiff)
			return true, expectedHash, nonce8, nil
		}
		log.Printf("✗ Share below target | Hash: %s < Target: %s", hashDiff, targetDiff)
		return false, expectedHash, nonce8, nil
	}

	log.Printf("Hash mismatch - submitted: %x | computed: %x", submittedMixDigest[:8], expectedHash[:8])
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

	poolDiff := big.NewInt(s.GetPoolShareDifficulty())

	valid, verifiedHash, verifiedNonce, err := s.verifyRandomXShare(t, seedHash, headerHash, nonce, mixDigest, poolDiff)
	if err != nil || !valid {
		if len(verifiedHash) > 0 {
			hashDiff := randomXHashDifficulty(verifiedHash)
			log.Printf("REJECTED | Hash Diff: %s < Pool Diff: %s", hashDiff, poolDiff)
		}
		return false, false
	}

	// Prepare verified params
	verifiedNonceHex := "0x" + hex.EncodeToString(verifiedNonce)
	verifiedMixHex := "0x" + hex.EncodeToString(verifiedHash)
	hashDiff := randomXHashDifficulty(verifiedHash)

	log.Printf("ACCEPTED share | Hash Diff: %s | Pool: %s", hashDiff, poolDiff)

	// Check if block candidate
	isBlock := randomXHashMeetsTarget(verifiedHash, t.Target)
	if !isBlock && t.Difficulty != nil && t.Difficulty.Sign() > 0 {
		isBlock = hashDiff.Cmp(t.Difficulty) >= 0
	}

	if isBlock {
		log.Printf("Block candidate detected!")
		ok, err := s.rpc().SubmitBlock([]string{verifiedNonceHex, "0x" + hex.EncodeToString(headerHash), verifiedMixHex})
		if err != nil || !ok {
			log.Printf("Block rejected by daemon, accepting as share")
		} else {
			log.Printf("BLOCK FOUND AND ACCEPTED!")
			go s.fetchBlockTemplate()
			// Write block to backend...
			exist, _ := s.backend.WriteBlock(login, id, []string{verifiedNonceHex, "0x"+hex.EncodeToString(headerHash), verifiedMixHex},
				hashDiff.Int64(), t.Difficulty.Int64(), t.Height, s.hashrateExpiration)
			return exist, true
		}
	}

	// Regular share
	exist, err := s.backend.WriteShare(login, id, []string{verifiedNonceHex, "0x"+hex.EncodeToString(headerHash), verifiedMixHex},
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
