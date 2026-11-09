.PHONY: build build-matrix print-version test test-e2e test-quick race lint lint-docs lint-private vet fmt fuzz demo clean coverage

# The source file is authoritative. Override VERSION only for a local build.
VERSION ?= $(shell tr -d '[:space:]' < internal/version/version.txt)
BINARY ?= bin/homelab-evidence-mcp
DIST ?= dist
TARGETS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
LINT_GOTOOLCHAIN ?= go1.26.10
LDFLAGS := -s -w -X github.com/ThomasCrouzet/homelab-evidence-mcp/internal/version.Version=$(VERSION)

build:
	mkdir -p bin
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/homelab-evidence-mcp

print-version:
	@printf '%s\n' '$(VERSION)'

# Build every release target from the same version and flags.
# The recipe uses POSIX sh because GNU Make defaults to /bin/sh.
build-matrix:
	mkdir -p $(DIST)
	set -eu; \
	for pair in $(TARGETS); do \
		os="$${pair%/*}"; \
		arch="$${pair#*/}"; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		out="$(DIST)/homelab-evidence-mcp_$${os}_$${arch}$${ext}"; \
		echo "Build output: $$out"; \
		GOOS="$$os" GOARCH="$$arch" CGO_ENABLED=0 go build -trimpath \
			-ldflags="$(LDFLAGS)" -o "$$out" ./cmd/homelab-evidence-mcp; \
	done
	ls -la $(DIST)

# Run isolated tests with race detection. Then run executable E2E tests.
test: race test-e2e

race:
	go test ./... -race -count=1 -skip '^TestStdio'

test-e2e:
	./scripts/test-e2e.sh

# Run fast tests without race detection.
test-quick:
	go test ./... -count=1 -skip '^TestStdio'

coverage:
	go test ./... -coverprofile=coverage.out -count=1 -skip '^TestStdio'
	go tool cover -func=coverage.out | tail -5

vet:
	go vet ./...

fmt:
	gofmt -w .

lint: vet
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "Files to format:"; echo "$$unformatted"; exit 1; \
	fi
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "Install golangci-lint 2.15.0 before you run make lint." >&2; \
		exit 1; \
	fi
	GOTOOLCHAIN=$(LINT_GOTOOLCHAIN) golangci-lint run --timeout=5m ./...

lint-docs:
	npx --yes markdownlint-cli2@0.23.2 '**/*.md'

lint-private:
	./scripts/check-private-addresses.sh --self-test
	./scripts/check-private-addresses.sh

fuzz:
	go test ./internal/config -fuzz=FuzzParse -fuzztime=10s
	go test ./internal/redaction -fuzz=FuzzApply -fuzztime=10s
	go test ./internal/adapters/loki -fuzz=FuzzParseNano -fuzztime=5s

demo:
	go run ./demo

clean:
	rm -f $(BINARY) coverage.out coverage.html
	rm -f dist/homelab-evidence-mcp_linux_amd64
	rm -f dist/homelab-evidence-mcp_linux_arm64
	rm -f dist/homelab-evidence-mcp_darwin_amd64
	rm -f dist/homelab-evidence-mcp_darwin_arm64
	rm -f dist/homelab-evidence-mcp_windows_amd64.exe
	rm -f dist/SHA256SUMS dist/LICENSE.txt dist/THIRD_PARTY_LICENSES.txt
	rmdir bin dist 2>/dev/null || true
