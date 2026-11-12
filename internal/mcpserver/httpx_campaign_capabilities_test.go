package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type httpxCampaignCountingContext struct {
	context.Context
	calls   int
	mu      sync.Mutex
	reached chan struct{}
}

func (c *httpxCampaignCountingContext) Done() <-chan struct{} {
	c.mu.Lock()
	c.calls++
	if c.calls == 1 {
		close(c.reached)
	}
	c.mu.Unlock()
	return c.Context.Done()
}

func TestEvidenceCapabilitiesIncludesCoalescedSourceRequests(t *testing.T) {
	app := newTestApp(t, false)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	if err := app.HTTP.RegisterDestination("httpx-campaign", upstream.URL); err != nil {
		t.Fatal(err)
	}

	leaderDone := make(chan error, 1)
	go func() {
		resp, _, err := app.HTTP.Get(context.Background(), "httpx-campaign", "/shared", nil)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		leaderDone <- err
	}()
	<-entered

	waiterCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counting := &httpxCampaignCountingContext{Context: waiterCtx, reached: make(chan struct{})}
	waiterDone := make(chan error, 1)
	go func() {
		resp, _, err := app.HTTP.Get(counting, "httpx-campaign", "/shared", nil)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		waiterDone <- err
	}()
	<-counting.reached
	close(release)

	if err := <-leaderDone; err != nil {
		t.Fatalf("leader: %v", err)
	}
	if err := <-waiterDone; err != nil {
		t.Fatalf("waiter: %v", err)
	}

	server := app.Server()
	t1, t2 := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = ss.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "httpx-campaign", Version: "1"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "evidence_capabilities", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("evidence_capabilities: %v %+v", err, result)
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type %T", result.Content[0])
	}
	var output struct {
		SourceCache struct {
			Coalesced uint64 `json:"coalesced"`
		} `json:"source_cache"`
	}
	if err := json.Unmarshal([]byte(content.Text), &output); err != nil {
		t.Fatalf("decode capabilities: %v\n%s", err, content.Text)
	}
	if output.SourceCache.Coalesced != 1 {
		t.Fatalf("capabilities coalesced=%d, want 1", output.SourceCache.Coalesced)
	}
}
