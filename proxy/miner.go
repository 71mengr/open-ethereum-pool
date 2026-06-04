package proxy

import (
    "bytes"
    "encoding/hex"
    "log"
    "math/big"
    "strings"
//    "sync"
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

// RandomX verification helper - matches daemon's hashimoto function
func (s *ProxyServer) verifyRandomXShare(t *BlockTemplate, seedHash, nonce, mixDigest []byte, targetDiff *big.Int) (bool, error) {
    // Protect manager initialization
    s.randomxMu.Lock()
    if s.randomxManager == nil {
        s.randomxManager = NewRandomXManager()
    }
    manager := s.randomxManager
    s.randomxMu.Unlock()

    if t == nil {
        return false, nil
    }

    height := t.Height
    if height == 0 {
        return false, nil
    }

    epochLength := s.config.Proxy.RandomX.EpochLength
    if epochLength == 0 {
        epochLength = 2048
    }
    epoch := height / epochLength

    // Use the seed hash from the daemon's work template
    daemonSeedHash := hexToBytes(t.Seed)
    if len(daemonSeedHash) == 0 {
        log.Printf("Empty daemon seed hash for height %d", height)
        return false, nil
    }

    cache, err := manager.GetCache(epoch, daemonSeedHash)
    if err != nil {
        log.Printf("Failed to get RandomX cache: %v", err)
        return false, err
    }

    if len(seedHash) != 32 {
        log.Printf("Invalid seed hash length: %d", len(seedHash))
        return false, nil
    }
    if len(nonce) == 0 || len(nonce) > 8 {
        log.Printf("Invalid nonce length: %d", len(nonce))
        return false, nil
    }
    if len(mixDigest) != 32 {
        log.Printf("Invalid mix digest length: %d", len(mixDigest))
        return false, nil
    }

    // Pad nonce to 8 bytes (little-endian as per daemon)
    submittedNonce := make([]byte, 8)
    copy(submittedNonce[8-len(nonce):], nonce)

    // Daemon uses little-endian for nonce
    littleEndianNonce := submittedNonce
    bigEndianNonce := reverseBytes(submittedNonce)

    // Try both endianness options
    nonceCandidates := []struct {
        name  string
        bytes []byte
    }{
        {"little-endian", littleEndianNonce},
        {"big-endian", bigEndianNonce},
    }

    var expectedHash []byte
    for _, candidate := range nonceCandidates {
        // CRITICAL: Use seedHash (from miner's params) NOT headerHash
        // This matches daemon's hashimoto: input = seedHash + nonce
        expectedHash, err = cache.ComputeHash(seedHash, candidate.bytes)
        if err != nil {
            log.Printf("ComputeHash error with %s nonce: %v", candidate.name, err)
            return false, err
        }

        if bytes.Equal(expectedHash, mixDigest) {
            if candidate.name != "little-endian" {
                log.Printf("RandomX share matched using %s nonce bytes", candidate.name)
            }
            hashDiff := randomXHashDifficulty(expectedHash)

            log.Printf("RandomX share difficulty: %s, required: %s", hashDiff.String(), targetDiff.String())
            return hashDiff.Cmp(targetDiff) >= 0, nil
        }
    }

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

func (s *ProxyServer) processRandomXShare(login, id, ip string, t *BlockTemplate, params []string) (bool, bool) {
    log.Printf("RandomX share params from %v: %+v", login, params)

    if len(params) < 3 {
        log.Printf("Invalid RandomX share params from %v@%v: got %d params, expected 3", login, ip, len(params))
        return false, false
    }

    if t == nil {
        log.Printf("No block template for RandomX share from %v@%v", login, ip)
        return false, false
    }

    nonceHex := params[0]
    minerSeedHashHex := params[1]  // This is seed hash from miner (used for verification only)
    mixDigestHex := params[2]
    
    nonce := hexToBytes(nonceHex)
    seedHash := hexToBytes(minerSeedHashHex)  // For verification
    mixDigest := hexToBytes(mixDigestHex)

    if nonce == nil || seedHash == nil || mixDigest == nil {
        log.Printf("Failed to decode hex values for share from %v@%v", login, ip)
        return false, false
    }

    // Use pool share difficulty
    poolShareDiff := s.config.Proxy.RandomX.ShareDifficulty
    if poolShareDiff == 0 {
        poolShareDiff = s.config.Proxy.Difficulty
    }
    if poolShareDiff == 0 {
        poolShareDiff = 1000
    }
    targetDiff := big.NewInt(poolShareDiff)

    // Verify the share using seed hash (matches daemon's hashimoto)
    valid, err := s.verifyRandomXShare(t, seedHash, nonce, mixDigest, targetDiff)
    if err != nil {
        log.Printf("RandomX verification error for %v@%v: %v", login, ip, err)
        return false, false
    }

    if !valid {
        log.Printf("Invalid RandomX share from %v@%v", login, ip)
        return false, false
    }

    hashDiff := randomXHashDifficulty(mixDigest)
    
    var networkDiff *big.Int
    if t.Difficulty != nil && t.Difficulty.Sign() > 0 {
        networkDiff = t.Difficulty
    } else if t.Target != "" {
        networkDiff = targetHexToDiff(t.Target)
    } else {
        networkDiff = big.NewInt(0)
    }

    // Check for block found
    if networkDiff.Sign() > 0 && hashDiff.Cmp(networkDiff) >= 0 {
        log.Printf("������ BLOCK FOUND! Hash Diff: %s >= Network Diff: %s", 
            hashDiff.String(), networkDiff.String())
        
        // CRITICAL: For block submission, use HEADER HASH from block template (t.Header)
        // NOT the seed hash from the miner!
        headerHashHex := t.Header
        
        // Format params for eth_submitWork: [nonce, headerHash, mixDigest]
        formattedParams := make([]string, 3)
        
        // Nonce
        if !strings.HasPrefix(nonceHex, "0x") {
            formattedParams[0] = "0x" + nonceHex
        } else {
            formattedParams[0] = nonceHex
        }
        
        // Header hash (from block template, NOT miner's seed hash)
        if !strings.HasPrefix(headerHashHex, "0x") {
            formattedParams[1] = "0x" + headerHashHex
        } else {
            formattedParams[1] = headerHashHex
        }
        
        // Mix digest
        if !strings.HasPrefix(mixDigestHex, "0x") {
            formattedParams[2] = "0x" + mixDigestHex
        } else {
            formattedParams[2] = mixDigestHex
        }
        
        log.Printf("Submitting block: nonce=%s, headerHash=%s..., mixDigest=%s...", 
            formattedParams[0], formattedParams[1][:18], formattedParams[2][:18])
        
        ok, err := s.rpc().SubmitBlock(formattedParams)
        if err != nil {
            log.Printf("Block submission error: %v", err)
            return false, false
        }
        
        if !ok {
            log.Printf("❌ Block REJECTED by daemon at height %d", t.Height)
            return false, false
        }
        
        log.Printf("✅✅✅ Block ACCEPTED at height %d! ✅✅✅", t.Height)
        
        // Fetch new template
        if s.config.Proxy.RandomX.Enabled {
            go s.fetchRandomXBlockTemplate()
        } else {
            go s.fetchBlockTemplate()
        }
        
        // Record the block
        exist, err := s.backend.WriteBlock(login, id, params, poolShareDiff, networkDiff.Int64(), t.Height, s.hashrateExpiration)
        if exist {
            return true, false
        }
        if err != nil {
            log.Printf("Failed to write block to backend: %v", err)
        }
        
        return true, false
    }
    
    // Regular share
    exist, err := s.backend.WriteShare(login, id, params, poolShareDiff, t.Height, s.hashrateExpiration)
    if exist {
        return true, false
    }
    if err != nil {
        log.Printf("Failed to write share to backend: %v", err)
    }
    
    return false, true
}

// targetHexToDiff converts a target hex string to difficulty big.Int
func targetHexToDiff(targetHex string) *big.Int {
    // Remove 0x prefix if present
    if len(targetHex) >= 2 && targetHex[:2] == "0x" {
        targetHex = targetHex[2:]
    }

    // Handle empty string
    if targetHex == "" {
        return big.NewInt(0)
    }

    // Parse hex string to big.Int
    target := new(big.Int)
    _, ok := target.SetString(targetHex, 16)
    if !ok {
        log.Printf("Failed to parse target hex: %s", targetHex)
        return big.NewInt(0)
    }

    // Difficulty = maxUint256 / target
    if target.Sign() == 0 {
        return big.NewInt(0)
    }

    return new(big.Int).Div(maxUint256, target)
}
