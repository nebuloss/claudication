BINARY  := claudication
CMD     := ./cmd/claudication
DIST    := dist
WEBDIST := internal/httpapi/webdist
PKG     := claudication/internal/version

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)
GOFLAGS := -trimpath

# The platforms a release ships. modernc.org/sqlite carries a generated
# translation per GOOS/GOARCH, so this list is bounded by what it supports
# rather than by what Go can target — and because the whole binary is pure Go,
# one machine cross-compiles all of them without a toolchain per target.
PLATFORMS ?= linux/amd64 linux/arm64 linux/arm linux/riscv64 \
             darwin/amd64 darwin/arm64 \
             windows/amd64 windows/arm64 \
             freebsd/amd64

.PHONY: help all build backend web web-deps dev dev-api run dist \
        fmt fmt-check vet tidy test test-go test-web typecheck web-build \
        docs docs-check tools check clean

## help: list the targets worth knowing about
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /' | sort

all: build

## build: compile the UI into the binary, producing dist/claudication
build: web tools backend

backend:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/$(BINARY) $(CMD)
	@echo "built $(DIST)/$(BINARY) ($(VERSION))"

## dist: cross-compile every release platform, with checksums
dist: web
	@rm -rf $(DIST)/release && mkdir -p $(DIST)/release
	@set -e; for p in $(PLATFORMS); do \
	  os=$${p%%/*}; arch=$${p##*/}; ext=; \
	  if [ "$$os" = windows ]; then ext=.exe; fi; \
	  echo "  $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch GOARM=7 \
	    go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
	    -o $(DIST)/release/$(BINARY)-$$os-$$arch$$ext $(CMD); \
	done
	@cd $(DIST)/release && sha256sum * > SHA256SUMS
	@echo; ls -1 $(DIST)/release

## web-deps: install the UI's dependencies from the lockfile
web-deps:
	cd web && npm ci

## web: build the UI and stage it where go:embed picks it up
web:
	cd web && npm run build
	@rm -rf $(WEBDIST)
	@mkdir -p $(WEBDIST)
	@cp -r web/dist/. $(WEBDIST)/
	@touch $(WEBDIST)/.gitkeep

## dev: Vite dev server on :5173, proxying the API to a local gateway
dev:
	cd web && npm run dev

## dev-api: the gateway only, with debug logging
dev-api:
	CLAUDICATION_LOG_LEVEL=debug CLAUDICATION_LOG_FORMAT=text go run $(CMD) serve

## run: build everything, then serve
run: build
	./$(DIST)/$(BINARY) serve

## typecheck: tsc in its own right — vite strips types without checking them
typecheck:
	cd web && npm run typecheck

## fmt: format the Go sources in place
fmt:
	gofmt -w cmd internal

fmt-check:
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; echo 'run make fmt'; exit 1; }

vet: tools
	go vet ./...

tidy:
	go mod tidy

## test: the Go suite, with the race detector
test: test-go

test-go: tools
	CGO_ENABLED=1 go test ./... -race -count=1

# Race detection needs cgo, which a musl or scratch toolchain may not have.
test-norace:
	CGO_ENABLED=0 go test ./... -count=1

# The helper scripts the admin UI offers for download. scripts/ stays the one
# copy; this puts them where go:embed can reach them, the same way `make web`
# does for the built UI.
TOOLS := codex-model-catalog.py

## tools: stage the downloadable helper scripts for embedding
tools:
	@mkdir -p internal/httpapi/tools
	@for t in $(TOOLS); do cp scripts/$$t internal/httpapi/tools/$$t; done
	@touch internal/httpapi/tools/.gitkeep

## docs: regenerate the documentation that comes from configs/
docs:
	scripts/gen-client-docs.py

# The client recipes live in configs/clients.json and have two consumers: the
# admin UI imports them at build time, and docs/clients.md is generated from
# them. Checking here is what makes editing one stanza and forgetting the other
# a build failure rather than a stale document nobody notices for months.
docs-check:
	@scripts/gen-client-docs.py --check

## check: everything CI runs — formatting, vet, tests, typecheck, docs and the UI build
check: fmt-check vet test typecheck docs-check web-build

# tsc does not run Tailwind, so a stylesheet that cannot compile sails through
# a typecheck. Building the UI is the only thing that proves it compiles.
web-build:
	cd web && npm run build

clean:
	rm -rf $(DIST) web/dist $(WEBDIST)/assets $(WEBDIST)/index.html web/node_modules
