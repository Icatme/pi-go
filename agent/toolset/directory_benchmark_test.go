package toolset

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	managed "github.com/Icatme/pi-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Exercise the real directory path with a warmed SDK catalog, without model calls.
func BenchmarkDirectoryCached(b *testing.B) {
	for _, count := range []int{1, 128} {
		b.Run(fmt.Sprintf("tools_%d", count), func(b *testing.B) {
			server := sdk.NewServer(&sdk.Implementation{Name: "directory-benchmark", Version: "1"}, &sdk.ServerOptions{SetCacheable: func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) { c.TTLMs = 60000; c.CacheScope = "private" }})
			for i := range count {
				server.AddTool(&sdk.Tool{Name: fmt.Sprintf("tool_%d", i), InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
					return &sdk.CallToolResult{}, nil
				})
			}
			var lists atomic.Int64
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
					if method == "tools/list" {
						lists.Add(1)
					}
					return next(ctx, method, req)
				}
			})
			host := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
			defer host.Close()
			manager, err := managed.New(managed.Config{Scope: managed.Scope{Identity: "benchmark-account", AuthEpoch: 1}, Servers: []managed.ServerConfig{{Name: "fixture", URL: host.URL, Exposure: managed.Direct}}})
			if err != nil {
				b.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := manager.Close(ctx); err != nil {
					b.Error(err)
				}
			}()
			ts := &Toolset{manager: manager}
			if _, _, _, err := ts.directory(b.Context(), true, "", nil); err != nil {
				b.Fatal(err)
			}
			warm := lists.Load()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				entries, _, _, err := ts.directory(b.Context(), true, "", nil)
				if err != nil || len(entries) != count+3 {
					b.Fatalf("directory: entries=%d err=%v", len(entries), err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(lists.Load()-warm)/float64(b.N), "wire_calls/op")
		})
	}
}
