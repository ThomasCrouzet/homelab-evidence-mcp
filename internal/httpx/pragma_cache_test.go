package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestSourceCacheLifetime_PragmaNoCacheOverridesMaxAge(t *testing.T) {
	header := http.Header{}
	header.Set("Cache-Control", "max-age=60")
	header.Set("Pragma", "no-cache")
	if lifetime, storable := sourceCacheLifetime(header, time.Minute); storable || lifetime != 0 {
		t.Fatalf("lifetime=%s storable=%v, want storage bypass", lifetime, storable)
	}
}

func TestSourceCacheLifetime_MaxAgeWithoutBypassKeepsTTLCap(t *testing.T) {
	header := http.Header{}
	header.Set("Cache-Control", "max-age=600")
	if lifetime, storable := sourceCacheLifetime(header, 30*time.Second); !storable || lifetime != 30*time.Second {
		t.Fatalf("lifetime=%s storable=%v, want 30s", lifetime, storable)
	}
}

func TestGet_PragmaNoCacheWithMaxAgeIsNotStored(t *testing.T) {
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Pragma", "no-cache")
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
		t.Fatalf("upstream calls=%d, want 2", calls)
	}
	if string(second) != "2" {
		t.Fatalf("cache reused a Pragma no-cache body: %q", second)
	}
}
