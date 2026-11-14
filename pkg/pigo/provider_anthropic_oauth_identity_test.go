package pigo

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAnthropicOAuthToolIdentity(t *testing.T) {
	if got := anthropicToolNameForInbound("ſ", []Tool{{Name: "S"}}, nil, true); got != "ſ" {
		t.Fatalf("unexpected Unicode alias %q", got)
	}
	if got := anthropicToolNameForInbound("Foo", []Tool{{Name: " Foo "}}, nil, true); got != " Foo " {
		t.Fatalf("trimmed wire original identity %q", got)
	}

	for _, tools := range [][]Tool{{{Name: "foo"}, {Name: "Foo"}}, {{Name: "Foo"}, {Name: "foo"}}} {
		for _, name := range []string{"foo", "Foo", "FOO", "unknown"} {
			if got := anthropicToolNameForInbound(name, tools, nil, true); got != name {
				t.Errorf("%q mapped to %q", name, got)
			}
		}
	}
	for _, tc := range []struct{ wire, want string }{{"Read", "read"}, {"READ", "read"}, {"unknown", "unknown"}} {
		if got := anthropicToolNameForInbound(tc.wire, []Tool{{Name: "read"}}, nil, true); got != tc.want {
			t.Errorf("%q got %q", tc.wire, got)
		}
	}
	if got := anthropicToolNameForInbound("Read", []Tool{{Name: "read"}}, nil, false); got != "Read" {
		t.Fatal(got)
	}
}

func TestAnthropicOAuthToolCollisionBeforeHTTP(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(400) }))
		tools := []Tool{{Name: "task", Parameters: map[string]any{"type": "object"}}, {Name: "Task", Parameters: map[string]any{"type": "object"}}}
		if reverse {
			tools[0], tools[1] = tools[1], tools[0]
		}
		model := Model{ID: "fixture", Provider: "anthropic", API: "anthropic-messages", BaseURL: server.URL}
		result, err := completeAnthropicOnce(model, Context{Tools: tools}, AnthropicMessagesProviderOptions{}, "fixture", true)
		failure, ok := ProviderFailureOf(result)
		if err == nil || !ok || failure.Kind != ProviderFailureSDK || failure.Reason != "invalid_tool_names" || requests.Load() != 0 {
			t.Fatalf("%v %+v requests%d", err, result, requests.Load())
		}
		// API-key paths preserve original distinct names and are not rejected locally.
		_, _ = completeAnthropicOnce(model, Context{Tools: tools}, AnthropicMessagesProviderOptions{}, "fixture", false)
		if requests.Load() != 1 {
			t.Fatal("API-key request unexpectedly blocked")
		}
		server.Close()
	}
}

func TestAnthropicOAuthToolIdentityHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture"}},
			map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "call_Foo", "name": "Foo", "input": map[string]any{}}},
			map[string]any{"type": "content_block_stop", "index": 0},
			map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}},
			map[string]any{"type": "message_stop"},
		)))
	}))
	defer server.Close()
	model := Model{ID: "fixture", Provider: "anthropic", API: "anthropic-messages", BaseURL: server.URL}
	ctx := Context{Tools: []Tool{{Name: "foo", Parameters: map[string]any{"type": "object"}}, {Name: "Foo", Parameters: map[string]any{"type": "object"}}}}
	result, err := completeAnthropicOnce(model, ctx, AnthropicMessagesProviderOptions{}, "fixture", true)
	if err != nil || len(result.Content) != 1 {
		t.Fatalf("%v %+v", err, result)
	}
	call, ok := result.Content[0].(ToolCall)
	if !ok || call.Name != "Foo" || call.ID != "call_Foo" || requests.Load() != 1 {
		t.Fatalf("%+v requests%d", result, requests.Load())
	}
	options := AnthropicMessagesProviderOptions{StreamOptions: StreamOptions{OnPayload: func(payload any, _ Model) any {
		return map[string]any{"tools": []any{map[string]any{"name": "same"}, map[string]any{"name": "same"}}}
	}}}
	result, err = completeAnthropicOnce(model, ctx, options, "fixture", true)
	f, ok := ProviderFailureOf(result)
	if err == nil || !ok || f.Reason != "invalid_tool_names" || requests.Load() != 1 {
		t.Fatalf("hook collision not rejected %v %+v", err, result)
	}
}

func TestAnthropicOAuthHostedCustomWireCollision(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	ctx := Context{Tools: []Tool{{Name: "$web_search", Parameters: map[string]any{"type": "object"}}}, HostedTools: []HostedTool{{Type: HostedToolTypeWebSearch, Name: "search"}}}
	result, err := completeAnthropicOnce(Model{ID: "fixture", Provider: "anthropic", API: "anthropic-messages", BaseURL: server.URL, HostedTools: HostedToolCapabilities{WebSearch: true}}, ctx, AnthropicMessagesProviderOptions{}, "fixture", true)
	f, ok := ProviderFailureOf(result)
	if err == nil || !ok || f.Reason != "invalid_tool_names" || requests.Load() != 0 {
		t.Fatalf("%v %+v requests%d", err, result, requests.Load())
	}
}

func TestAnthropicOAuthOriginalCollisionCannotBeHiddenByHook(t *testing.T) {
	for _, name := range []string{"Task", "renamed"} {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
		hookCalled := false
		options := AnthropicMessagesProviderOptions{StreamOptions: StreamOptions{OnPayload: func(_ any, _ Model) any {
			hookCalled = true
			return map[string]any{"tools": []any{map[string]any{"name": name}}}
		}}}
		ctx := Context{Tools: []Tool{{Name: "task"}, {Name: "Task"}}}
		result, err := completeAnthropicOnce(Model{ID: "fixture", Provider: "anthropic", API: "anthropic-messages", BaseURL: server.URL}, ctx, options, "fixture", true)
		f, ok := ProviderFailureOf(result)
		if err == nil || !ok || f.Reason != "invalid_tool_names" || requests.Load() != 0 || hookCalled {
			t.Fatalf("%v %+v requests%d hook%v", err, result, requests.Load(), hookCalled)
		}
		server.Close()
	}
}

func TestAnthropicOAuthDistinctBuiltinFunctionNames(t *testing.T) {
	if err := validateAnthropicOAuthToolNames([]byte(`{"tools":[{"type":"builtin_function","function":{"name":"$web_search"}},{"type":"builtin_function","function":{"name":"$fetch"}}]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicOAuthNilReturningHookCollision(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	options := AnthropicMessagesProviderOptions{StreamOptions: StreamOptions{OnPayload: func(payload any, _ Model) any {
		request := payload.(anthropicRequest)
		request.Tools[1].Name = request.Tools[0].Name
		return nil
	}}}
	result, err := completeAnthropicOnce(Model{ID: "fixture", Provider: "anthropic", API: "anthropic-messages", BaseURL: server.URL}, Context{Tools: []Tool{{Name: "one"}, {Name: "two"}}}, options, "fixture", true)
	f, ok := ProviderFailureOf(result)
	if err == nil || !ok || f.Reason != "invalid_tool_names" || requests.Load() != 0 {
		t.Fatalf("%v %+v requests%d", err, result, requests.Load())
	}
}
