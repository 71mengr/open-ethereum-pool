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
    "time"
    "unsafe"
)

const (
    RandomXEpochLength = 2048
    RandomXCacheSize   = 256 * 1024 * 1024 // 256MB
    RandomXDatasetSize = 2 * 1024 * 1024 * 1024 // 2GB
    MaxConcurrentVerifications = 32
)

// RandomX flags constants
const (
    RandomXFlagDefault C.randomx_flags = 0
    RandomXFlagFullMem C.randomx_flags = 1
    RandomXFlagJIT     C.randomx_flags = 2
    RandomXFlagHardAES C.randomx_flags = 4
    RandomXFlagLargePages C.randomx_flags = 8
)

type RandomXCache struct {
    cache     *C.randomx_cache
    vm        *C.randomx_vm
    epoch     uint64
    createdAt time.Time
    lastUsed  time.Time
    mu        sync.RWMutex
}

type RandomXManager struct {
    caches       map[uint64]*RandomXCache
    mu           sync.RWMutex
    currentEpoch uint64
    semaphore    chan struct{}
    maxThreads   int
}

func NewRandomXManager() *RandomXManager {
    return &RandomXManager{
        caches:    make(map[uint64]*RandomXCache),
        semaphore: make(chan struct{}, MaxConcurrentVerifications),
        maxThreads: 4,
    }
}

func (m *RandomXManager) GetCache(epoch uint64, seedHash []byte) (*RandomXCache, error) {
    if len(seedHash) == 0 {
        return nil, fmt.Errorf("seed hash cannot be empty")
    }

    // Fast path: try read lock first
    m.mu.RLock()
    cache, exists := m.caches[epoch]
    m.mu.RUnlock()

    if exists {
        cache.updateLastUsed()
        return cache, nil
    }

    // Slow path: create new cache with write lock
    m.mu.Lock()
    defer m.mu.Unlock()

    // Double check after acquiring write lock
    if cache, exists = m.caches[epoch]; exists {
        cache.updateLastUsed()
        return cache, nil
    }

    log.Printf("Creating RandomX cache for epoch %d, seed hash: %x", epoch, seedHash[:8])
    startTime := time.Now()

    // Use default flags with JIT and AES for better performance
    flags := RandomXFlagJIT | RandomXFlagHardAES

    // Allocate cache
    cCache := C.randomx_alloc_cache(flags)
    if cCache == nil {
        // Try without JIT
        flags = RandomXFlagHardAES
        cCache = C.randomx_alloc_cache(flags)
        if cCache == nil {
            // Try with default flags
            cCache = C.randomx_alloc_cache(RandomXFlagDefault)
            if cCache == nil {
                return nil, fmt.Errorf("failed to allocate RandomX cache for epoch %d", epoch)
            }
        }
    }

    // Initialize cache with seed hash
    seedPtr := unsafe.Pointer(&seedHash[0])
    C.randomx_init_cache(cCache, seedPtr, C.size_t(len(seedHash)))

    // Create VM with the cache
    cVm := C.randomx_create_vm(flags, cCache, nil)
    if cVm == nil {
        // Try with default flags
        cVm = C.randomx_create_vm(RandomXFlagDefault, cCache, nil)
        if cVm == nil {
            C.randomx_release_cache(cCache)
            return nil, fmt.Errorf("failed to create RandomX VM for epoch %d", epoch)
        }
    }

    cache = &RandomXCache{
        cache:     cCache,
        vm:        cVm,
        epoch:     epoch,
        createdAt: startTime,
        lastUsed:  time.Now(),
    }
    m.caches[epoch] = cache

    log.Printf("RandomX cache created successfully for epoch %d in %v", epoch, time.Since(startTime))

    // Clean old caches (keep last 3 epochs)
    for e, c := range m.caches {
        if e+3 < epoch {
            c.Close()
            delete(m.caches, e)
        }
    }

    return cache, nil
}

func (m *RandomXManager) cleanOldCaches(currentEpoch uint64) {
    for epoch, cache := range m.caches {
        if epoch+3 < currentEpoch {
            log.Printf("Cleaning old RandomX cache for epoch %d", epoch)
            cache.Close()
            delete(m.caches, epoch)
        }
    }
}

func (m *RandomXManager) Close() {
    m.mu.Lock()
    defer m.mu.Unlock()

    for epoch, cache := range m.caches {
        log.Printf("Closing RandomX cache for epoch %d", epoch)
        cache.Close()
        delete(m.caches, epoch)
    }
}

func (c *RandomXCache) updateLastUsed() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.lastUsed = time.Now()
}

func (c *RandomXCache) ComputeHash(headerHash, nonce []byte) ([]byte, error) {
    if len(headerHash) != 32 {
        return nil, fmt.Errorf("invalid header hash length: %d, expected 32", len(headerHash))
    }
    if len(nonce) != 8 {
        return nil, fmt.Errorf("invalid nonce length: %d, expected 8", len(nonce))
    }

    c.mu.RLock()
    defer c.mu.RUnlock()

    if c.vm == nil {
        return nil, fmt.Errorf("RandomX VM is nil for epoch %d", c.epoch)
    }

    // Input: 32 bytes headerHash + 8 bytes nonce = 40 bytes
    input := make([]byte, 40)
    copy(input[:32], headerHash)
    copy(input[32:40], nonce[:8])

    output := make([]byte, 32)
    C.randomx_calculate_hash(c.vm, unsafe.Pointer(&input[0]), C.size_t(len(input)), unsafe.Pointer(&output[0]))

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

// GetCacheInfo returns information about the cache for debugging
func (c *RandomXCache) GetCacheInfo() map[string]interface{} {
    c.mu.RLock()
    defer c.mu.RUnlock()

    return map[string]interface{}{
        "epoch":      c.epoch,
        "created_at": c.createdAt,
        "last_used":  c.lastUsed,
        "has_vm":     c.vm != nil,
        "has_cache":  c.cache != nil,
    }
}
