package proxy

import (
        "bytes"
        "encoding/hex"
        "log"
        "math/big"
)


var maxUint256 = new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil)

func randomXHashDifficulty(hash []byte) *big.Int {
    hashLE := reverseBytes(hash)
    hashBig := new(big.Int).SetBytes(hashLE)
    if hashBig.Sign() == 0 {
        return big.NewInt(0)
    }
    return new(big.Int).Div(maxUint256, hashBig)
}

// RandomX verification helper
func (s *ProxyServer) verifyRandomXShare(t *BlockTemplate, headerHash, nonce, mixDigest []byte, targetDiff *big.Int) (bool, error) {
    if s.randomxManager == nil {
        s.randomxManager = NewRandomXManager()
    }

    if t == nil {
        return false, nil
    }

    height := t.Height
    if height == 0 {
        return false, nil
    }

    epoch := height / 2048

    // Use the seed hash returned by the daemon with this work template.
    seedHash := hexToBytes(t.Seed)

    cache, err := s.randomxManager.GetCache(epoch, seedHash)
    if err != nil {
        log.Printf("Failed to get RandomX cache: %v", err)
        return false, err
    }

    // Miners submit the eth_submitWork nonce in network/big-endian hex, but
    // RandomX mining software commonly embeds the nonce bytes in little-endian
    // order before hashing the work blob. Try both forms so valid shares are
    // not rejected with a local hash mismatch.
    nonceCandidates := []struct {
        name  string
        bytes []byte
    }{
        {"submitted", nonce},
    }
    if len(nonce) > 1 {
        nonceCandidates = append(nonceCandidates, struct {
            name  string
            bytes []byte
        }{"little-endian", reverseBytes(nonce)})
    }

    var expectedHash []byte
    for _, candidate := range nonceCandidates {
        expectedHash, err = cache.ComputeHash(headerHash, candidate.bytes)
        if err != nil {
            return false, err
        }

        log.Printf("Computed RandomX hash (%s nonce): %x", candidate.name, expectedHash)
        if bytes.Equal(expectedHash, mixDigest) {
            if candidate.name != "submitted" {
                log.Printf("RandomX share matched using %s nonce bytes", candidate.name)
            }
            hashDiff := randomXHashDifficulty(expectedHash)

            log.Printf("RandomX share difficulty: %s, required: %s", hashDiff.String(), targetDiff.String())
            return hashDiff.Cmp(targetDiff) >= 0, nil
        }
    }

    log.Printf("Miner's mixDigest: %x", mixDigest)
    log.Printf("Hash mismatch: expected=%x, got=%x", expectedHash, mixDigest)
    return false, nil
}

func reverseBytes(input []byte) []byte {
    output := make([]byte, len(input))
    for i := range input {
        output[i] = input[len(input)-1-i]
    }
    return output
}

func hexToBytes(hexStr string) []byte {
    if len(hexStr) >= 2 && hexStr[:2] == "0x" {
        hexStr = hexStr[2:]
    }
    // Ensure even length
    if len(hexStr)%2 != 0 {
        hexStr = "0" + hexStr
    }
    bytes, err := hex.DecodeString(hexStr)
    if err != nil {
        log.Printf("Failed to decode hex string '%s': %v", hexStr, err)
        return nil
    }
    return bytes
}

func (s *ProxyServer) processShare(login, id, ip string, t *BlockTemplate, params []string) (bool, bool) {
        // Check if RandomX is enabled
        if s.config.Proxy.RandomX.Enabled {
                return s.processRandomXShare(login, id, ip, t, params)
        }
        return s.processFallbackShare(login, id, ip, t, params)
}

// New RandomX share processing
func (s *ProxyServer) processRandomXShare(login, id, ip string, t *BlockTemplate, params []string) (bool, bool) {
        log.Printf("RandomX share params from %v: %+v", login, params)

        // RandomX params: [nonce, headerHash, resultHash]
        if len(params) < 3 {
                log.Printf("Invalid RandomX share params from %v@%v: %v", login, ip, params)
                return false, false
        }

        if t == nil {
                log.Printf("No block template for RandomX share from %v@%v", login, ip)
                return false, false
        }
        
        nonceHex := params[0]
        headerHashHex := params[1]
        resultHashHex := params[2]
                log.Printf("nonce=%s, headerHash=%s, resultHash=%s", nonceHex, headerHashHex, resultHashHex)

        hashNoNonce := headerHashHex
        h, ok := t.headers[hashNoNonce]
        if !ok {
                log.Printf("Stale share from %v@%v", login, ip)
                return false, false
        }

        nonce := hexToBytes(nonceHex)
        headerHash := hexToBytes(hashNoNonce)
        resultHash := hexToBytes(resultHashHex)
        
        shareDiff := s.config.Proxy.Difficulty
        targetDiff := big.NewInt(shareDiff)
        
        // Verify the share using RandomX and the daemon-provided work.
        valid, err := s.verifyRandomXShare(t, headerHash, nonce, resultHash, targetDiff)
        if err != nil {
                log.Printf("RandomX verification error for %v@%v: %v", login, ip, err)
                return false, false
        }
        
        if !valid {
                log.Printf("Invalid RandomX share from %v@%v", login, ip)
                return false, false
        }
        
        // Check if this share meets network difficulty (block found)
        blockDiff := h.diff
        hashDiff := randomXHashDifficulty(resultHash)
        
        if hashDiff.Cmp(blockDiff) >= 0 {
                // Block found! Submit to network
                ok, err := s.rpc().SubmitBlock(params)
                if err != nil {
                        log.Printf("Block submission failure at height %v for %v: %v", h.height, t.Header, err)
                } else if !ok {
                        log.Printf("Block rejected at height %v for %v", h.height, t.Header)
                        return false, false
                } else {
                        // Block accepted
                        s.fetchBlockTemplate()
                        exist, err := s.backend.WriteBlock(login, id, params, shareDiff, blockDiff.Int64(), h.height, s.hashrateExpiration)
                        if exist {
                                return true, false
                        }
                        if err != nil {
                                log.Println("Failed to insert block candidate into backend:", err)
                        } else {
                                log.Printf("Inserted block %v to backend", h.height)
                        }
                        log.Printf("RandomX block found by miner %v@%v at height %d", login, ip, h.height)
                }
        } else {
                // Regular share - record it
                exist, err := s.backend.WriteShare(login, id, params, shareDiff, h.height, s.hashrateExpiration)
                if exist {
                        return true, false
                }
                if err != nil {
                        log.Println("Failed to insert share data into backend:", err)
                }
        }
        return false, true
}
