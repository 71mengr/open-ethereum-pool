package proxy

import (
    "bytes"
    "encoding/binary"
    "fmt"
    "math/big"
    "sync"

    randomx "github.com/tevador/randomx"
)

const (
    RandomXEpochLength = 2048
    RandomXCacheSize   = 256 * 1024 * 1024 // 256MB
    RandomXDatasetSize = 2 * 1024 * 1024 * 1024 // 2GB
)

type RandomXCache struct {
    cache *randomx.Cache
    vm    *randomx.VM
    epoch uint64
    mu    sync.RWMutex
}

type RandomXManager struct {
    caches     map[uint64]*RandomXCache
    mu         sync.RWMutex
    rpc        *rpc.RPCClient
    currentEpoch uint64
}

func NewRandomXManager(rpc *rpc.RPCClient) *RandomXManager {
    return &RandomXManager{
        caches: make(map[uint64]*RandomXCache),
        rpc:    rpc,
    }
}

func (m *RandomXManager) GetSeedHash(height uint64) ([]byte, error) {
    var seedHash string
    err := m.rpc.Call(&seedHash, "randomx_getSeedHash", height)
    if err != nil {
        return nil, fmt.Errorf("failed to get seed hash: %v", err)
    }
    return hexToBytes(seedHash), nil
}

func (m *RandomXManager) GetCache(epoch uint64, seedHash []byte) (*RandomXCache, error) {
    m.mu.RLock()
    cache, exists := m.caches[epoch]
    m.mu.RUnlock()
    
    if exists {
        return cache, nil
    }
    
    m.mu.Lock()
    defer m.mu.Unlock()
    
    // Double-check after acquiring write lock
    if cache, exists = m.caches[epoch]; exists {
        return cache, nil
    }
    
    // Create new cache
    randomxCache := randomx.NewCache(seedHash)
    vm := randomx.NewVM(randomxCache, nil, randomx.FlagFullMem)
    
    cache = &RandomXCache{
        cache: randomxCache,
        vm:    vm,
        epoch: epoch,
    }
    m.caches[epoch] = cache
    
    // Clean old caches
    if epoch > 2 {
        delete(m.caches, epoch-2)
    }
    
    return cache, nil
}

func (c *RandomXCache) ComputeHash(headerHash, nonce []byte) ([]byte, error) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    
    // Input: 32 bytes headerHash + 8 bytes nonce = 40 bytes
    input := make([]byte, 40)
    copy(input[:32], headerHash)
    copy(input[32:], nonce)
    
    output := make([]byte, 32)
    c.vm.CalculateHash(input, output)
    return output, nil
}

func (c *RandomXCache) Close() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.vm.Close()
    c.cache.Close()
}

func hexToBytes(hex string) []byte {
    if len(hex) >= 2 && hex[:2] == "0x" {
        hex = hex[2:]
    }
    bytes := make([]byte, len(hex)/2)
    for i := 0; i < len(hex); i += 2 {
        fmt.Sscanf(hex[i:i+2], "%02x", &bytes[i/2])
    }
    return bytes
}
