package proxy

import (
	"log"
	"sync"
)

type RandomXManager struct {
	caches map[uint64]*Cache
	mu     sync.RWMutex
}

func NewRandomXManager() *RandomXManager {
	return &RandomXManager{
		caches: make(map[uint64]*Cache),
	}
}

func (m *RandomXManager) GetCache(epoch uint64, seed []byte) (*Cache, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return existing cache if available
	if cache, ok := m.caches[epoch]; ok && cache != nil {
		return cache, nil
	}

	// Create new cache
	cache := NewCache(RANDOMX_FLAG_FULL_MEM | RANDOMX_FLAG_JIT)
	if cache == nil {
		return nil, ErrFailedToCreateCache
	}

	cache.Init(seed)
	m.caches[epoch] = cache

	// Cleanup old caches (keep current + previous epoch)
	m.cleanupOldCaches(epoch)

	log.Printf("✅ Created new RandomX cache for epoch %d", epoch)
	return cache, nil
}

func (m *RandomXManager) cleanupOldCaches(currentEpoch uint64) {
	for e, c := range m.caches {
		if e+2 < currentEpoch { // Keep last 2 epochs
			if c != nil {
				c.Close()
			}
			delete(m.caches, e)
		}
	}
}

// Global error
var ErrFailedToCreateCache = NewStratumError(20, "failed to create RandomX cache")
