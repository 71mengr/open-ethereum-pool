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
)

// RandomX flags constants
const (
	RandomXFlagDefault  = C.randomx_flags(0)
	RandomXFlagHardAES  = C.randomx_flags(4)
)

type RandomXCache struct {
	cache *C.randomx_cache
	vm    *C.randomx_vm
	epoch uint64
	mu    sync.RWMutex
}

type RandomXManager struct {
	caches map[uint64]*RandomXCache
	mu     sync.RWMutex
}

func NewRandomXManager() *RandomXManager {
	return &RandomXManager{
		caches: make(map[uint64]*RandomXCache),
	}
}

func (m *RandomXManager) GetCache(epoch uint64, seedHash []byte) (*RandomXCache, error) {
	if len(seedHash) == 0 {
		return nil, fmt.Errorf("seed hash cannot be empty")
	}

	// Fast path - check if cache exists
	m.mu.RLock()
	cache, exists := m.caches[epoch]
	m.mu.RUnlock()

	if exists && cache != nil && cache.vm != nil {
		return cache, nil
	}

	// Create new cache
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double check after lock
	if cache, exists = m.caches[epoch]; exists && cache != nil && cache.vm != nil {
		return cache, nil
	}

	log.Printf("Creating RandomX cache for epoch %d", epoch)
	startTime := time.Now()

	// Allocate cache
	cCache := C.randomx_alloc_cache(RandomXFlagHardAES)
	if cCache == nil {
		cCache = C.randomx_alloc_cache(RandomXFlagDefault)
		if cCache == nil {
			return nil, fmt.Errorf("failed to allocate RandomX cache for epoch %d", epoch)
		}
	}

	// Initialize cache
	seedPtr := unsafe.Pointer(&seedHash[0])
	C.randomx_init_cache(cCache, seedPtr, C.size_t(len(seedHash)))

	// Create VM
	cVm := C.randomx_create_vm(RandomXFlagDefault, cCache, nil)
	if cVm == nil {
		C.randomx_release_cache(cCache)
		return nil, fmt.Errorf("failed to create RandomX VM for epoch %d", epoch)
	}

	cache = &RandomXCache{
		cache: cCache,
		vm:    cVm,
		epoch: epoch,
	}
	m.caches[epoch] = cache

	log.Printf("RandomX cache created for epoch %d in %v", epoch, time.Since(startTime))

	// Clean old caches
	for e, c := range m.caches {
		if e+3 < epoch {
			c.Close()
			delete(m.caches, e)
		}
	}

	return cache, nil
}

func (c *RandomXCache) ComputeHash(blob []byte) ([]byte, error) {
    if len(blob) == 0 {
        return nil, fmt.Errorf("empty input")
    }
    
    c.mu.RLock()
    defer c.mu.RUnlock()
    
    if c.vm == nil {
        // Try to recreate VM
        if c.cache != nil {
            c.mu.RUnlock()
            c.mu.Lock()
            c.vm = C.randomx_create_vm(RandomXFlagDefault, c.cache, nil)
            c.mu.Unlock()
            c.mu.RLock()
            if c.vm == nil {
                return nil, fmt.Errorf("RandomX VM is nil for epoch %d", c.epoch)
            }
        } else {
            return nil, fmt.Errorf("RandomX VM and cache are nil for epoch %d", c.epoch)
        }
    }
    
    output := make([]byte, 32)
    C.randomx_calculate_hash(c.vm, unsafe.Pointer(&blob[0]), C.size_t(len(blob)), unsafe.Pointer(&output[0]))
    
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

func (m *RandomXManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, cache := range m.caches {
		cache.Close()
	}
	m.caches = make(map[uint64]*RandomXCache)
}
