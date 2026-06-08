package proxy

import (
    "bytes"
    "encoding/binary"
    "encoding/hex"
    "log"
    "math/big"
    "strings"
//    "sync"
)

var maxUint256 = new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil)
var maxUint32 = new(big.Int).SetUint64(^uint64(0) >> 32)

func randomXHashDifficulty(hash []byte) *big.Int {
    if len(hash) == 0 {
        return big.NewInt(0)
    }

    // RandomX/CryptoNote hashes are compared as little-endian integers for
    // share difficulty. Treating the digest as a big-endian integer makes many
    // valid high-difficulty shares look like difficulty 1-10 and causes local
    // rejection when the configured pool difficulty is raised.
    hashBig := new(big.Int).SetBytes(reverseBytes(hash))
    if hashBig.Sign() == 0 {
        return big.NewInt(0)
    }
    return new(big.Int).Div(maxUint256, hashBig)
}

func randomXStratumTarget(diff int64) string {
    if diff <= 0 {
        diff = DefaultRandomXShareDifficulty
    }

    target := new(big.Int).Div(maxUint32, big.NewInt(diff))
    if target.Sign() > 0 {
        targetBytes := target.FillBytes(make([]byte, 4))
        return hex.EncodeToString(reverseBytes(targetBytes))
    }

    // Extremely high configured difficulties do not fit XMRig's compact
    // 4-byte target. Fall back to its supported 8-byte little-endian form.
    target = new(big.Int).Div(new(big.Int).SetUint64(^uint64(0)), big.NewInt(diff))
    if target.Sign() == 0 {
        target.SetInt64(1)
    }
    targetBytes := target.FillBytes(make([]byte, 8))
    return hex.EncodeToString(reverseBytes(targetBytes))
}

func randomXHashMeetsTarget(hash []byte, targetHex string) bool {
    target := hexToBytes(targetHex)
    if len(hash) == 0 || len(target) == 0 {
        return false
    }
    return new(big.Int).SetBytes(hash).Cmp(new(big.Int).SetBytes(target)) <= 0
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

// RandomX verification helper - uses the template seed for the cache and the header hash as input
func (s *ProxyServer) verifyRandomXShare(t *BlockTemplate, seedHash, headerHash, nonce, submittedMixDigest []byte, targetDiff *big.Int) (bool, []byte, []byte, error) {
    // Protect manager initialization
    s.randomxMu.Lock()
    if s.randomxManager == nil {
        s.randomxManager = NewRandomXManager()
    }
    manager := s.randomxManager
    s.randomxMu.Unlock()

    if t == nil {
        return false, nil, nil, nil
    }

    height := t.Height
    if height == 0 {
        return false, nil, nil, nil
    }

    epochLength := s.config.Proxy.RandomX.EpochLength
    if epochLength == 0 {
        epochLength = 2048
    }
    epoch := height / epochLength

    // Use the daemon/template seed to initialize the RandomX cache.  The
    // submitted second eth_submitWork parameter is the header hash from
    // eth_getWork[0], not the cache seed; using it as the seed causes local
    // verification to reject otherwise valid shares with a hash mismatch.
    if len(seedHash) != 32 {
        log.Printf("Invalid seed hash length: %d", len(seedHash))
        return false, nil, nil, nil
    }
    if len(headerHash) != 32 {
        log.Printf("Invalid header hash length: %d", len(headerHash))
        return false, nil, nil, nil
    }

    log.Printf("Getting RandomX cache - Height: %d, Epoch: %d, Seed: %x, Header: %x", height, epoch, seedHash[:8], headerHash[:8])

    cache, err := manager.GetCache(epoch, seedHash)
    if err != nil {
        log.Printf("Failed to get RandomX cache: %v", err)
        return false, nil, nil, err
    }

    if len(nonce) == 0 || len(nonce) > 8 {
        log.Printf("Invalid nonce length: %d", len(nonce))
        return false, nil, nil, nil
    }
    if len(submittedMixDigest) != 32 {
        log.Printf("Invalid mix digest length: %d", len(submittedMixDigest))
        return false, nil, nil, nil
    }

    // The daemon stores header.Nonce as a uint64 in big-endian byte order.
    // Build the RandomX input nonce the same way so header.Nonce[:] matches
    // the bytes hashed by local share verification.
    bigEndianNonce := nonceBytesBigEndian(nonce)

    nonceCandidates := []struct {
        name  string
        bytes []byte
    }{
        {"big-endian", bigEndianNonce},
    }

    var bestHash []byte
    bestDiff := big.NewInt(0)

    for _, candidate := range nonceCandidates {
        expectedHash, err := cache.ComputeHash(headerHash, candidate.bytes)
        if err != nil {
            log.Printf("ComputeHash error with %s nonce: %v", candidate.name, err)
            return false, nil, nil, err
        }

        if !bytes.Equal(expectedHash, submittedMixDigest) {
            if isZeroBytes(submittedMixDigest) {
                log.Printf("Submitted zero mix digest; using computed RandomX hash for %s nonce", candidate.name)
            } else {
                log.Printf("Submitted mix digest mismatch for %s nonce; using computed RandomX hash", candidate.name)
            }
        }

        hashDiff := randomXHashDifficulty(expectedHash)
        if bestHash == nil || hashDiff.Cmp(bestDiff) > 0 {
            bestHash = expectedHash
            bestDiff = hashDiff
        }
        if hashDiff.Cmp(targetDiff) >= 0 {
            log.Printf("✓ Share verified - %s nonce, Hash difficulty: %s, Target: %s",
                candidate.name, hashDiff.String(), targetDiff.String())
            return true, expectedHash, candidate.bytes, nil
        }

        log.Printf("RandomX hash below target with %s nonce - Hash difficulty: %s, Target: %s",
            candidate.name, hashDiff.String(), targetDiff.String())
    }
    log.Printf("✗ RandomX share below target for seed %x and header %x", seedHash[:8], headerHash[:8])
    return false, bestHash, nil, nil
}

func reverseBytes(input []byte) []byte {
    output := make([]byte, len(input))
    for i := range input {
        output[i] = input[len(input)-1-i]
    }
    return output
}

func nonceBytesBigEndian(nonce []byte) []byte {
    paddedNonce := make([]byte, 8)
    if len(nonce) >= 8 {
        copy(paddedNonce, nonce[len(nonce)-8:])
    } else {
        copy(paddedNonce[8-len(nonce):], nonce)
    }

    nonceValue := binary.BigEndian.Uint64(paddedNonce)
    bigEndianNonce := make([]byte, 8)
    binary.BigEndian.PutUint64(bigEndianNonce, nonceValue)
    return bigEndianNonce
}

func nonceBytesToHex(nonce []byte) string {
    return "0x" + hex.EncodeToString(nonceBytesBigEndian(nonce))
}

func isZeroBytes(input []byte) bool {
    for _, b := range input {
        if b != 0 {
            return false
        }
    }
    return true
}

func shortHex(hexStr string, length int) string {
    if len(hexStr) <= length {
        return hexStr
    }
    return hexStr[:length]
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

    // Miner's original params: [nonce, headerHash, mixDigest]
    nonceHex := params[0]
    minerHeaderHashHex := params[1]
    mixDigestHex := params[2]

    // DEBUG: Log what we have
    log.Printf("DEBUG: t.Header = %s", t.Header)
    log.Printf("DEBUG: t.Seed = %s", t.Seed)
    log.Printf("DEBUG: Miner nonce = %s", nonceHex)
    log.Printf("DEBUG: Miner headerHash = %s", shortHex(minerHeaderHashHex, 16))
    log.Printf("DEBUG: Miner mixDigest = %s", shortHex(mixDigestHex, 16))

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
    if !strings.HasPrefix(minerHeaderHashHex, "0x") {
        formattedParams[1] = "0x" + minerHeaderHashHex
    } else {
        formattedParams[1] = minerHeaderHashHex
    }
    
    // Parameter 3: Mix digest (use miner's mix digest)
    if !strings.HasPrefix(mixDigestHex, "0x") {
        formattedParams[2] = "0x" + mixDigestHex
    } else {
        formattedParams[2] = mixDigestHex
    }
    
    log.Printf("Prepared daemon submit params (only block candidates are sent to daemon):")
    log.Printf("  nonce=%s", formattedParams[0])
    log.Printf("  headerHash=%s...", shortHex(formattedParams[1], 16))
    log.Printf("  mixDigest=%s...", shortHex(formattedParams[2], 16))

    // Calculate share difficulty for logging and local pool validation.
    mixDigest := hexToBytes(mixDigestHex)
    hashDiff := randomXHashDifficulty(mixDigest)
    shareDiff := s.GetPoolShareDifficulty()
    poolDiff := big.NewInt(shareDiff)

    var networkDiff *big.Int
    if t.Difficulty != nil && t.Difficulty.Sign() > 0 {
        networkDiff = t.Difficulty
    } else {
        networkDiff = big.NewInt(0)
    }

    // eth_submitWork only accepts full block candidates.  Validate normal pool
    // shares locally so valid shares below network difficulty are not rejected
    // by the daemon, as shown by Hash Diff < Network Diff in the logs.
    seedHash := hexToBytes(t.Seed)
    headerHash := hexToBytes(minerHeaderHashHex)
    nonce := hexToBytes(nonceHex)
    validShare, verifiedHash, verifiedNonce, err := s.verifyRandomXShare(t, seedHash, headerHash, nonce, mixDigest, poolDiff)
    if err != nil {
        log.Printf("RandomX share verification error: %v", err)
        return false, false
    }
    if !validShare {
        if len(verifiedHash) > 0 {
            hashDiff = randomXHashDifficulty(verifiedHash)
        }
        log.Printf("Share REJECTED locally - Hash Diff: %s, Pool Diff: %s", hashDiff.String(), poolDiff.String())
        return false, false
    }

    verifiedNonceHex := nonceBytesToHex(verifiedNonce)
    verifiedMixDigestHex := "0x" + hex.EncodeToString(verifiedHash)
    formattedParams[0] = verifiedNonceHex
    formattedParams[2] = verifiedMixDigestHex
    verifiedParams := []string{verifiedNonceHex, formattedParams[1], verifiedMixDigestHex}
    hashDiff = randomXHashDifficulty(verifiedHash)
    log.Printf("Share - Hash Diff: %s, Pool Diff: %s, Network Diff: %s", hashDiff.String(), poolDiff.String(), networkDiff.String())

    // Only submit block candidates to the daemon.  Regular shares have already
    // been verified against the pool difficulty and should be accepted locally.
    blockCandidate := randomXHashMeetsTarget(verifiedHash, t.Target)
    if !blockCandidate && networkDiff.Sign() > 0 {
        blockCandidate = hashDiff.Cmp(networkDiff) >= 0
    }
    if blockCandidate {
        log.Printf("Block candidate detected - Hash Diff: %s, Network Diff: %s", hashDiff.String(), networkDiff.String())
        ok, err := s.rpc().SubmitBlock(formattedParams)
        if err != nil {
            log.Printf("SubmitBlock error: %v", err)
        } else if !ok {
            log.Printf("Block candidate REJECTED by daemon; accepting as regular share")
        } else {
            log.Printf("Block candidate ACCEPTED by daemon")
            log.Printf("������ BLOCK FOUND and ACCEPTED! ������")

            // Fetch new template
            go s.fetchRandomXBlockTemplate()

            // Record the block in backend
            exist, err := s.backend.WriteBlock(login, id, verifiedParams, shareDiff, networkDiff.Int64(), t.Height, s.hashrateExpiration)
            if exist {
                return true, false
            }
            if err != nil {
                log.Printf("Failed to write block to backend: %v", err)
                return false, false
            }

            return false, true
        }
    } else {
        log.Printf("Accepted share is not a block candidate - Hash Diff: %s, Network Diff: %s", hashDiff.String(), networkDiff.String())
    }

    // Regular share - record it
    exist, err := s.backend.WriteShare(login, id, verifiedParams, shareDiff, t.Height, s.hashrateExpiration)
    if exist {
        return true, false
    }
    if err != nil {
        log.Printf("Failed to write share to backend: %v", err)
        return false, false
    }
    log.Printf("Share ACCEPTED locally")
    return false, true
}
