package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Keep this benchmark on the public path so it also runs against the previous
// implementation. wire_calls/op verifies that the measured path is an SDK hit.
func BenchmarkCatalogRefreshCached(b *testing.B) {
	for _, size := range []int{4 << 10, (1 << 20) - (2 << 10)} {
		b.Run(fmt.Sprintf("schema_%d", size), func(b *testing.B) {
			server := wireTestServer()
			server.AddTool(&sdk.Tool{Name: "fixture", InputSchema: json.RawMessage(`{"type":"object","description":"` + strings.Repeat("x", size) + `"}`)}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{}, nil
			})
			var lists atomic.Int64
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
					if method == "tools/list" {
						lists.Add(1)
					}
					return next(ctx, method, req)
				}
			})
			httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
			defer httpServer.Close()
			manager, err := New(Config{Scope: Scope{Identity: "benchmark"}, Servers: []ServerConfig{{Name: "fixture", URL: httpServer.URL}}})
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
			connection, err := manager.Connect(b.Context(), "fixture")
			if err != nil {
				b.Fatal(err)
			}
			if _, err := connection.Refresh(b.Context()); err != nil {
				b.Fatal(err)
			}
			warm := lists.Load()
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for range b.N {
				catalog, err := connection.Refresh(b.Context())
				if err != nil || len(catalog.Tools) != 1 {
					b.Fatalf("cached refresh: tools=%d err=%v", len(catalog.Tools), err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(lists.Load()-warm)/float64(b.N), "wire_calls/op")
		})
	}
}
