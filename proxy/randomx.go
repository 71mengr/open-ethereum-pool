//go:build cgo && randomx
// +build cgo,randomx

package proxy

/*
#cgo CFLAGS: -I../build/_workspace/RandomX/src
#cgo LDFLAGS: -L../build/_workspace/RandomX/build -lrandomx -lstdc++ -lm
#include <stdlib.h>
#include "randomx.h"
*/
import "C"

import (
	"unsafe"
)

// Flags
const (
	RANDOMX_FLAG_DEFAULT     = 0
	RANDOMX_FLAG_FULL_MEM    = 1
	RANDOMX_FLAG_JIT         = 2
	RANDOMX_FLAG_HARD_AES    = 4
	RANDOMX_FLAG_LARGE_PAGES = 8
	RANDOMX_FLAG_SECURE      = 16
)

// Cache
type Cache struct {
	ptr *C.randomx_cache
}

func NewCache(flags int) *Cache {
	c := C.randomx_alloc_cache(C.randomx_flags(flags))
	if c == nil {
		return nil
	}
	return &Cache{ptr: c}
}

func (c *Cache) Init(seed []byte) {
	if c == nil || c.ptr == nil {
		return
	}
	var seedPtr unsafe.Pointer
	if len(seed) > 0 {
		seedPtr = unsafe.Pointer(&seed[0])
	}
	C.randomx_init_cache(c.ptr, seedPtr, C.size_t(len(seed)))
}

// ComputeHash is the main method used by the proxy
func (c *Cache) ComputeHash(headerHash, nonce []byte) ([]byte, error) {
	if c == nil || c.ptr == nil {
		return nil, ErrFailedToCreateCache
	}

	// Input = headerHash + nonce (standard RandomX input format)
	input := make([]byte, len(headerHash)+len(nonce))
	copy(input, headerHash)
	copy(input[len(headerHash):], nonce)

	output := make([]byte, 32)

	vm := NewVM(RANDOMX_FLAG_FULL_MEM| RANDOMX_FLAG_JIT, c, nil)
	if vm == nil {
		return nil, ErrFailedToCreateCache
	}
	defer vm.Close()

	vm.CalculateHash(input, output)
	return output, nil
}

func (c *Cache) Close() {
	if c != nil && c.ptr != nil {
		C.randomx_release_cache(c.ptr)
		c.ptr = nil
	}
}

// Dataset (kept for completeness, though not heavily used in proxy)
type Dataset struct {
	ptr *C.randomx_dataset
}

func NewDataset(flags int) *Dataset {
	d := C.randomx_alloc_dataset(C.randomx_flags(flags))
	if d == nil {
		return nil
	}
	return &Dataset{ptr: d}
}

func (d *Dataset) InitDataset(cache *Cache, start, count uint32) {
	if d == nil || d.ptr == nil || cache == nil || cache.ptr == nil {
		return
	}
	C.randomx_init_dataset(d.ptr, cache.ptr, C.uint32_t(start), C.uint32_t(count))
}

func (d *Dataset) Close() {
	if d != nil && d.ptr != nil {
		C.randomx_release_dataset(d.ptr)
		d.ptr = nil
	}
}

// VM
type VM struct {
	ptr *C.randomx_vm
}

func NewVM(flags int, cache *Cache, dataset *Dataset) *VM {
	var cCache *C.randomx_cache
	var cDataset *C.randomx_dataset
	if cache != nil {
		cCache = cache.ptr
	}
	if dataset != nil {
		cDataset = dataset.ptr
	}

	vm := C.randomx_create_vm(C.randomx_flags(flags), cCache, cDataset)
	if vm == nil {
		return nil
	}
	return &VM{ptr: vm}
}

func (vm *VM) CalculateHash(input, output []byte) {
	if vm == nil || vm.ptr == nil || len(output) == 0 {
		return
	}
	var inputPtr unsafe.Pointer
	if len(input) > 0 {
		inputPtr = unsafe.Pointer(&input[0])
	}
	C.randomx_calculate_hash(vm.ptr, inputPtr, C.size_t(len(input)), unsafe.Pointer(&output[0]))
}

func (vm *VM) Close() {
	if vm != nil && vm.ptr != nil {
		C.randomx_destroy_vm(vm.ptr)
		vm.ptr = nil
	}
}
