package mcpserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// runtimeGatusTLSFixture starts one Gatus TLS endpoint with a certificate that
// is unrelated to the httptest standard certificate.
func runtimeGatusTLSFixture(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(20261112),
		Subject:               pkix.Name{CommonName: "runtime-campaign"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "api", "key": "api",
			"results": []map[string]any{{
				"success": true, "status": 200, "timestamp": time.Now().UTC().Format(time.RFC3339),
			}},
		}})
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "runtime-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return server, path
}

// This test detects the loss of global tls.ca_file trust when a source adds an
// allowed_cidrs policy. It also checks that a per-source ca_file still
// replaces the global trust for that source only, and that an address policy
// alone does not trust an unknown CA.
func TestStdioGlobalCAInheritanceUnderCIDRPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	globalServer, globalCA := runtimeGatusTLSFixture(t)
	overrideServer, overrideCA := runtimeGatusTLSFixture(t)
	binary := stdioTestBinary(t, ctx)

	type expectation struct {
		status   string
		complete bool
	}

	withGlobal := fmt.Sprintf(`version: 1
limits:
  per_source_timeout: 2s
  total_timeout: 5s
tls:
  ca_file: %s
sources:
  plain:
    kind: gatus
    base_url: %s
  cidr:
    kind: gatus
    base_url: %s
    allowed_cidrs: ["127.0.0.0/8"]
  override-mismatch:
    kind: gatus
    base_url: %s
    ca_file: %s
  override-match:
    kind: gatus
    base_url: %s
    ca_file: %s
services:
  - id: plain
    sources:
      gatus: { source: plain, endpoint_key: api }
  - id: cidr
    sources:
      gatus: { source: cidr, endpoint_key: api }
  - id: override-mismatch
    sources:
      gatus: { source: override-mismatch, endpoint_key: api }
  - id: override-match
    sources:
      gatus: { source: override-match, endpoint_key: api }
`, globalCA, globalServer.URL, globalServer.URL, globalServer.URL, overrideCA, overrideServer.URL, overrideCA)
	withGlobalPath := runtimeWriteConfig(t, withGlobal)
	session := runtimeConnect(t, ctx, binary, withGlobalPath)
	want := map[string]expectation{
		// The global CA alone must trust the internal certificate.
		"plain": {status: "ok", complete: true},
		// A CIDR policy must keep the global CA trust.
		"cidr": {status: "ok", complete: true},
		// An explicit source CA replaces the global trust for that source.
		"override-mismatch": {status: "error", complete: false},
		"override-match":    {status: "ok", complete: true},
	}
	for serviceID, expected := range want {
		raw := executableE2ECall(t, ctx, session, "service_status", map[string]any{"service_id": serviceID})
		outcome := runtimeFindSource(t, runtimeSources(t, raw), serviceID, "gatus")
		if outcome.Status != expected.status || outcome.Complete != expected.complete {
			t.Fatalf("service %s: status=%q complete=%v, want status=%q complete=%v: %s",
				serviceID, outcome.Status, outcome.Complete, expected.status, expected.complete, raw)
		}
		if expected.status != "ok" && outcome.Reason != "upstream_error" {
			t.Fatalf("service %s: reason=%q, want upstream_error: %s", serviceID, outcome.Reason, raw)
		}
	}

	// The CIDR policy alone must not add certificate trust.
	policyOnly := fmt.Sprintf(`version: 1
limits:
  per_source_timeout: 2s
  total_timeout: 5s
sources:
  cidr-only:
    kind: gatus
    base_url: %s
    allowed_cidrs: ["127.0.0.0/8"]
services:
  - id: cidr-only
    sources:
      gatus: { source: cidr-only, endpoint_key: api }
`, globalServer.URL)
	policyOnlySession := runtimeConnect(t, ctx, binary, runtimeWriteConfig(t, policyOnly))
	raw := executableE2ECall(t, ctx, policyOnlySession, "service_status", map[string]any{"service_id": "cidr-only"})
	outcome := runtimeFindSource(t, runtimeSources(t, raw), "cidr-only", "gatus")
	if outcome.Status != "error" || outcome.Complete {
		t.Fatalf("address policy alone trusted an unknown CA: status=%q complete=%v: %s",
			outcome.Status, outcome.Complete, raw)
	}
}
