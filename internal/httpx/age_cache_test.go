package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestSourceCacheLifetime_SubtractsAge(t *testing.T) {
	header := http.Header{}
	header.Set("Cache-Control", "max-age=60")
	header.Set("Age", "59")
	lifetime, storable := sourceCacheLifetime(header, time.Minute)
	if !storable || lifetime != time.Second {
		t.Fatalf("lifetime=%s storable=%v, want 1s", lifetime, storable)
	}
}

func TestSourceCacheLifetime_RejectsUnusableAge(t *testing.T) {
	cases := []string{"-1", "1.5", "abc", "5, 6", "99999999999999999999"}
	for _, age := range cases {
		t.Run(age, func(t *testing.T) {
			header := http.Header{}
			header.Set("Cache-Control", "max-age=60")
			header.Set("Age", age)
			if lifetime, storable := sourceCacheLifetime(header, time.Minute); storable || lifetime != 0 {
				t.Fatalf("age=%q lifetime=%s storable=%v, want storage bypass", age, lifetime, storable)
			}
		})
	}
	header := http.Header{}
	header.Set("Cache-Control", "max-age=60")
	header.Set("Age", "60")
	if lifetime, storable := sourceCacheLifetime(header, time.Minute); storable || lifetime != 0 {
		t.Fatalf("exhausted age lifetime=%s storable=%v, want storage bypass", lifetime, storable)
	}
	header.Set("Age", "0")
	if lifetime, storable := sourceCacheLifetime(header, time.Minute); !storable || lifetime != time.Minute {
		t.Fatalf("age zero lifetime=%s storable=%v, want 60s", lifetime, storable)
	}
}

func TestSourceCacheLifetime_NoAgeKeepsCurrentBehavior(t *testing.T) {
	header := http.Header{}
	header.Set("Cache-Control", "max-age=120")
	if lifetime, storable := sourceCacheLifetime(header, time.Minute); !storable || lifetime != time.Minute {
		t.Fatalf("lifetime=%s storable=%v, want operator cap 60s", lifetime, storable)
	}
	if lifetime, storable := sourceCacheLifetime(http.Header{}, time.Minute); !storable || lifetime != time.Minute {
		t.Fatalf("lifetime=%s storable=%v, want operator TTL 60s", lifetime, storable)
	}
}

func TestGet_AgedResponseExpiresWithInjectedClock(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Age", "59")
		_, _ = w.Write([]byte(strconv.Itoa(int(n))))
	}))
	defer ts.Close()

	client := NewLockedClient(Options{
		Timeout:  time.Second,
		CacheTTL: time.Minute,
		Now:      func() time.Time { return now },
	})
	if err := client.RegisterDestination("src", ts.URL); err != nil {
		t.Fatal(err)
	}
	resp, _, err := client.Get(context.Background(), "src", "/", nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(1100 * time.Millisecond)
	resp, second, err := client.Get(context.Background(), "src", "/", nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want 2 after the upstream age expired", calls.Load())
	}
	if string(second) != "2" {
		t.Fatalf("aged response stayed cached: %q", second)
	}
}

func TestGet_ExhaustedAgeIsNotStored(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Age", "60")
		_, _ = w.Write([]byte(strconv.Itoa(int(n))))
	}))
	defer ts.Close()

	client := NewLockedClient(Options{Timeout: time.Second, CacheTTL: time.Minute})
	if err := client.RegisterDestination("src", ts.URL); err != nil {
		t.Fatal(err)
	}
	resp, _, err := client.Get(context.Background(), "src", "/", nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	resp, second, err := client.Get(context.Background(), "src", "/", nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want 2 for a fully aged response", calls.Load())
	}
	if string(second) != "2" {
		t.Fatalf("fully aged response was stored: %q", second)
	}
}
