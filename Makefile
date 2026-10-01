BINARY  := bag
PKG     := github.com/rytsh/bag
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo v0.0.0)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo -)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.DEFAULT_GOAL := help

.PHONY: build
build: ## Build the bag binary (pure Go, CGO disabled)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/bag

.PHONY: install
install: ## Install bag into GOBIN
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/bag

.PHONY: test
test: ## Run tests
	go test -race -v -cover ./...

.PHONY: lint
lint: ## Run vet and gofmt checks
	go vet ./...
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal && exit 1)

.PHONY: parity
parity: build ## Compare bag against Graphify on a directory: make parity DIR=path
	@test -n "$(DIR)" || (echo "usage: make parity DIR=path" && exit 1)
	cd $(DIR) && graphify extract . --code-only >/dev/null && $(CURDIR)/$(BINARY) extract . --out bag-out --no-cache --force
	python3 scripts/compare.py $(DIR)/graphify-out/graph.json $(DIR)/bag-out/graph.json

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BINARY) dist

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'
