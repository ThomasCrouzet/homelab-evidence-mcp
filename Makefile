.PHONY: build test test-quick race lint lint-docs vet fmt fuzz demo clean coverage

VERSION ?= 0.1.1
BINARY ?= bin/homelab-evidence-mcp
LINT_GOTOOLCHAIN ?= go1.25.14
LDFLAGS := -s -w -X github.com/ThomasCrouzet/homelab-evidence-mcp/internal/version.Version=$(VERSION)

build:
	mkdir -p bin
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/homelab-evidence-mcp

# Run all local tests with race detection.
test: race

race:
	go test ./... -race -count=1

# Run fast tests without race detection.
test-quick:
	go test ./... -count=1

coverage:
	go test ./... -coverprofile=coverage.out -count=1
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
		echo "Install golangci-lint 2.12.2 before you run make lint." >&2; \
		exit 1; \
	fi
	GOTOOLCHAIN=$(LINT_GOTOOLCHAIN) golangci-lint run --timeout=5m ./...

lint-docs:
	npx --yes markdownlint-cli2@0.23.2 '**/*.md'

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
