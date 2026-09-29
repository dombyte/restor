BINARY_NAME := restor

# Get version info
VERSION := $(shell git describe --tags --always 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')

# Build info lives in package cmd (see AGENTS.md "Migration backlog")
PKG := github.com/dombyte/restor/cmd

# Build flags for small binary
LDFLAGS := -s -w
BUILD_FLAGS := -ldflags "-X $(PKG).Version=$(VERSION) -X $(PKG).GitCommit=$(COMMIT) -X $(PKG).BuildDate=$(DATE) $(LDFLAGS)"

.PHONY: build
build:
	CGO_ENABLED=0 go build $(BUILD_FLAGS) -o $(BINARY_NAME) .

.PHONY: clean
clean:
	rm -f $(BINARY_NAME)

.PHONY: all
all: build

.PHONY: run
run: build
	./$(BINARY_NAME) --config config.yaml

.PHONY: check
check:
	./scripts/pre-commit.sh
