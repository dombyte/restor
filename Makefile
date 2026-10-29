BINARY_NAME := restor

# Get version info
VERSION := $(shell git describe --tags --always 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
GOVERSION := $(shell go version | awk '{print $$3}')

# Build flags for small binary
LDFLAGS := -s -w
BUILD_FLAGS := -ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildDate=$(DATE) -X main.GoVersion=$(GOVERSION) $(LDFLAGS)"

.PHONY: build
build:
	CGO_ENABLED=0 go build $(BUILD_FLAGS) -o $(BINARY_NAME) ./cmd

.PHONY: clean
clean:
	rm -f $(BINARY_NAME)

.PHONY: all
all: build

.PHONY: run
run: build
	./$(BINARY_NAME) run --config config.yaml

.PHONY: check
check:
	./scripts/pre-commit.sh

# Integration tests against the restic on PATH (not part of check: they need restic).
.PHONY: integration
integration:
	go test -tags integration -race -count=1 -run TestIntegration ./internal/app ./cmd

# The same against the oldest restic restor supports (minSupported in
# internal/restic/version.go), downloaded once into the user's cache directory.
RESTIC_OLDEST := $(shell sed -n 's/^[[:space:]]*minSupported = "\(.*\)"/\1/p' internal/restic/version.go)
RESTIC_CACHE := $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/restor/restic/$(RESTIC_OLDEST)

.PHONY: integration-oldest
integration-oldest:
	./scripts/install-restic.sh $(RESTIC_OLDEST) $(RESTIC_CACHE)
	PATH="$(RESTIC_CACHE):$$PATH" $(MAKE) integration
