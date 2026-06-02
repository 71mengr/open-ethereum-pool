package proxy

import (
    "fmt"
    "sync"

    randomx "github.com/sammy007/open-ethereum-pool/randomx"
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
    caches       map[uint64]*RandomXCache
    mu           sync.RWMutex
    currentEpoch uint64
}

func NewRandomXManager() *RandomXManager {
    return &RandomXManager{
        caches: make(map[uint64]*RandomXCache),
    }
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
    if randomxCache == nil {
        return nil, fmt.Errorf("failed to create RandomX cache")
    }
    vm := randomx.NewVM(randomxCache, nil, randomx.FlagFullMem)
    if vm == nil {
        randomxCache.Close()
        return nil, fmt.Errorf("failed to create RandomX VM")
    }
    
    cache = &RandomXCache{
        cache: randomxCache,
        vm:    vm,
        epoch: epoch,
    }
    m.caches[epoch] = cache
    
    // Clean old caches
    if epoch > 2 {
        if oldCache, ok := m.caches[epoch-2]; ok {
            oldCache.Close()
            delete(m.caches, epoch-2)
        }
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
