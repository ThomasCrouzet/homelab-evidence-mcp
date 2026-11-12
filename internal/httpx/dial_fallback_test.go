package httpx

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type campaignDialRecord struct {
	mu      sync.Mutex
	address []string
}

func (r *campaignDialRecord) add(address string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.address = append(r.address, address)
}

func (r *campaignDialRecord) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.address...)
}

func campaignSourceURL(t *testing.T, serverURL, host string) string {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Host = net.JoinHostPort(host, parsed.Port())
	return parsed.String()
}

func TestAllowedCIDRs_FallsBackToNextPermittedAddress(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	realAddress := upstreamURL.Host

	client := NewLockedClient(Options{
		Timeout: time.Second,
		AllowedCIDRs: campaignCIDRs(t,
			"2001:db8::/32",
			"192.0.2.0/24",
		),
	})
	client.lookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("2001:db8::1")},
			{IP: net.ParseIP("192.0.2.10")},
		}, nil
	}
	var dialed campaignDialRecord
	client.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed.add(address)
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if host == "2001:db8::1" {
			return nil, errors.New("network unreachable")
		}
		return (&net.Dialer{}).DialContext(ctx, network, realAddress)
	}
	if err := client.RegisterDestination("src", campaignSourceURL(t, upstream.URL, "source.internal")); err != nil {
		t.Fatal(err)
	}

	resp, body, err := client.Get(context.Background(), "src", "/", nil) //nolint:bodyclose
	if err != nil {
		t.Fatalf("permitted fallback failed: %v (dialed %v)", err, dialed.snapshot())
	}
	if resp.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	got := dialed.snapshot()
	if len(got) != 2 || !strings.HasPrefix(got[0], "[2001:db8::1]:") || !strings.HasPrefix(got[1], "192.0.2.10:") {
		t.Fatalf("dialed=%v, want both permitted candidates in order", got)
	}
}

func TestAllowedCIDRs_NeverDialsOutsideThePolicy(t *testing.T) {
	client := NewLockedClient(Options{
		Timeout: time.Second,
		AllowedCIDRs: campaignCIDRs(t,
			"192.0.2.0/24",
		),
	})
	client.lookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("192.0.2.10")},
			{IP: net.ParseIP("198.51.100.7")},
			{IP: net.ParseIP("192.0.2.11")},
		}, nil
	}
	var dialed campaignDialRecord
	client.dialContext = func(_ context.Context, _ string, address string) (net.Conn, error) {
		dialed.add(address)
		return nil, errors.New("connection refused")
	}
	if err := client.RegisterDestination("src", "http://source.internal:8080"); err != nil {
		t.Fatal(err)
	}

	resp, body, err := client.Get(context.Background(), "src", "/", nil) //nolint:bodyclose
	if err == nil || resp != nil || body != nil {
		t.Fatalf("all failed: resp=%v body=%v err=%v", resp, body, err)
	}
	if strings.Contains(err.Error(), "192.0.2") || strings.Contains(err.Error(), "198.51.100") ||
		strings.Contains(err.Error(), "source.internal") {
		t.Fatalf("error leaked a destination address: %v", err)
	}
	got := dialed.snapshot()
	want := []string{"192.0.2.10:8080", "192.0.2.11:8080"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("dialed=%v, want only permitted candidates %v", got, want)
	}
}

func TestAllowedCIDRs_DeadlineStopsFallback(t *testing.T) {
	client := NewLockedClient(Options{
		Timeout: 5 * time.Second,
		AllowedCIDRs: campaignCIDRs(t,
			"192.0.2.0/24",
		),
	})
	client.lookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("192.0.2.10")},
			{IP: net.ParseIP("192.0.2.11")},
		}, nil
	}
	var calls atomic.Int32
	client.dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := client.RegisterDestination("src", "http://source.internal:8080"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := client.Get(ctx, "src", "/", nil) //nolint:bodyclose
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected deadline error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("fallback ignored the deadline: %s", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("dial calls=%d, want 1 before the deadline", calls.Load())
	}
}

func TestAllowedCIDRs_KeepsTLSHostnameVerification(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	cert, err := campaignTLSCert(upstream)
	if err != nil {
		t.Fatal(err)
	}

	client := NewLockedClient(Options{
		Timeout:      time.Second,
		RootCAs:      campaignCertPool(cert),
		AllowedCIDRs: campaignCIDRs(t, "127.0.0.0/8"),
	})
	client.lookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if err := client.RegisterDestination("src", campaignSourceURL(t, upstream.URL, "name.invalid")); err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Get(context.Background(), "src", "/", nil) //nolint:bodyclose
	if err == nil {
		t.Fatal("TLS accepted a certificate for a different hostname")
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "name.invalid") {
		t.Fatalf("TLS error leaked the destination: %v", err)
	}
}

func campaignCIDRs(t *testing.T, values ...string) []*net.IPNet {
	t.Helper()
	out := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, network)
	}
	return out
}

func campaignTLSCert(server *httptest.Server) (*x509.Certificate, error) {
	return x509.ParseCertificate(server.TLS.Certificates[0].Certificate[0])
}

func campaignCertPool(cert *x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}
