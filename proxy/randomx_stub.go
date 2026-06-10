//go:build !cgo || !randomx
// +build !cgo !randomx

package proxy

const (
	RANDOMX_FLAG_DEFAULT     = 0
	RANDOMX_FLAG_LARGE_PAGES = 1
	RANDOMX_FLAG_HARD_AES    = 2
	RANDOMX_FLAG_FULL_MEM    = 4
	RANDOMX_FLAG_JIT         = 8
	RANDOMX_FLAG_SECURE      = 16
)

type Cache struct{}

func NewCache(flags int) *Cache {
	return nil
}

func (c *Cache) Init(seed []byte) {}

func (c *Cache) ComputeHash(headerHash, nonce []byte) ([]byte, error) {
	return nil, ErrFailedToCreateCache
}

func (c *Cache) Close() {}

type Dataset struct{}

func NewDataset(flags int) *Dataset {
	return nil
}

func (d *Dataset) InitDataset(cache *Cache, start, count uint32) {}

func (d *Dataset) Close() {}

type VM struct{}

func NewVM(flags int, cache *Cache, dataset *Dataset) *VM {
	return nil
}

func (vm *VM) CalculateHash(input, output []byte) {}

func (vm *VM) Close() {}
