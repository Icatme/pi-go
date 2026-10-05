package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCatalogCacheOwnsFrozenPagesAndDetachedTools(t *testing.T) {
	server := wireTestServer()
	readOnly := false
	server.AddTool(&sdk.Tool{
		Name: "fixture", Description: "original", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"const":9007199254740993}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"amount":{"const":0.100000000000000000001}}}`),
		Meta:         sdk.Meta{"nested": map[string]any{"number": json.Number("9007199254740993")}},
		Annotations:  &sdk.ToolAnnotations{DestructiveHint: &readOnly},
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{}, nil
	})
	_, c := newManagerFixture(t, server, true)
	first, err := c.Refresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	frozen := c.catalog
	digest := sha256.New()
	for _, page := range frozen.pages {
		fmt.Fprintf(digest, "%d:", len(page.source.raw))
		digest.Write(page.source.raw)
	}
	if first.SourceDigest != hex.EncodeToString(digest.Sum(nil)) {
		t.Fatal("public source digest no longer describes original wire pages")
	}
	mutate := func(tool *sdk.Tool) {
		tool.Description = "changed"
		tool.Meta["nested"].(map[string]any)["number"] = "changed"
		*tool.Annotations.DestructiveHint = true
		tool.InputSchema.(json.RawMessage)[0] = '!'
		tool.OutputSchema.(json.RawMessage)[0] = '!'
	}
	mutate(first.Tools[0])
	external, err := c.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mutate(external.Tools[0])
	tracked, record := c.Track(t.Context())
	second, err := c.Refresh(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if record().Attempts != 0 || c.catalog != frozen || second.Revision != first.Revision || !c.IsCurrent(first) {
		t.Fatalf("SDK cache hit rebuilt or retired frozen catalog: attempts=%d revision=%d", record().Attempts, second.Revision)
	}
	tool := second.Tools[0]
	if tool.Description != "original" || tool.Meta["nested"].(map[string]any)["number"] != json.Number("9007199254740993") || *tool.Annotations.DestructiveHint {
		t.Fatalf("caller mutation escaped detached tool: %+v", tool)
	}
	if !strings.Contains(string(tool.InputSchema.(json.RawMessage)), "9007199254740993") || !strings.Contains(string(tool.OutputSchema.(json.RawMessage)), "0.100000000000000000001") {
		t.Fatal("schema number lexemes were not preserved")
	}
}

func TestCatalogExternalRefreshFencesBeforeResponse(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%t", fail), func(t *testing.T) {
			server := wireTestServer()
			addWireTool(server, "fixture", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{}, nil
			})
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			var lists atomic.Int32
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
					if method != "tools/list" {
						return next(ctx, method, req)
					}
					n := lists.Add(1)
					if n == 2 {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						if fail {
							return nil, errors.New("refresh failed")
						}
					}
					return &sdk.ListToolsResult{Tools: []*sdk.Tool{{Name: "fixture", Description: fmt.Sprint(n), InputSchema: map[string]any{"type": "object"}}}}, nil
				}
			})
			_, c := newManagerFixture(t, server, true)
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			first, err := c.Refresh(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := c.ListTools(t.Context(), nil); done <- err }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("external refresh did not reach server")
			}
			if c.IsCurrent(first) {
				t.Fatal("published catalog stayed current during an external wire refresh")
			}
			releaseOnce.Do(func() { close(release) })
			if err := <-done; (err != nil) != fail {
				t.Fatalf("external refresh failure=%t: %v", fail, err)
			}
			fresh, err := c.Refresh(t.Context())
			if err != nil || fresh.Revision <= first.Revision || !c.IsCurrent(fresh) || c.IsCurrent(first) {
				t.Fatalf("new catalog did not replace external refresh: revision=%d err=%v", fresh.Revision, err)
			}
		})
	}
}

func TestCatalogCachedIdentityRequiresLiveWireBinding(t *testing.T) {
	server := wireTestServer()
	addWireTool(server, "fixture", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{}, nil
	})
	_, c := newManagerFixture(t, server, true)
	if _, err := c.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	frozen := c.catalog
	c.observer.Forget(frozen.pages[0].source.result)
	if _, err := c.Refresh(t.Context()); !errors.Is(err, ErrRawSnapshotUnavailable) {
		t.Fatalf("frozen page bypassed wire eviction: %v", err)
	}
	if c.catalog != frozen {
		t.Fatal("failed refresh replaced the frozen catalog")
	}
}

func TestCatalogFrozenIdentityDoesNotCrossScope(t *testing.T) {
	for _, scope := range []Scope{{Identity: "bob", AuthEpoch: 1}, {Identity: "alice", AuthEpoch: 2}} {
		t.Run(fmt.Sprint(scope), func(t *testing.T) {
			server := wireTestServer()
			addWireTool(server, "fixture", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{}, nil
			})
			m, c := newManagerFixture(t, server, true)
			first, err := c.Refresh(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := m.SetScope(scope); err != nil {
				t.Fatal(err)
			}
			next, err := m.Connect(t.Context(), "fixture")
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := next.Refresh(t.Context())
			if err != nil || c.IsCurrent(first) || next.IsCurrent(first) || !next.IsCurrent(fresh) || c.catalog == next.catalog || c.catalog.pages[0].source == next.catalog.pages[0].source {
				t.Fatalf("frozen catalog crossed identity/auth epoch: %v", err)
			}
		})
	}
}

func TestCatalogConcurrentDetachedRefresh(t *testing.T) {
	server := wireTestServer()
	addWireTool(server, "fixture", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{}, nil
	})
	_, c := newManagerFixture(t, server, true)
	if _, err := c.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for worker := range 8 {
		group.Go(func() {
			for range 12 {
				catalog, err := c.Refresh(t.Context())
				if err != nil {
					t.Error(err)
					return
				}
				catalog.Tools[0].Name = fmt.Sprint(worker)
				catalog.Tools[0].InputSchema.(json.RawMessage)[0] = '!'
				page, err := c.ListTools(t.Context(), nil)
				if err != nil {
					t.Error(err)
					return
				}
				page.Tools[0].Name = fmt.Sprint(worker)
				if !c.IsCurrent(catalog) {
					t.Error("cached reads retired the catalog")
				}
			}
		})
	}
	group.Wait()
}
