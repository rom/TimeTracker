# TimeTracker Makefile
#
# The build is deliberately plain: no code generation, no Node.js, no cgo.
# `make build` is essentially `go build`, which is what makes the single-binary
# promise in ASR-003 hold. See docs/adr/0003-pure-go-sqlite.md for why cgo is
# banned, and docs/adr/0009-embedded-assets-and-migrations.md for why there is no
# asset pipeline.

BINARY      := timetracker
BIN_DIR     := bin
DIST_DIR    := dist
PKG         := ./...
MAIN        := ./cmd/timetracker

# Version metadata is stamped into the binary rather than hard-coded, so
# `timetracker version` and the health endpoint always report the truth.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
# SOURCE_DATE_EPOCH support keeps builds reproducible when it is set.
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildDate=$(BUILD_DATE)

# CGO_ENABLED=0 is not an optimisation, it is a requirement: it is what allows
# cross-compilation to every target from any host (ASR-002).
export CGO_ENABLED := 0

# Windows needs the .exe suffix or the produced file is not executable.
ifeq ($(OS),Windows_NT)
	BINARY_EXT := .exe
else
	BINARY_EXT :=
endif

.DEFAULT_GOAL := build

## ---------------------------------------------------------------- build ----

.PHONY: build
build: ## Build the binary for the host platform into bin/
	@mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY)$(BINARY_EXT) $(MAIN)
	@echo "built $(BIN_DIR)/$(BINARY)$(BINARY_EXT)  ($(VERSION), $(COMMIT))"

.PHONY: dev
dev: ## Build with the 'dev' tag: assets load from disk, templates re-parse per request
	@mkdir -p $(BIN_DIR)
	go build -tags dev -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY)$(BINARY_EXT) $(MAIN)

.PHONY: run
run: build ## Build and run in local single-user mode
	./$(BIN_DIR)/$(BINARY)$(BINARY_EXT)

# The platform matrix from ASR-002. Every one of these builds from any host
# precisely because nothing in the tree uses cgo.
PLATFORMS := \
	darwin/amd64 darwin/arm64 \
	linux/amd64  linux/arm64 \
	windows/amd64 windows/arm64

.PHONY: build-all
build-all: ## Cross-compile every supported OS/architecture into dist/
	@mkdir -p $(DIST_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		out="$(DIST_DIR)/$(BINARY)_$${os}_$${arch}$$ext"; \
		echo "  $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o "$$out" $(MAIN) || exit 1; \
	done
	@echo "cross-compiled into $(DIST_DIR)/"

.PHONY: checksums
checksums: build-all ## Produce SHA-256 checksums for the release artefacts
	@cd $(DIST_DIR) && sha256sum * > SHA256SUMS 2>/dev/null || shasum -a 256 * > SHA256SUMS
	@echo "wrote $(DIST_DIR)/SHA256SUMS"

## ----------------------------------------------------------------- test ----

# The race detector needs cgo, so it is enabled for tests only. The shipped
# binary is still built with CGO_ENABLED=0 - a test-time tool has no bearing on
# what the release artefact links against.
.PHONY: test
test: ## Run the full test suite with the race detector
	@if go env CC >/dev/null 2>&1 && command -v "$$(go env CC)" >/dev/null 2>&1; then \
		CGO_ENABLED=1 go test -race -count=1 $(PKG); \
	else \
		echo "no C compiler found: running tests without the race detector"; \
		go test -count=1 $(PKG); \
	fi

.PHONY: test-norace
test-norace: ## Run the tests without the race detector (no C toolchain needed)
	go test -count=1 $(PKG)

.PHONY: test-short
test-short: ## Fast inner-loop tests (skips the store-heavy cases)
	go test -short -count=1 $(PKG)

.PHONY: test-perf
test-perf: ## Performance suite for the ASR-012 budgets (slow)
	# -v because the measurements are the output. Without it `go test` hides the
	# log lines of a passing test, and a run that says nothing but "ok" tells you
	# the budget was met and not by how much - which is the number that shows a
	# regression coming before it arrives.
	go test -tags perf -run TestPerf -count=1 -timeout 20m -v $(PKG)

# Only the packages that have tests. Passing ./... to -coverprofile asks the
# toolchain to merge coverage for packages with no test binary, which needs the
# `covdata` tool - absent from some toolchain installations, and the failure
# message ("no such tool") gives no hint of the cause.
# Derived from which directories actually contain tests, rather than a hand-kept
# exclusion list that goes stale the moment a package gains or loses its first
# test.
TESTED_PKGS = $(shell go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)

.PHONY: coverage
coverage: ## Produce a coverage profile and an HTML report
	go test -count=1 -coverprofile=coverage.out -covermode=count $(TESTED_PKGS)
	go tool cover -html=coverage.out -o coverage.html
	@echo
	@go tool cover -func=coverage.out | tail -1
	@echo "HTML report: coverage.html"

# Coverage floors for the packages where a gap actually matters.
#
# Not a global percentage: chasing one produces tests for trivial accessors
# while an authorisation branch goes unexercised. These are floors that must not
# fall, not targets to optimise, and they are set just below the current figures
# so that a regression fails the build while ordinary work does not.
#
# Deliberately absent: internal/web, whose coverage is dominated by template
# rendering that the HTTP tests exercise without the counter noticing.
#
# internal/config used to be absent too, on the grounds that it is mostly flag
# declarations. It is not: it decides what the process listens on and whether it
# will serve a login form in clear, and the tests that raised it from 14% to 92%
# found two addresses that were treated as loopback and are not.
COVERAGE_FLOORS = domain:84 service:57 store:48 export:85 blob:70 auth:80 \
                  archive:75 preview:80 config:88

.PHONY: coverage-check
coverage-check: ## Fail if a critical package drops below its coverage floor
	@failed=0; \
	for spec in $(COVERAGE_FLOORS); do \
		pkg=$${spec%%:*}; floor=$${spec##*:}; \
		actual=$$(go test -cover ./internal/$$pkg/ 2>/dev/null \
			| sed -n 's/.*coverage: \([0-9]*\)\.[0-9]*%.*/\1/p'); \
		if [ -z "$$actual" ]; then \
			printf '  %-10s no coverage reported\n' "$$pkg"; failed=1; continue; \
		fi; \
		printf '  %-10s %3s%%  (floor %s%%)  ' "$$pkg" "$$actual" "$$floor"; \
		if [ "$$actual" -lt "$$floor" ]; then echo 'BELOW FLOOR'; failed=1; else echo 'ok'; fi; \
	done; \
	if [ "$$failed" -ne 0 ]; then \
		echo; \
		echo 'Coverage fell below a floor. Add tests, or lower the floor deliberately'; \
		echo 'with a note saying why.'; \
	fi; \
	exit $$failed

.PHONY: bench
bench: ## Run benchmarks
	go test -run '^$$' -bench . -benchmem $(PKG)

## ------------------------------------------------------------- analysis ----

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -s -w .

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt-clean (ASR-011)
	@files=$$(gofmt -s -l .); \
	if [ -n "$$files" ]; then echo "not gofmt-clean:"; echo "$$files"; exit 1; fi
	@echo "gofmt: clean"

.PHONY: vet
vet: ## Run go vet
	go vet $(PKG)

.PHONY: lint
lint: ## Run golangci-lint if it is installed
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed - skipping (go vet still runs via 'make vet')"; \
	fi

.PHONY: vulncheck
vulncheck: ## Scan dependencies for known vulnerabilities
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck $(PKG); \
	else \
		echo "govulncheck not installed: go install golang.org/x/vuln/cmd/govulncheck@latest"; \
	fi

# A compile check for every supported platform, without producing artefacts.
#
# This exists because a symbol defined only in a _linux.go file compiled fine on
# the development machine and broke the macOS and Windows builds - a class of
# regression that only a cross-compile can catch, and that the test suite never
# will.
.PHONY: build-check
build-check: ## Compile for every supported OS/arch without writing artefacts
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		printf '  %-16s' "$$os/$$arch"; \
		if GOOS=$$os GOARCH=$$arch go build -o /dev/null ./... 2>/tmp/tt-build-check.err; then \
			echo 'ok'; \
		else \
			echo 'FAILED'; cat /tmp/tt-build-check.err; exit 1; \
		fi; \
	done

.PHONY: check
check: fmt-check vet lint build-check test coverage-check ## Everything CI runs

## --------------------------------------------------------------- tidy up ----

.PHONY: tidy
tidy: ## Tidy and verify the module graph
	go mod tidy
	go mod verify

.PHONY: clean
clean: ## Remove build artefacts
	rm -rf $(BIN_DIR) $(DIST_DIR) coverage.out coverage.html

.PHONY: help
help: ## List the available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
