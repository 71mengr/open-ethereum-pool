# This Makefile is meant to be used by people that do not usually work
# with Go source code. If you know what GOPATH is then you probably
# don't need to bother with make.

.PHONY: all test clean randomx

GOBIN = build/bin

# RandomX library settings
RANDOMX_REPO = https://github.com/tevador/RandomX.git
RANDOMX_DIR = build/_workspace/RandomX
RANDOMX_BUILD = $(RANDOMX_DIR)/build
RANDOMX_LIB = $(RANDOMX_BUILD)/librandomx.a

# CGO settings for RandomX
CGO_ENABLED = 1
CGO_CFLAGS = -I$(RANDOMX_DIR)/src
CGO_LDFLAGS = -L$(RANDOMX_BUILD) -lrandomx -lstdc++ -lm

# Check if RandomX is already built
randomx:
	@if [ ! -f $(RANDOMX_LIB) ]; then \
		set -e; \
		echo "Building RandomX library..."; \
		mkdir -p build/_workspace; \
		if [ ! -d $(RANDOMX_DIR) ]; then \
			git clone $(RANDOMX_REPO) $(RANDOMX_DIR); \
		fi; \
		mkdir -p $(RANDOMX_BUILD); \
		cd $(RANDOMX_BUILD) && cmake .. -DARCH=native -DBUILD_SHARED_LIBS=OFF; \
		make -j4; \
		echo "RandomX library built successfully"; \
	fi

# Build with RandomX support
all: randomx
	@echo "Building open-ethereum-pool with RandomX support..."
	@echo "CGO_CFLAGS=$(CGO_CFLAGS)"
	@echo "CGO_LDFLAGS=$(CGO_LDFLAGS)"
	build/env.sh go mod download
	CGO_ENABLED=$(CGO_ENABLED) CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" build/env.sh go build -v -o $(GOBIN)/open-ethereum-pool main.go
	@echo "Build complete! Binary: $(GOBIN)/open-ethereum-pool"

# Build without RandomX (Ethash only)
all-legacy:
	build/env.sh go mod download
	build/env.sh go build -v -o $(GOBIN)/open-ethereum-pool main.go

test: all
	build/env.sh go test -v ./...

clean:
	rm -fr build/_workspace/pkg/ $(GOBIN)/*
	@if [ -d $(RANDOMX_BUILD) ]; then \
		echo "Cleaning RandomX build..."; \
		rm -rf $(RANDOMX_BUILD); \
	fi

# Clean everything including RandomX source
clean-all: clean
	@if [ -d $(RANDOMX_DIR) ]; then \
		echo "Removing RandomX source..."; \
		rm -rf $(RANDOMX_DIR); \
	fi

# Quick build with verbose output
debug: randomx
	CGO_ENABLED=$(CGO_ENABLED) CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" build/env.sh go build -v -x -o $(GOBIN)/open-ethereum-pool main.go

# Build for specific RandomX flags (AVX2)
randomx-avx2: clean-randomx
	@echo "Building RandomX with AVX2 support..."
	mkdir -p build/_workspace
	if [ ! -d $(RANDOMX_DIR) ]; then \
		git clone $(RANDOMX_REPO) $(RANDOMX_DIR); \
	fi
	mkdir -p $(RANDOMX_BUILD)
	cd $(RANDOMX_BUILD) && cmake .. -DARCH=haswell -DBUILD_SHARED_LIBS=OFF
	make -j4
	@echo "RandomX AVX2 build complete"

# Build for specific RandomX flags (AVX512)
randomx-avx512: clean-randomx
	@echo "Building RandomX with AVX512 support..."
	mkdir -p build/_workspace
	if [ ! -d $(RANDOMX_DIR) ]; then \
		git clone $(RANDOMX_REPO) $(RANDOMX_DIR); \
	fi
	mkdir -p $(RANDOMX_BUILD)
	cd $(RANDOMX_BUILD) && cmake .. -DARCH=skylake-avx512 -DBUILD_SHARED_LIBS=OFF
	make -j4
	@echo "RandomX AVX512 build complete"

# Clean only RandomX build
clean-randomx:
	@if [ -d $(RANDOMX_BUILD) ]; then \
		echo "Cleaning RandomX build directory..."; \
		rm -rf $(RANDOMX_BUILD); \
	fi

# Help target
help:
	@echo "Available targets:"
	@echo "  all             - Build with RandomX support (default)"
	@echo "  all-legacy      - Build without RandomX (Ethash only)"
	@echo "  debug           - Build with verbose output"
	@echo "  test            - Run tests"
	@echo "  clean           - Remove build artifacts"
	@echo "  clean-all       - Remove build artifacts and RandomX source"
	@echo "  randomx-avx2    - Build RandomX with AVX2 optimization"
	@echo "  randomx-avx512  - Build RandomX with AVX512 optimization"
	@echo "  help            - Show this help"
