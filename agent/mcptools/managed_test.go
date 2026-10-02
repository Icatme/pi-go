package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	managed "github.com/Icatme/pi-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func managedFixture(t *testing.T, server *sdk.Server, limits managed.Limits) *managed.Connection {
	t.Helper()
	hs := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(hs.Close)
	m, err := managed.New(managed.Config{Scope: managed.Scope{Identity: "test"}, Limits: limits, Servers: []managed.ServerConfig{{Name: "fixture", URL: hs.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	c, err := m.Connect(t.Context(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestManagedSchemaCanonicalizationAndAttempts(t *testing.T) {
	var calls atomic.Int32
	s := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	s.AddTool(&sdk.Tool{Name: "write", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "done"}}}, nil
	})
	c := managedFixture(t, s, managed.Limits{})
	tools, err := Discover(t.Context(), c, Options{Names: []string{"write"}})
	if err != nil {
		t.Fatal(err)
	}
	out := agent.RunToolCall(t.Context(), agent.ToolCall{Name: "write", ParsedArgs: map[string]any{"value": "ok"}}, agent.RunToolCallOptions{Tools: tools})
	if out.Err != nil || calls.Load() != 1 || out.Execution.Remote != agent.ToolRemoteCompleteReported || len(out.Execution.Attempts) != 1 || out.Execution.Attempts[0].Number != 1 {
		t.Fatalf("out=%+v calls=%d", out, calls.Load())
	}
}

func TestManagedRejectedRawResultKeepsCompletion(t *testing.T) {
	var calls atomic.Int32
	s := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	s.AddTool(&sdk.Tool{Name: "write", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(make([]byte, 4096))}}}, nil
	})
	c := managedFixture(t, s, managed.Limits{MaxResponseBytes: 2048})
	tools, err := Discover(t.Context(), c, Options{Names: []string{"write"}})
	if err != nil {
		t.Fatal(err)
	}
	out := agent.RunToolCall(t.Context(), agent.ToolCall{Name: "write"}, agent.RunToolCallOptions{Tools: tools})
	var resultErr *ResultError
	if !errors.As(out.Err, &resultErr) || calls.Load() != 1 || out.Failure.Code != agent.ToolFailureResultRejected || out.Execution.Remote != agent.ToolRemoteCompleteReported || len(out.Execution.Attempts) != 1 {
		t.Fatalf("out=%+v calls=%d", out, calls.Load())
	}
}

func TestDiscoverRejectsTypedNilSession(t *testing.T) {
	var session *sdk.ClientSession
	if _, err := Discover(t.Context(), session, Options{Names: []string{"leaf"}}); err == nil {
		t.Fatal("typed nil session accepted")
	}
}

func TestManagedPermissionRecheckedAtPhysicalDispatch(t *testing.T) {
	var calls, checks atomic.Int32
	private := errors.New("PRIVATE revoked authorization detail")
	s := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	s.AddTool(&sdk.Tool{Name: "write", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		return &sdk.CallToolResult{}, nil
	})
	c := managedFixture(t, s, managed.Limits{})
	tools, err := Discover(t.Context(), c, Options{Names: []string{"write"}})
	if err != nil {
		t.Fatal(err)
	}
	out := agent.RunToolCall(t.Context(), agent.ToolCall{Name: "write"}, agent.RunToolCallOptions{Tools: tools, CheckToolPermission: func(context.Context, agent.BeforeToolCallContext) error {
		if checks.Add(1) >= 3 {
			return private
		}
		return nil
	}})
	if !errors.Is(out.Err, private) || strings.Contains(out.Err.Error(), "PRIVATE") || calls.Load() != 0 || checks.Load() != 3 || out.Execution.Remote != agent.ToolRemoteNotDispatched || len(out.Execution.Attempts) != 0 || out.Failure.Code != agent.ToolFailurePolicyDenied {
		t.Fatalf("out=%+v calls=%d checks=%d", out, calls.Load(), checks.Load())
	}
}

func TestRawSchemaCannotRoundAuthorizationConstraints(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","properties":{"id":{"type":"integer","minimum":9007199254740993}}}`,
		`{"type":"object","properties":{"id":{"enum":[9007199254740993]}}}`,
		`{"type":"object","properties":{"amount":{"type":"number","minimum":0.10000000000000000001}}}`,
		`{"type":"object","properties":{"amount":{"type":"number","minimum":0e-999999999}}}`,
	} {
		if _, _, err := schema(json.RawMessage(raw), 64<<10, true); err == nil {
			t.Fatal("rounded raw schema accepted", raw)
		}
	}
	if _, _, err := schema(json.RawMessage(`{"type":"object","properties":{"amount":{"type":"number","minimum":0.1,"maximum":19.99}}}`), 64<<10, true); err != nil {
		t.Fatal("ordinary decimals rejected", err)
	}
}
