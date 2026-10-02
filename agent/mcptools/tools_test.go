package mcptools

import (
	"bytes"
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
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var objectSchema = map[string]any{"type": "object"}

func session(t *testing.T, server *mcp.Server, httpTransport bool, version string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	var transport mcp.Transport
	if httpTransport {
		h := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
		t.Cleanup(h.Close)
		transport = &mcp.StreamableClientTransport{Endpoint: h.URL}
	} else {
		local, remote := mcp.NewInMemoryTransports()
		ss, err := server.Connect(t.Context(), remote, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ss.Close() })
		transport = local
	}
	cs, err := client.Connect(t.Context(), transport, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func add(server *mcp.Server, name string, handler mcp.ToolHandler) {
	server.AddTool(&mcp.Tool{Name: name, InputSchema: objectSchema}, handler)
}

func TestDiscoveryAndDirectCall(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
	var calls atomic.Int32
	for _, name := range []string{"one", "two"} {
		add(s, name, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello"}, &mcp.ImageContent{Data: []byte{1, 2}, MIMEType: "image/png"}}, StructuredContent: []any{"value", float64(2)}, IsError: true}, nil
		})
	}
	cs := session(t, s, true, "2025-11-25")
	tools, err := Discover(t.Context(), cs, Options{Names: []string{"two", "one"}})
	if err != nil || len(tools) != 2 || tools[0].Name != "two" {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
	out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "call", Name: "two", Arguments: json.RawMessage(`{}`)}, agent.RunToolCallOptions{Tools: tools})
	if !out.IsError || !out.Result.IsError || string(out.Result.StructuredContent) != `["value",2]` || len(out.Result.Content) != 2 || out.Result.Content[1].Data != "AQI=" || calls.Load() != 1 {
		t.Fatalf("outcome=%+v calls=%d", out, calls.Load())
	}
	// Discovery/execution never closes the caller's session.
	if _, err := cs.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitSelectionAndBounds(t *testing.T) {
	if got, err := Discover(t.Context(), nil, Options{}); err != nil || len(got) != 0 {
		t.Fatalf("empty selection: %v %v", got, err)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"one", "two"} {
		add(s, name, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	}
	cs := session(t, s, false, "2025-11-25")
	for _, options := range []Options{{Names: []string{"missing"}}, {Names: []string{"one", "one"}}, {Names: []string{"one"}, MaxPages: 1}, {Names: []string{"one"}, MaxTools: 1}, {Names: []string{"one"}, MaxSchemaBytes: 1}, {MaxPages: -1}} {
		if _, err := Discover(t.Context(), cs, options); err == nil {
			t.Fatalf("expected rejection: %+v", options)
		}
	}
}

func TestMalformedDiscoveryFailsClosed(t *testing.T) {
	for _, kind := range []string{"duplicate", "cursor", "external-schema"} {
		t.Run(kind, func(t *testing.T) {
			s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
			s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
				return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
					if method != "tools/list" {
						return next(ctx, method, req)
					}
					tool := &mcp.Tool{Name: "echo", InputSchema: objectSchema}
					result := &mcp.ListToolsResult{Tools: []*mcp.Tool{tool}}
					switch kind {
					case "duplicate":
						result.Tools = append(result.Tools, tool)
					case "cursor":
						result.Tools = nil
						result.NextCursor = "repeat"
					case "external-schema":
						tool.InputSchema = map[string]any{"type": "object", "$ref": "https://invalid.example/schema"}
					}
					return result, nil
				}
			})
			cs := session(t, s, false, "2025-11-25")
			if _, err := Discover(t.Context(), cs, Options{Names: []string{"echo"}}); err == nil {
				t.Fatal("malformed discovery accepted")
			}
		})
	}
}

func TestStructuredOutputValidationAndErrorPreservation(t *testing.T) {
	_, validator, err := schema(map[string]any{"type": "number"}, 1024, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := convertResult(&mcp.CallToolResult{StructuredContent: float64(42)}, validator, 1024)
	if err != nil || string(got.StructuredContent) != "42" {
		t.Fatalf("primitive output: %+v %v", got, err)
	}
	got, err = convertResult(&mcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"reason": "fixture"}}, validator, 1024)
	if err != nil || !got.IsError || !strings.Contains(string(got.StructuredContent), "reason") {
		t.Fatalf("tool error: %+v %v", got, err)
	}
	if _, err := convertResult(&mcp.CallToolResult{}, validator, 1024); err == nil {
		t.Fatal("missing structured output accepted")
	}
}

func TestSchemaDriftAndInputValidationBeforeCall(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	h := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	}
	s.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object", "required": []any{"value"}, "properties": map[string]any{"value": map[string]any{"type": "string"}}}}, h)
	cs := session(t, s, false, "2025-11-25")
	tools, err := Discover(t.Context(), cs, Options{Names: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools[0].Execute(t.Context(), agent.ToolExecutionContext{ToolCall: agent.ToolCall{ID: "id"}, Args: map[string]any{}, OnUpdate: nil}); err == nil {
		t.Fatal("missing argument accepted")
	}
	// Caller-visible schema edits must not alter the closure's validation snapshot.
	tools[0].Parameters["required"] = nil
	if _, err := tools[0].Execute(t.Context(), agent.ToolExecutionContext{ToolCall: agent.ToolCall{ID: "id"}, Args: map[string]any{}, OnUpdate: nil}); err == nil {
		t.Fatal("mutated schema bypassed validator")
	}
	s.AddTool(&mcp.Tool{Name: "echo", InputSchema: objectSchema}, h)
	if _, err := tools[0].Execute(t.Context(), agent.ToolExecutionContext{ToolCall: agent.ToolCall{ID: "id"}, Args: map[string]any{"value": "ok"}, OnUpdate: nil}); err == nil || !strings.Contains(err.Error(), "schema changed") {
		t.Fatalf("drift err=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid/drifted call executed")
	}
}

func TestResultFailuresRemainDistinctAndNoRetry(t *testing.T) {
	for _, kind := range []string{"protocol", "unsupported", "oversized", "invalid-output", "input-required"} {
		t.Run(kind, func(t *testing.T) {
			s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
			var calls atomic.Int32
			tool := &mcp.Tool{Name: "test", InputSchema: objectSchema}
			if kind == "invalid-output" {
				tool.OutputSchema = map[string]any{"type": "object", "required": []any{"answer"}}
			}
			s.AddTool(tool, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				switch kind {
				case "protocol":
					return nil, errors.New("fixture protocol failure")
				case "unsupported":
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ResourceLink{URI: "https://invalid.example/do-not-fetch", Name: "fixture"}}}, nil
				case "oversized":
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", 512)}}}, nil
				case "invalid-output":
					return &mcp.CallToolResult{StructuredContent: map[string]any{}}, nil
				default:
					return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{}}, nil
				}
			})
			cs := session(t, s, false, "2026-07-28")
			tools, err := Discover(t.Context(), cs, Options{Names: []string{"test"}, MaxResultBytes: 256})
			if err != nil {
				t.Fatal(err)
			}
			result, err := tools[0].Execute(t.Context(), agent.ToolExecutionContext{ToolCall: agent.ToolCall{ID: "id"}, Args: map[string]any{}, OnUpdate: nil})
			if err == nil || result.IsError || calls.Load() != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
			}
		})
	}
}

func TestCancellationDoesNotCloseSession(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	entered := make(chan struct{})
	stopped := make(chan struct{})
	add(s, "wait", func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(entered)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	})
	cs := session(t, s, false, "2025-11-25")
	tools, err := Discover(t.Context(), cs, Options{Names: []string{"wait"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := tools[0].Execute(ctx, agent.ToolExecutionContext{ToolCall: agent.ToolCall{ID: "id"}, Args: map[string]any{}, OnUpdate: nil})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("call did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not cancel")
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("compliant peer did not stop")
	}
	if _, err := cs.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestCallerPolicyAlsoRequiredForDirectCalls(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	add(s, "echo", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	cs := session(t, s, false, "2025-11-25")
	tools, err := Discover(t.Context(), cs, Options{Names: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	invoke := tools[0].Execute
	allowed := false
	tools[0].Execute = func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
		if !allowed {
			return agent.ToolResult{}, errors.New("caller policy denied")
		}
		return invoke(ctx, execution)
	}
	call := agent.ToolCall{Name: "echo", Arguments: json.RawMessage(`{}`)}
	if out := agent.RunToolCall(t.Context(), call, agent.RunToolCallOptions{Tools: tools}); !out.IsError || calls.Load() != 0 {
		t.Fatal("direct call bypassed wrapper")
	}
	allowed = true
	if out := agent.RunToolCall(t.Context(), call, agent.RunToolCallOptions{Tools: tools}); out.IsError || calls.Load() != 1 {
		t.Fatalf("allowed call failed: %+v", out)
	}
}

type fixtureStream struct {
	message agent.Message
	events  chan agent.AssistantEvent
}

func (s *fixtureStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *fixtureStream) Wait() (agent.Message, error)        { return s.message, nil }
func (s *fixtureStream) Close() error                        { return nil }

func TestSameDefinitionThroughAgent(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	add(s, "echo", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echoed"}}, StructuredContent: "echoed"}, nil
	})
	cs := session(t, s, false, "2025-11-25")
	tools, err := Discover(t.Context(), cs, Options{Names: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	model := agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		requests++
		message := agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonStop, Parts: []agent.Part{{Type: agent.PartTypeText, Text: "done"}}}
		if requests == 1 {
			message.StopReason = agent.StopReasonToolUse
			message.ToolCalls = []agent.ToolCall{{ID: "call", Name: "echo", Arguments: json.RawMessage(`{}`)}}
		}
		events := make(chan agent.AssistantEvent, 1)
		events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
		close(events)
		return &fixtureStream{message: message, events: events}, nil
	})
	snapshot, err := agent.NewEngine().Run(t.Context(), agent.AgentDefinition{Model: model, Tools: tools}, &agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("call echo")}, nil)
	if err != nil || requests != 2 || calls.Load() != 1 {
		t.Fatalf("requests=%d calls=%d err=%v", requests, calls.Load(), err)
	}
	found := false
	for _, message := range snapshot.Messages {
		if message.Role == agent.RoleTool && message.ToolResult != nil && string(message.ToolResult.StructuredContent) == `"echoed"` {
			found = true
		}
	}
	if !found {
		t.Fatal("structured MCP result missing from Agent transcript")
	}
}

func TestNumericArgumentsRemainExactOnWire(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	s.AddTool(&mcp.Tool{Name: "number", InputSchema: map[string]any{"type": "object", "required": []any{"id", "nested"}, "properties": map[string]any{"id": map[string]any{"type": "integer"}, "nested": map[string]any{"type": "array", "items": map[string]any{"type": "number"}}}}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		var args map[string]any
		decoder := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
		decoder.UseNumber()
		if err := decoder.Decode(&args); err != nil {
			return nil, err
		}
		if args["id"] != json.Number("9007199254740993") || args["nested"].([]any)[0] != json.Number("-9007199254740993") {
			t.Errorf("rounded arguments: %s", req.Params.Arguments)
		}
		return &mcp.CallToolResult{}, nil
	})
	cs := session(t, s, false, "2025-11-25")
	tools, err := Discover(t.Context(), cs, Options{Names: []string{"number"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools[0].Execute(t.Context(), agent.ToolExecutionContext{ToolCall: agent.ToolCall{ID: "direct"}, Args: map[string]any{"id": int64(9007199254740993), "nested": []any{int64(-9007199254740993)}}, OnUpdate: nil}); err != nil {
		t.Fatal(err)
	}
	call := agent.ToolCall{ID: "call", Name: "number", Arguments: json.RawMessage(`{"id":9007199254740993,"nested":[-9007199254740993]}`)}
	if out := agent.RunToolCall(t.Context(), call, agent.RunToolCallOptions{Tools: tools}); out.IsError {
		t.Fatalf("direct helper: %+v", out)
	}
	requests := 0
	model := agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		requests++
		message := agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonStop}
		if requests == 1 {
			message.StopReason = agent.StopReasonToolUse
			message.ToolCalls = []agent.ToolCall{call}
		}
		events := make(chan agent.AssistantEvent, 1)
		events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
		close(events)
		return &fixtureStream{message: message, events: events}, nil
	})
	if _, err := agent.NewEngine().Run(t.Context(), agent.AgentDefinition{Model: model, Tools: tools}, &agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("number")}, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestUnsafeSDKResultIsPostExecutionFailure(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	add(s, "number", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "exact id: 9007199254740993"}}, StructuredContent: map[string]any{"nested": []any{int64(9007199254740993)}}}, nil
	})
	cs := session(t, s, false, "2025-11-25")
	tools, err := Discover(t.Context(), cs, Options{Names: []string{"number"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools[0].Execute(t.Context(), agent.ToolExecutionContext{ToolCall: agent.ToolCall{ID: "id"}, Args: map[string]any{}, OnUpdate: nil})
	var mapping *ResultError
	if !errors.As(err, &mapping) || !strings.Contains(err.Error(), "after remote response") || !strings.Contains(err.Error(), "do not retry") || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	if len(result.StructuredContent) != 0 || len(mapping.Result.StructuredContent) != 0 || len(result.Content) != 1 || result.Content[0].Text != "exact id: 9007199254740993" {
		t.Fatalf("unsafe output exposed or exact text lost: %+v", result)
	}
	for _, value := range []float64{1 << 53, -(1 << 53), 1 << 54} {
		if _, err := convertResult(&mcp.CallToolResult{StructuredContent: map[string]any{"nested": []any{value}}}, nil, 1024); err == nil {
			t.Fatal("unsafe boundary accepted")
		}
	}
	if _, err := convertResult(&mcp.CallToolResult{StructuredContent: float64((1 << 53) - 1)}, nil, 1024); err != nil {
		t.Fatal(err)
	}
	exact, err := convertResult(&mcp.CallToolResult{StructuredContent: map[string]any{"nested": []any{json.Number("9007199254740993")}}}, nil, 1024)
	if err != nil || string(exact.StructuredContent) != `{"nested":[9007199254740993]}` {
		t.Fatalf("exact retained number: %s %v", exact.StructuredContent, err)
	}
}

func TestUnsafeSDKSchemaRejected(t *testing.T) {
	if _, _, err := schema(map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"const": float64(1 << 53)}}}, 1024, true); err == nil {
		t.Fatal("rounded schema accepted")
	}
}

func TestExactValidationProjectionDomains(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want any
	}{{"9007199254740993", int64(9007199254740993)}, {"18446744073709551615", uint64(18446744073709551615)}, {"-9223372036854775808", int64(-9223372036854775808)}, {"0.125", float64(0.125)}, {"0.1", float64(0.1)}, {"19.99", float64(19.99)}, {"1e3", int64(1000)}} {
		got, err := validationProjection(json.Number(test.raw))
		if err != nil || got != test.want {
			t.Fatalf("%s got=%v(%T) err=%v", test.raw, got, got, err)
		}
	}
	for _, raw := range []string{"18446744073709551616", "-9223372036854775809", "0.10000000000000001", "1e99999999"} {
		if _, err := validationProjection(json.Number(raw)); err == nil {
			t.Fatalf("unsupported numeric domain accepted: %s", raw)
		}
	}
	_, validator, err := schema(map[string]any{"type": "number", "minimum": float64(0), "maximum": float64(2), "multipleOf": float64(0.125)}, 1024, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"0.125", "1.5"} {
		v, err := validationProjection(json.Number(raw))
		if err != nil || validator.Validate(v) != nil {
			t.Fatalf("exact number rejected: %s %v", raw, err)
		}
	}
	bad, _ := validationProjection(json.Number("2.125"))
	if validator.Validate(bad) == nil {
		t.Fatal("maximum ignored")
	}
}

func TestDecimalConstraintsAndWireValues(t *testing.T) {
	for _, test := range []struct {
		name, raw  string
		constraint map[string]any
		wantError  bool
	}{
		{"enum", "19.99", map[string]any{"enum": []any{19.99}}, false},
		{"const", "0.1", map[string]any{"const": 0.1}, false},
		{"minimum", "19.99", map[string]any{"minimum": 19.99}, false},
		{"maximum", "19.99", map[string]any{"maximum": 19.99}, false},
		{"multiple", "0.1", map[string]any{"multipleOf": 0.1}, false},
		{"wrong-const", "0.2", map[string]any{"const": 0.1}, true},
		{"wrong-maximum", "20", map[string]any{"maximum": 19.99}, true},
		{"library-price-multiple-limit", "19.99", map[string]any{"multipleOf": 0.01}, true},
		{"library-tenths-multiple-limit", "0.3", map[string]any{"multipleOf": 0.1}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
			var calls atomic.Int32
			property := map[string]any{"type": "number"}
			for key, value := range test.constraint {
				property[key] = value
			}
			s.AddTool(&mcp.Tool{Name: "price", InputSchema: map[string]any{"type": "object", "required": []any{"value"}, "properties": map[string]any{"value": property}}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				var args map[string]any
				d := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
				d.UseNumber()
				if err := d.Decode(&args); err != nil {
					return nil, err
				}
				if args["value"] != json.Number(test.raw) {
					t.Errorf("wire value changed: %s", req.Params.Arguments)
				}
				return &mcp.CallToolResult{}, nil
			})
			cs := session(t, s, false, "2025-11-25")
			tools, err := Discover(t.Context(), cs, Options{Names: []string{"price"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = tools[0].Execute(t.Context(), agent.ToolExecutionContext{ToolCall: agent.ToolCall{ID: "direct"}, Args: map[string]any{"value": json.Number(test.raw)}, OnUpdate: nil})
			if (err != nil) != test.wantError {
				t.Fatalf("direct err=%v", err)
			}
			out := agent.RunToolCall(t.Context(), agent.ToolCall{Name: "price", Arguments: json.RawMessage(`{"value":` + test.raw + `}`)}, agent.RunToolCallOptions{Tools: tools})
			if out.IsError != test.wantError {
				t.Fatalf("RunToolCall outcome=%+v", out)
			}
			want := int32(2)
			if test.wantError {
				want = 0
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d want=%d", calls.Load(), want)
			}
		})
	}
}
