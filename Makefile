.PHONY: build test test-e2e test-quick race lint lint-docs lint-private vet fmt fuzz demo clean coverage

VERSION ?= 0.1.3
BINARY ?= bin/homelab-evidence-mcp
LINT_GOTOOLCHAIN ?= go1.26.10
LDFLAGS := -s -w -X github.com/ThomasCrouzet/homelab-evidence-mcp/internal/version.Version=$(VERSION)

build:
	mkdir -p bin
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/homelab-evidence-mcp

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
