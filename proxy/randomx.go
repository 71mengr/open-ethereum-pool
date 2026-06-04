// +build cgo,randomx

package proxy

/*
#cgo CFLAGS: -I${SRCDIR}/../build/_workspace/RandomX/src
#cgo LDFLAGS: -L${SRCDIR}/../build/_workspace/RandomX/build -lrandomx -lstdc++ -lm
#include <stdlib.h>
#include <string.h>
#include "randomx.h"
*/
import "C"
import (
    "fmt"
    "log"
    "sync"
    "unsafe"
)

const (
    RandomXEpochLength = 2048
    RandomXCacheSize   = 256 * 1024 * 1024 // 256MB
    RandomXDatasetSize = 2 * 1024 * 1024 * 1024 // 2GB
)

type RandomXCache struct {
    cache *C.randomx_cache
    vm    *C.randomx_vm
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

    if cache, exists = m.caches[epoch]; exists {
        return cache, nil
    }

    log.Printf("Creating RandomX cache for epoch %d, seed hash: %x", epoch, seedHash[:8])

    // Create cache with default flags (0)
    cCache := C.randomx_alloc_cache(0)
    if cCache == nil {
        return nil, fmt.Errorf("failed to allocate RandomX cache")
    }

    // Initialize cache with seed hash
    if len(seedHash) > 0 {
        C.randomx_init_cache(cCache, unsafe.Pointer(&seedHash[0]), C.size_t(len(seedHash)))
    }

    // Create VM with default flags (0)
    cVm := C.randomx_create_vm(0, cCache, nil)
    if cVm == nil {
        C.randomx_release_cache(cCache)
        return nil, fmt.Errorf("failed to create RandomX VM")
    }

    cache = &RandomXCache{
        cache: cCache,
        vm:    cVm,
        epoch: epoch,
    }
    m.caches[epoch] = cache

    log.Printf("RandomX cache created successfully for epoch %d", epoch)

    // Clean old caches (keep last 3 epochs)
    for e, c := range m.caches {
        if e+3 < epoch {
            c.Close()
            delete(m.caches, e)
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

    if len(input) > 0 {
        C.randomx_calculate_hash(c.vm, unsafe.Pointer(&input[0]), C.size_t(len(input)), unsafe.Pointer(&output[0]))
    }

    return output, nil
}

func (c *RandomXCache) Close() {
    c.mu.Lock()
    defer c.mu.Unlock()
    if c.vm != nil {
        C.randomx_destroy_vm(c.vm)
        c.vm = nil
    }
    if c.cache != nil {
        C.randomx_release_cache(c.cache)
        c.cache = nil
    }
}
