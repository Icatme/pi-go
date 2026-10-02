package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/internal/jsontext"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestArgumentUnicodeRejectedBeforeMCPDecodeOrSend(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "unicode", Version: "1"}, nil)
	var calls atomic.Int32
	add(server, "write", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	tools, err := Discover(t.Context(), session(t, server, false, "2025-11-25"), Options{Names: []string{"write"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"id":"abc\ud800"}`, `{"id":"\udfff"}`, `{"\ud800":"id"}`,
		`{"id":"\ud800\ud800"}`, "{\"id\":\"" + string([]byte{0xff}) + "\"}",
		"{\"" + string([]byte{0xed, 0xa0, 0x80}) + "\":1}",
	} {
		t.Run(raw, func(t *testing.T) {
			call := agent.ToolCall{Name: "write", Arguments: json.RawMessage(raw)}
			_, err := tools[0].ParseArguments(call)
			var classified *agent.ToolExecutionError
			if !errors.As(err, &classified) || classified.Code != agent.ToolFailureArgumentInvalid || classified.Reason != "invalid_unicode" || classified.Execution.Remote != agent.ToolRemoteNotDispatched {
				t.Fatalf("parser accepted/reclassified Unicode: %q err=%v", raw, err)
			}
			outcome := agent.RunToolCall(t.Context(), call, agent.RunToolCallOptions{Tools: tools})
			if !outcome.IsError || outcome.Failure == nil || outcome.Failure.Reason != "invalid_unicode" || outcome.Execution.Remote != agent.ToolRemoteNotDispatched || calls.Load() != 0 {
				t.Fatalf("invalid Unicode dispatched: %q outcome=%+v calls=%d", raw, outcome, calls.Load())
			}
		})
	}
}

func TestArgumentUnicodePreservesPairsAndReplacementRune(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "unicode", Version: "1"}, nil)
	var calls atomic.Int32
	add(server, "write", func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]string
		if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
			return nil, err
		}
		if args["😀"] != "abc😀�" {
			t.Errorf("Unicode identifier changed on wire: %q", request.Params.Arguments)
		}
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	tools, err := Discover(t.Context(), session(t, server, false, "2025-11-25"), Options{Names: []string{"write"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"\ud83d\ude00":"abc\ud83d\ude00\ufffd"}`, `{"😀":"abc😀�"}`} {
		outcome := agent.RunToolCall(t.Context(), agent.ToolCall{Name: "write", Arguments: json.RawMessage(raw)}, agent.RunToolCallOptions{Tools: tools})
		if outcome.IsError {
			t.Fatalf("valid Unicode rejected: %s outcome=%+v", raw, outcome)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("valid Unicode calls=%d", calls.Load())
	}
}

func TestArgumentGoUTF8RejectedBeforeMarshal(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "unicode", Version: "1"}, nil)
	var calls atomic.Int32
	add(server, "write", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	tools, err := Discover(t.Context(), session(t, server, false, "2025-11-25"), Options{Names: []string{"write"}})
	if err != nil {
		t.Fatal(err)
	}
	invalid := string([]byte{0xff})
	for _, args := range []map[string]any{{"id": invalid}, {invalid: "id"}, {"nested": []string{invalid}}, {"nested": map[string]string{invalid: "id"}}, {"nested": json.RawMessage(`"\ud800"`)}} {
		call := agent.ToolCall{Name: "write", ParsedArgs: args}
		_, err := tools[0].ParseArguments(call)
		var classified *agent.ToolExecutionError
		if !errors.As(err, &classified) || classified.Reason != "invalid_unicode" {
			t.Fatalf("parsed Go strings silently repaired: %+v err=%v", args, err)
		}
		_, err = tools[0].Execute(t.Context(), agent.ToolExecutionContext{Args: args})
		if !errors.As(err, &classified) || classified.Reason != "invalid_unicode" || classified.Execution.Remote != agent.ToolRemoteNotDispatched || calls.Load() != 0 {
			t.Fatalf("direct Go strings sent: %+v err=%v calls=%d", args, err, calls.Load())
		}
	}
	_, err = tools[0].ParseArguments(agent.ToolCall{Arguments: json.RawMessage(`{"id":"\ud800"}`), ParsedArgs: map[string]any{"id": "safe"}})
	var classified *agent.ToolExecutionError
	if !errors.As(err, &classified) || classified.Reason != "invalid_unicode" {
		t.Fatalf("ParsedArgs hid invalid original JSON: %v", err)
	}
}

func TestRawSchemaUnicodeRejectedBeforeDecode(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage(`{"type":"object","title":"\ud800"}`), json.RawMessage(`{"type":"object","\udfff":1}`)} {
		if _, _, err := schema(raw, 1024, true); !errors.Is(err, jsontext.ErrInvalidUnicode) {
			t.Fatalf("raw schema string replaced before validation: %q err=%v", raw, err)
		}
	}
}
