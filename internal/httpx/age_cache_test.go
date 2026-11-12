package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Age", "59")
		_, _ = w.Write([]byte(strconv.Itoa(calls)))
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
	if _, _, err := client.Get(context.Background(), "src", "/", nil); err != nil { //nolint:bodyclose
		t.Fatal(err)
	}
	now = now.Add(1100 * time.Millisecond)
	_, second, err := client.Get(context.Background(), "src", "/", nil) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("upstream calls=%d, want 2 after the upstream age expired", calls)
	}
	if string(second) != "2" {
		t.Fatalf("aged response stayed cached: %q", second)
	}
}

func TestGet_ExhaustedAgeIsNotStored(t *testing.T) {
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Age", "60")
		_, _ = w.Write([]byte(strconv.Itoa(calls)))
	}))
	defer ts.Close()

	client := NewLockedClient(Options{Timeout: time.Second, CacheTTL: time.Minute})
	if err := client.RegisterDestination("src", ts.URL); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.Get(context.Background(), "src", "/", nil); err != nil { //nolint:bodyclose
		t.Fatal(err)
	}
	_, second, err := client.Get(context.Background(), "src", "/", nil) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("upstream calls=%d, want 2 for a fully aged response", calls)
	}
	if string(second) != "2" {
		t.Fatalf("fully aged response was stored: %q", second)
	}
}
