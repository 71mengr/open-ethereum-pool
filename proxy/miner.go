package proxy

import (
        "bytes"
        "encoding/hex"
        "log"
        "math/big"
        "strconv"
        "strings"

        "github.com/ethereum/ethash"
        "github.com/ethereum/go-ethereum/common"
)

var hasher = ethash.New()

var maxUint256 = new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil)

// RandomX verification helper
func (s *ProxyServer) verifyRandomXShare(headerHash, nonce, resultHash []byte, targetDiff *big.Int) (bool, error) {
        if s.randomxManager == nil {
                s.randomxManager = NewRandomXManager()
        }
        
        // Get current block height from the block template
        t := s.currentBlockTemplate()
        if t == nil {
                return false, nil
        }
        
        height := t.Height
        if height == 0 {
                return false, nil
        }
        
        epoch := height / 2048 // RandomX epoch length
        
        // Get seed hash for this epoch
        seedHash, err := s.getRandomXSeedHash(height)
        if err != nil {
                log.Printf("Failed to get RandomX seed hash: %v", err)
                return false, err
        }
        
        cache, err := s.randomxManager.GetCache(epoch, hexToBytes(seedHash))
        if err != nil {
                log.Printf("Failed to get RandomX cache: %v", err)
                return false, err
        }
        
        // Compute expected hash
        expectedHash, err := cache.ComputeHash(headerHash, nonce)
        if err != nil {
                return false, err
        }
        
        // Compare with miner's result
        if !bytes.Equal(expectedHash, resultHash) {
                return false, nil
        }
        
        // Check difficulty target
        hashBig := new(big.Int).SetBytes(expectedHash)
        hashDiff := new(big.Int).Div(maxUint256, hashBig)
        
        return hashDiff.Cmp(targetDiff) >= 0, nil
}

// Helper to convert hex string to bytes
func hexToBytes(hexStr string) []byte {
        if len(hexStr) >= 2 && hexStr[:2] == "0x" {
                hexStr = hexStr[2:]
        }
        bytes, _ := hex.DecodeString(hexStr)
        return bytes
}

func (s *ProxyServer) processShare(login, id, ip string, t *BlockTemplate, params []string) (bool, bool) {
        // Check if RandomX is enabled
        if s.config.Proxy.RandomX.Enabled {
                return s.processRandomXShare(login, id, ip, t, params)
        }
        return s.processEthashShare(login, id, ip, t, params)
}

// New RandomX share processing
func (s *ProxyServer) processRandomXShare(login, id, ip string, t *BlockTemplate, params []string) (bool, bool) {
        // RandomX params: [nonce, headerHash, resultHash]
        if len(params) < 3 {
                log.Printf("Invalid RandomX share params from %v@%v: %v", login, ip, params)
                return false, false
        }
        
        nonceHex := params[0]
        headerHashHex := params[1]
        resultHashHex := params[2]
        
        nonce := hexToBytes(nonceHex)
        headerHash := hexToBytes(headerHashHex)
        resultHash := hexToBytes(resultHashHex)
        
        shareDiff := s.config.Proxy.Difficulty
        targetDiff := big.NewInt(shareDiff)
        
        // Verify the share using RandomX
        valid, err := s.verifyRandomXShare(headerHash, nonce, resultHash, targetDiff)
        if err != nil {
                log.Printf("RandomX verification error for %v@%v: %v", login, ip, err)
                return false, false
        }
        
        if !valid {
                log.Printf("Invalid RandomX share from %v@%v", login, ip)
                return false, false
        }
        
        // Get the header hash as string for lookup
        hashNoNonce := headerHashHex
        h, ok := t.headers[hashNoNonce]
        if !ok {
                log.Printf("Stale share from %v@%v", login, ip)
                return false, false
        }
        
        // Check if this share meets network difficulty (block found)
        blockDiff := h.diff
        hashBig := new(big.Int).SetBytes(resultHash)
        hashDiff := new(big.Int).Div(maxUint256, hashBig)
        
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

// Original Ethash share processing (unchanged)
func (s *ProxyServer) processEthashShare(login, id, ip string, t *BlockTemplate, params []string) (bool, bool) {
        nonceHex := params[0]
        hashNoNonce := params[1]
        mixDigest := params[2]
        nonce, _ := strconv.ParseUint(strings.Replace(nonceHex, "0x", "", -1), 16, 64)
        shareDiff := s.config.Proxy.Difficulty

        h, ok := t.headers[hashNoNonce]
        if !ok {
                log.Printf("Stale share from %v@%v", login, ip)
                return false, false
        }

        share := Block{
                number:      h.height,
                hashNoNonce: common.HexToHash(hashNoNonce),
                difficulty:  big.NewInt(shareDiff),
                nonce:       nonce,
                mixDigest:   common.HexToHash(mixDigest),
        }

        block := Block{
                number:      h.height,
                hashNoNonce: common.HexToHash(hashNoNonce),
                difficulty:  h.diff,
                nonce:       nonce,
                mixDigest:   common.HexToHash(mixDigest),
        }

        if !hasher.Verify(share) {
                return false, false
        }

        if hasher.Verify(block) {
                ok, err := s.rpc().SubmitBlock(params)
                if err != nil {
                        log.Printf("Block submission failure at height %v for %v: %v", h.height, t.Header, err)
                } else if !ok {
                        log.Printf("Block rejected at height %v for %v", h.height, t.Header)
                        return false, false
                } else {
                        s.fetchBlockTemplate()
                        exist, err := s.backend.WriteBlock(login, id, params, shareDiff, h.diff.Int64(), h.height, s.hashrateExpiration)
                        if exist {
                                return true, false
                        }
                        if err != nil {
                                log.Println("Failed to insert block candidate into backend:", err)
                        } else {
                                log.Printf("Inserted block %v to backend", h.height)
                        }
                        log.Printf("Block found by miner %v@%v at height %d", login, ip, h.height)
                }
        } else {
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
