.PHONY: build test test-quick race lint vet fmt fuzz demo clean coverage

VERSION ?= 0.1.0
BINARY ?= bin/homelab-evidence-mcp
LDFLAGS := -s -w -X github.com/ThomasCrouzet/homelab-evidence-mcp/internal/version.Version=$(VERSION)

build:
	mkdir -p bin
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/homelab-evidence-mcp

# Full local suite with race detection.
test: race

race:
	go test ./... -race -count=1

# Fast tests without race detection.
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
		echo "Unformatted files:"; echo "$$unformatted"; exit 1; \
	fi
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --timeout=5m ./...; \
	else \
		echo "golangci-lint not found; limited to vet and gofmt"; \
	fi

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
