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

// RandomX verification helper - uses miner's provided seed hash
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

    // Use the miner's seed hash for verification
    if len(seedHash) != 32 {
        log.Printf("Invalid seed hash length: %d", len(seedHash))
        return false, nil
    }

    log.Printf("Getting RandomX cache - Height: %d, Epoch: %d, Miner Seed: %x", height, epoch, seedHash[:8])

    cache, err := manager.GetCache(epoch, seedHash)
    if err != nil {
        log.Printf("Failed to get RandomX cache: %v", err)
        return false, err
    }

    if len(nonce) == 0 || len(nonce) > 8 {
        log.Printf("Invalid nonce length: %d", len(nonce))
        return false, nil
    }
    if len(mixDigest) != 32 {
        log.Printf("Invalid mix digest length: %d", len(mixDigest))
        return false, nil
    }

    // Pad nonce to 8 bytes
    submittedNonce := make([]byte, 8)
    copy(submittedNonce[8-len(nonce):], nonce)

    // Try both endianness
    littleEndianNonce := submittedNonce
    bigEndianNonce := reverseBytes(submittedNonce)

    nonceCandidates := []struct {
        name  string
        bytes []byte
    }{
        {"little-endian", littleEndianNonce},
        {"big-endian", bigEndianNonce},
    }

    for _, candidate := range nonceCandidates {
        // Pass the same seed hash to ComputeHash
        expectedHash, err := cache.ComputeHash(seedHash, candidate.bytes)
        if err != nil {
            log.Printf("ComputeHash error with %s nonce: %v", candidate.name, err)
            return false, err
        }

        if bytes.Equal(expectedHash, mixDigest) {
            hashDiff := randomXHashDifficulty(expectedHash)
            log.Printf("✓ Share verified - %s nonce, Hash difficulty: %s, Target: %s",
                candidate.name, hashDiff.String(), targetDiff.String())
            return hashDiff.Cmp(targetDiff) >= 0, nil
        }
    }

    log.Printf("✗ Hash mismatch for seed %x", seedHash[:8])
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
    // Only RandomX is supported
    return s.processRandomXShare(login, id, ip, t, params)
}

// RandomX share processing - uses miner's seed hash
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

    // Miner's original params: [nonce, seedHash, mixDigest]
    nonceHex := params[0]
    minerSeedHashHex := params[1]
    mixDigestHex := params[2]

    // DEBUG: Log what we have
    log.Printf("DEBUG: t.Header = %s", t.Header)
    log.Printf("DEBUG: t.Seed = %s", t.Seed)
    log.Printf("DEBUG: Miner nonce = %s", nonceHex)
    log.Printf("DEBUG: Miner seedHash = %s", minerSeedHashHex[:16])
    log.Printf("DEBUG: Miner mixDigest = %s", mixDigestHex[:16])

    // Format params for daemon RPC call
    // Daemon's VerifySeal expects: [nonce, headerHash, mixDigest]
    formattedParams := make([]string, 3)
    
    // Parameter 1: Nonce (use miner's nonce)
    if !strings.HasPrefix(nonceHex, "0x") {
        formattedParams[0] = "0x" + nonceHex
    } else {
        formattedParams[0] = nonceHex
    }
    
    // Parameter 2: HEADER HASH from block template (NOT from miner!)
    // This is the hash from eth_getWork[0]
    headerHash := strings.TrimPrefix(t.Header, "0x")
    if !strings.HasPrefix(headerHash, "0x") {
        formattedParams[1] = "0x" + headerHash
    } else {
        formattedParams[1] = headerHash
    }
    
    // Parameter 3: Mix digest (use miner's mix digest)
    if !strings.HasPrefix(mixDigestHex, "0x") {
        formattedParams[2] = "0x" + mixDigestHex
    } else {
        formattedParams[2] = mixDigestHex
    }
    
    log.Printf("Submitting to daemon:")
    log.Printf("  nonce=%s", formattedParams[0])
    log.Printf("  headerHash=%s...", formattedParams[1][:16])
    log.Printf("  mixDigest=%s...", formattedParams[2][:16])

    // Calculate share difficulty for logging
    mixDigest := hexToBytes(mixDigestHex)
    hashDiff := randomXHashDifficulty(mixDigest)
    
    var networkDiff *big.Int
    if t.Difficulty != nil && t.Difficulty.Sign() > 0 {
        networkDiff = t.Difficulty
    } else {
        networkDiff = big.NewInt(0)
    }
    
    log.Printf("Share - Hash Diff: %s, Network Diff: %s", hashDiff.String(), networkDiff.String())

    // Submit to daemon for verification
    ok, err := s.rpc().SubmitBlock(formattedParams)
    if err != nil {
        log.Printf("SubmitBlock error: %v", err)
        return false, false
    }
    
    if !ok {
        log.Printf("Share REJECTED by daemon")
        return false, false
    }
    
    log.Printf("Share ACCEPTED by daemon")
    
    // Check if this was a block (difficulty >= network)
    if networkDiff.Sign() > 0 && hashDiff.Cmp(networkDiff) >= 0 {
        log.Printf("������ BLOCK FOUND and ACCEPTED! ������")
        
        // Fetch new template
        go s.fetchRandomXBlockTemplate()
        
        // Record the block in backend
        exist, err := s.backend.WriteBlock(login, id, params, 1, networkDiff.Int64(), t.Height, s.hashrateExpiration)
        if exist {
            return true, false
        }
        if err != nil {
            log.Printf("Failed to write block to backend: %v", err)
        }
        
        return true, false
    }
    
    // Regular share - record it
    s.backend.WriteShare(login, id, params, 1, t.Height, s.hashrateExpiration)
    return false, true
}
