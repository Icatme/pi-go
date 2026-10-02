package toolset

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/codemode"
	managed "github.com/Icatme/pi-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResolveRejectsColdConfigurationChangeDuringConnect(t *testing.T) {
	sandbox, err := codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sandbox.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	for _, change := range []string{"server_hidden", "tool_exposure", "policy_ABA", "reconnect"} {
		t.Run(change, func(t *testing.T) {
			server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
			server.AddTool(&sdk.Tool{Name: "public", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			})
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			var blocked atomic.Bool
			var privateRequests atomic.Int32
			handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Namespace") == "private_store" {
					privateRequests.Add(1)
				}
				if r.Header.Get("X-Namespace") == "a" && r.Method == http.MethodPost && blocked.CompareAndSwap(false, true) {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(httpServer.Close)
			manager, err := managed.New(managed.Config{Scope: managed.Scope{Identity: "account", AuthEpoch: 1}, Servers: []managed.ServerConfig{
				{Name: "a", URL: httpServer.URL, Exposure: managed.Direct, Headers: http.Header{"X-Namespace": []string{"a"}}},
				{Name: "private_store", URL: httpServer.URL, Exposure: managed.Codemode, Headers: http.Header{"X-Namespace": []string{"private_store"}}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := manager.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			set, err := New(manager, sandbox, Options{})
			if err != nil {
				t.Fatal(err)
			}
			type resolved struct {
				tools []agent.ToolDefinition
				err   error
			}
			done := make(chan resolved, 1)
			go func() {
				tools, err := set.Resolve(t.Context(), agent.AgentSnapshot{})
				done <- resolved{tools, err}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("direct connection did not park after capturing resource declarations")
			}
			switch change {
			case "server_hidden", "policy_ABA":
				if err := manager.SetExposure("private_store", managed.Hidden, nil); err != nil {
					t.Fatal(err)
				}
				if change == "policy_ABA" {
					if err := manager.SetExposure("private_store", managed.Codemode, nil); err != nil {
						t.Fatal(err)
					}
				}
			case "tool_exposure":
				if err := manager.SetExposure("private_store", managed.Codemode, []managed.ToolRule{{Pattern: "public", Exposure: managed.Hidden}}); err != nil {
					t.Fatal(err)
				}
			case "reconnect":
				if _, err := manager.Reconnect(t.Context(), "private_store"); err != nil {
					t.Fatal(err)
				}
			}
			releaseOnce.Do(func() { close(release) })
			var result resolved
			select {
			case result = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("resolver did not finish")
			}
			var failure *agent.ToolExecutionError
			if !errors.Is(result.err, managed.ErrStale) || !errors.As(result.err, &failure) || failure.Execution.Remote != agent.ToolRemoteNotDispatched || len(result.tools) != 0 {
				t.Fatalf("changed cold configuration published a directory: change=%s tools=%s err=%v", change, toolNames(result.tools), result.err)
			}
			if change != "reconnect" && privateRequests.Load() != 0 {
				t.Fatalf("cold policy change triggered implicit connection/retry: requests=%d", privateRequests.Load())
			}
			// A new explicit resolution uses the current policy; the failed
			// resolution never retries or publishes an older declaration.
			tools, err := set.Resolve(t.Context(), agent.AgentSnapshot{})
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource"} {
				definition := findTool(t, tools, name)
				schema, err := json.Marshal(definition.Parameters)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(schema), "private_store") != (change != "server_hidden") {
					t.Fatalf("fresh resource schema did not honor visibility: change=%s schema=%s", change, schema)
				}
			}
		})
	}
}
