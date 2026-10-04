package pigo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func customItem(id, call, name, input string) map[string]any {
	return map[string]any{"type": "custom_tool_call", "id": id, "call_id": call, "name": name, "input": input, "status": "completed"}
}
func customEvent(kind, input string) map[string]any {
	return map[string]any{"type": "response.output_item." + kind, "output_index": 0, "item": customItem("ctc_a", "call_a", "code", input)}
}
func customInputEvent(kind, input string) map[string]any {
	field := "delta"
	if kind == "done" {
		field = "input"
	}
	return map[string]any{"type": "response.custom_tool_call_input." + kind, "item_id": "ctc_a", "output_index": 0, field: input}
}
func customTerminal(items ...map[string]any) map[string]any {
	return map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": items}}
}

func customFixtureModel(transport, baseURL string) Model {
	model := *GetModel("openai", "gpt-6-astra")
	if strings.HasPrefix(transport, "codex") {
		provider := Provider("test-n2-native-codex")
		// A host registration exercises the adapter, without claiming built-in
		// Codex model capability or contacting a provider.
		if GetModel(provider, "fixture") == nil {
			RegisterProviderModule(ProviderModule{Provider: provider, Models: map[string]Model{"fixture": {
				API: "openai-codex-responses", MaxTokens: 256,
				Capabilities: ModelCapabilities{CustomTools: CapabilitySupported, GrammarTools: CapabilitySupported},
			}}})
		}
		model = *GetModel(provider, "fixture")
	}
	model.BaseURL = baseURL
	return model
}

func TestResponsesCustomStreamAcrossTransports(t *testing.T) {
	unicodeInput := "text(\"hello\\world\");\n// 雪 ❄️"
	for _, test := range []struct {
		name   string
		events []map[string]any
		want   []string
		fail   bool
	}{
		{"incremental unicode and escaped JSON", []map[string]any{customEvent("added", "text("), customInputEvent("delta", unicodeInput[len("text("):]), customInputEvent("done", unicodeInput), customEvent("done", unicodeInput), customTerminal(customItem("ctc_a", "call_a", "code", unicodeInput))}, []string{unicodeInput}, false},
		{"empty input", []map[string]any{customEvent("added", ""), customInputEvent("done", ""), customEvent("done", ""), customTerminal()}, []string{""}, false},
		{"terminal only", []map[string]any{customTerminal(customItem("ctc_a", "call_a", "code", "terminal"))}, []string{"terminal"}, false},
		{"item done only", []map[string]any{customEvent("done", "item"), customTerminal()}, []string{"item"}, false},
		{"terminal completes partial", []map[string]any{customEvent("added", "a"), customTerminal(customItem("ctc_a", "call_a", "code", "abc"))}, []string{"abc"}, false},
		{"duplicate done", []map[string]any{customEvent("added", ""), customInputEvent("done", "abc"), customInputEvent("done", "abc"), customEvent("done", "abc"), customEvent("done", "abc"), customTerminal(customItem("ctc_a", "call_a", "code", "abc"))}, []string{"abc"}, false},
		{"unfinished", []map[string]any{customEvent("added", "partial"), customTerminal()}, nil, true},
		{"input done without item completion", []map[string]any{customEvent("added", ""), customInputEvent("done", "abc"), customTerminal()}, nil, true},
		{"missing terminal", []map[string]any{customEvent("done", "abc")}, nil, true},
		{"nonmonotonic done", []map[string]any{customEvent("added", "abc"), customInputEvent("done", "abd"), customTerminal()}, nil, true},
		{"delta after input done", []map[string]any{customEvent("added", ""), customInputEvent("done", "a"), customInputEvent("delta", "b"), customTerminal()}, nil, true},
		{"conflicting terminal", []map[string]any{customEvent("done", "abc"), customTerminal(customItem("ctc_a", "call_a", "code", "abd"))}, nil, true},
		{"undeclared tool", []map[string]any{customTerminal(customItem("ctc_a", "call_a", "unknown", "abc"))}, nil, true},
		{"malformed output cannot trigger capacity retry", []map[string]any{customTerminal(customItem("ctc_a", "call_a", "model is at capacity", "abc"))}, nil, true},
		{"undeclared namespace", []map[string]any{customTerminal(map[string]any{"type": "custom_tool_call", "id": "ctc_a", "call_id": "call_a", "name": "code", "namespace": "unknown", "input": "abc"})}, nil, true},
		{"invalid item ID", []map[string]any{customTerminal(customItem("fc_a", "call_a", "code", "abc"))}, nil, true},
		{"invalid identity", []map[string]any{customEvent("added", ""), {"type": "response.custom_tool_call_input.delta", "item_id": "ctc_wrong", "output_index": 0, "delta": "bad"}, customTerminal()}, nil, true},
		{"unknown index", []map[string]any{customEvent("added", ""), {"type": "response.custom_tool_call_input.delta", "item_id": "ctc_a", "output_index": 1, "delta": "bad"}, customTerminal()}, nil, true},
		{"nonstring input", []map[string]any{customEvent("added", ""), {"type": "response.custom_tool_call_input.done", "item_id": "ctc_a", "output_index": 0, "input": 42}, customTerminal()}, nil, true},
		{"missing input", []map[string]any{{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "custom_tool_call", "id": "ctc_a", "call_id": "call_a", "name": "code"}}, customTerminal()}, nil, true},
		{"unfinished item status", []map[string]any{{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "custom_tool_call", "id": "ctc_a", "call_id": "call_a", "name": "code", "input": "x", "status": "incomplete"}}, customTerminal()}, nil, true},
		{"function event cannot alter custom input", []map[string]any{customEvent("added", ""), {"type": "response.function_call_arguments.delta", "item_id": "ctc_a", "output_index": 0, "delta": "{}"}, customTerminal()}, nil, true},
		{"item cannot move index", []map[string]any{customEvent("added", ""), {"type": "response.output_item.done", "output_index": 9, "item": customItem("ctc_a", "call_a", "code", "a")}, customTerminal()}, nil, true},
		{"huge index", []map[string]any{{"type": "response.output_item.added", "output_index": 1e100, "item": customItem("ctc_a", "call_a", "code", "")}, customTerminal()}, nil, true},
		{"native declaration cannot return a function", []map[string]any{responsesToolEvent("response.output_item.done", "fc_a", "call_a", "code", `{"code":"abc"}`, 0), customTerminal()}, nil, true},
		{"interleaved custom calls", []map[string]any{customEvent("added", ""), {"type": "response.output_item.added", "output_index": 1, "item": customItem("ctc_b", "call_b", "code", "B")}, customInputEvent("delta", "A"), {"type": "response.output_item.done", "output_index": 1, "item": customItem("ctc_b", "call_b", "code", "BB")}, customEvent("done", "AA"), customTerminal()}, []string{"AA", "BB"}, false},
	} {
		for _, transport := range []string{"responses-sse", "codex-sse", "codex-websocket"} {
			t.Run(test.name+"/"+transport, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if transport == "codex-websocket" {
						conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.Close()
						if _, _, err = conn.ReadMessage(); err != nil {
							return
						}
						for _, event := range test.events {
							if conn.WriteJSON(event) != nil {
								return
							}
						}
						return
					}
					var request map[string]any
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					tools := request["tools"].([]any)
					if tools[0].(map[string]any)["type"] != "custom" {
						t.Errorf("tools=%v", tools)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, buildOpenAICodexSSE(test.events...))
				}))
				defer server.Close()
				model := customFixtureModel(transport, server.URL)
				options := SimpleStreamOptions{APIKey: "fixture", MaxRetries: 2, MaxRetryDelay: 1}
				if strings.HasPrefix(transport, "codex") {
					options.APIKey = makeOpenAICodexToken("fixture")
					options.Transport = TransportSSE
				}
				if transport == "codex-websocket" {
					options.Transport = TransportWebSocket
				}
				stream := StreamSimple(model, Context{Tools: []Tool{samplingTool("text", "require")}}, options)
				deltas := map[int]string{}
				ends := 0
				for event := range stream.Events() {
					if event.Type == AssistantMessageEventToolCallDelta {
						deltas[event.ContentIndex] += event.Delta
					}
					if event.Type == AssistantMessageEventToolCallEnd {
						ends++
					}
				}
				result := stream.Result()
				if requests.Load() != 1 {
					t.Fatalf("requests=%d", requests.Load())
				}
				if test.fail {
					if result.StopReason != StopReasonError {
						t.Fatalf("expected failure: %+v", result)
					}
					return
				}
				if result.StopReason != StopReasonToolUse || ends != len(test.want) {
					t.Fatalf("result=%+v ends=%d", result, ends)
				}
				for i, want := range test.want {
					call := result.Content[i].(ToolCall)
					if call.Arguments["code"] != want || !strings.Contains(call.ID, "|ctc_") {
						t.Fatalf("call=%+v", call)
					}
					var args map[string]any
					if err := json.Unmarshal([]byte(deltas[i]), &args); err != nil || !reflect.DeepEqual(args, call.Arguments) {
						t.Fatalf("deltas=%q args=%v err=%v", deltas[i], args, err)
					}
				}
			})
		}
	}
}

func TestResponsesCustomReplayAndFunctionFallback(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	tool := samplingTool("text", "prefer")
	assistant := AssistantMessage{Provider: model.Provider, API: model.API, Model: model.ID, StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "call_a|ctc_a", Name: "code", Arguments: map[string]any{"code": "text(1)"}}}}
	ctx := Context{Tools: []Tool{tool}, Messages: []Message{assistant, ToolResultMessage{ToolCallID: "call_a|ctc_a", ToolName: "code", IsError: true, Content: []ContentBlock{TextContent{Text: "tool failed"}}}}}
	for _, test := range []struct {
		name   string
		target Model
		custom bool
		wantID string
	}{
		{"same model", model, true, "ctc_a"},
		{"unsupported model", *GetModel("openai", "gpt-5.4"), false, ""},
		{"unsupported codex", *GetModel("openai-codex", "gpt-6-astra"), false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			custom, err := resolveContextToolSampling(test.target, ctx)
			if err != nil {
				t.Fatal(err)
			}
			wire := convertOpenAIResponsesMessages(nil, custom, test.target, ResolveTranscript(NormalizeContext(ctx), supportsTranscriptSystemMessages(test.target)), true)
			var call, output map[string]any
			for _, item := range wire {
				if item["name"] == "code" {
					call = item
				}
				if item["output"] != nil {
					output = item
				}
			}
			kind := "function_call"
			if test.custom {
				kind = "custom_tool_call"
			}
			if call["type"] != kind || output["type"] != kind+"_output" || call["call_id"] != "call_a" || output["call_id"] != "call_a" || output["output"] != "tool failed" {
				t.Fatalf("wire=%v", wire)
			}
			if test.wantID == "" {
				if call["id"] != nil {
					t.Fatalf("unexpected id=%v", call)
				}
			} else if call["id"] != test.wantID {
				t.Fatalf("id=%v", call)
			}
		})
	}
	// Changing an old function call to custom must omit its incompatible item ID.
	assistant.Content[0] = ToolCall{ID: "call_a|fc_a", Name: "code", Arguments: map[string]any{"code": "text(1)"}}
	ctx.Messages[0] = assistant
	ctx.Messages[1] = ToolResultMessage{ToolCallID: "call_a|fc_a", ToolName: "code", Content: []ContentBlock{TextContent{Text: "ok"}}}
	custom, err := resolveContextToolSampling(model, ctx)
	if err != nil {
		t.Fatal(err)
	}
	wire := convertOpenAIResponsesMessages(nil, custom, model, ResolveTranscript(NormalizeContext(ctx), supportsTranscriptSystemMessages(model)), true)
	for _, item := range wire {
		if item["type"] == "custom_tool_call" && item["id"] != nil {
			t.Fatalf("function ID on custom replay: %v", item)
		}
	}
	// A removed declaration replays as an ordinary function, paired by call ID.
	ctx.Tools = nil
	custom, err = resolveContextToolSampling(model, ctx)
	if err != nil {
		t.Fatal(err)
	}
	wire = convertOpenAIResponsesMessages(nil, custom, model, ResolveTranscript(NormalizeContext(ctx), supportsTranscriptSystemMessages(model)), true)
	for _, item := range wire {
		if item["type"] == "custom_tool_call" || item["type"] == "custom_tool_call_output" {
			t.Fatalf("retired custom replay: %v", wire)
		}
	}
}

func TestResponsesCustomCancellationAndNo400Fallback(t *testing.T) {
	for _, cancelStream := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelStream), func(t *testing.T) {
			var calls atomic.Int32
			requestCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if !cancelStream {
					http.Error(w, `{"error":{"message":"unsupported grammar"}}`, 400)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, buildOpenAICodexSSE(customEvent("added", "partial")))
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			model := customFixtureModel("responses-sse", server.URL)
			stream := StreamSimple(model, Context{Tools: []Tool{samplingTool("grammar", "require")}}, SimpleStreamOptions{APIKey: "fixture", RequestContext: requestCtx, MaxRetries: 3, MaxRetryDelay: 1})
			ends := 0
			for event := range stream.Events() {
				if event.Type == AssistantMessageEventToolCallStart {
					cancel()
				}
				if event.Type == AssistantMessageEventToolCallEnd {
					ends++
				}
			}
			result := stream.Result()
			want := StopReasonError
			if cancelStream {
				want = StopReasonAborted
			}
			if result.StopReason != want || calls.Load() != 1 || ends != 0 {
				t.Fatalf("result=%+v calls=%d ends=%d", result, calls.Load(), ends)
			}
		})
	}
}

func TestResponsesCustomResourceBounds(t *testing.T) {
	for _, limit := range []string{"input", "total", "calls"} {
		t.Run(limit, func(t *testing.T) {
			stream := newAssistantMessageEventStream()
			drained := make(chan struct{})
			go func() {
				for range stream.Events() {
				}
				close(drained)
			}()
			response := AssistantMessage{}
			defer func() { stream.finish(response); <-drained }()
			state := openAIResponsesStreamingState{CurrentTextIndex: -1, CurrentThinkingIndex: -1, CustomTools: map[string]string{"code": "code"}, FinalizedItemKeys: map[string]bool{}}
			input := "x"
			count := 1
			switch limit {
			case "input":
				input = strings.Repeat("x", MaxCustomToolInputBytes+1)
			case "total":
				input = strings.Repeat("x", MaxCustomToolInputBytes)
				count = 5
			case "calls":
				count = maxCustomResponseCalls + 1
			}
			for i := 0; i < count; i++ {
				item := openAIResponsesResponseItem{Type: "custom_tool_call", ID: fmt.Sprintf("ctc_%d", i), CallID: fmt.Sprintf("call_%d", i), Name: "code", Input: &input}
				err := processOpenAIResponsesCustomItem(&response, stream, &state, nil, item, true)
				if (i == count-1) != (err != nil) {
					t.Fatalf("iteration=%d err=%v", i, err)
				}
			}
			if state.CustomInputBytes > maxCustomResponseBytes || state.CustomCalls > maxCustomResponseCalls {
				t.Fatal("resource bound exceeded")
			}
		})
	}
}

func TestResponsesCustomMalformedWire(t *testing.T) {
	for _, malformed := range []string{"{broken", `{"type":"response.custom_tool_call_input.done","item_id":"ctc_a","output_index":0,"input":"partial\ud800"}`} {
		t.Run(malformed, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, buildOpenAICodexSSE(customEvent("added", "partial"))+"data: "+malformed+"\n\n"+buildOpenAICodexSSE(customEvent("done", "partial\ufffd"), customTerminal()))
			}))
			defer server.Close()
			model := customFixtureModel("responses-sse", server.URL)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			result := CompleteSimple(model, Context{Tools: []Tool{samplingTool("text", "require")}}, SimpleStreamOptions{APIKey: "fixture", RequestContext: ctx})
			if result.StopReason != StopReasonError {
				t.Fatalf("malformed stream accepted: %+v", result)
			}
			if strings.Contains(malformed, `\ud800`) && !strings.Contains(result.ErrorMessage, "invalid_unicode") {
				t.Fatalf("Unicode replacement was not rejected: %+v", result)
			}
		})
	}
}
