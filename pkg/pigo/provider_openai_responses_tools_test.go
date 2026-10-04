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
)

func TestResponsesToolAnchorsCanStartAfterConversation(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	ctx := Context{Messages: []Message{UserMessage{Content: "before any tools"}, SystemMessage{ToolsAdded: []Tool{anchorFunction("late")}}, UserMessage{Content: "use late"}}}
	request := mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
	if len(request.Tools) != 0 || countToolAnchors(request.Input) != 1 || request.Input[1]["type"] != "additional_tools" {
		t.Fatal(request)
	}
}

func TestResponsesToolAnchorsSurviveFailedHistoryFiltering(t *testing.T) {
	for _, stop := range []StopReason{StopReasonError, StopReasonAborted, StopReasonStop} {
		for _, initial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/initial_%t", stop, initial), func(t *testing.T) {
				requests := make(chan openAIResponsesRequest, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request openAIResponsesRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					requests <- request
					w.Header().Set("Content-Type", "text/event-stream")
					item := map[string]any{"type": "function_call", "id": "fc_late", "call_id": "call_late", "name": "late", "namespace": "late", "arguments": "{}", "status": "completed"}
					fmt.Fprint(w, buildOpenAICodexSSE(customTerminal(item)))
				}))
				defer server.Close()
				model := *GetModel("openai", "gpt-6-astra")
				model.BaseURL = server.URL
				ctx := Context{Messages: []Message{
					AssistantMessage{StopReason: stop, Content: []ContentBlock{TextContent{Text: "previous attempt"}}},
					SystemMessage{ToolsAdded: []Tool{anchorFunction("late")}},
					UserMessage{Content: "use late"},
				}}
				if initial {
					ctx.Tools = []Tool{anchorFunction("initial")}
				}
				result := CompleteSimple(model, ctx, SimpleStreamOptions{APIKey: "fixture", MaxRetries: 0})
				if result.StopReason != StopReasonToolUse {
					t.Fatalf("unexpected result: %+v", result)
				}
				request := <-requests
				wantTools := 0
				if initial {
					wantTools = 1
				}
				anchorIndex := 0
				if stop == StopReasonStop {
					anchorIndex = 1
				}
				if len(request.Tools) != wantTools || countToolAnchors(request.Input) != 1 || request.Input[anchorIndex]["type"] != "additional_tools" {
					t.Fatalf("declaration lost or moved after filtering: %+v", request)
				}
				tools := request.Input[anchorIndex]["tools"].([]any)
				if len(tools) != 1 || tools[0].(map[string]any)["name"] != "late" || request.Input[anchorIndex+1]["role"] != "user" {
					t.Fatalf("late declaration must precede its user message: %+v", request.Input)
				}
				ctx.Messages = append(ctx.Messages, result, ToolResultMessage{ToolCallID: "call_late|fc_late", ToolName: "late", Content: []ContentBlock{TextContent{Text: "done"}}})
				replay := mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
				call := replay.Input[len(replay.Input)-2]
				if call["namespace"] != "late" || call["id"] != "fc_late" {
					t.Fatalf("anchored replay lost its declaration identity: %v", call)
				}
			})
		}
	}
}

func TestResponsesSystemUpdatesSurviveFailedHistoryFiltering(t *testing.T) {
	for _, api := range []API{"openai-responses", "openai-codex-responses"} {
		for _, stop := range []StopReason{StopReasonError, StopReasonAborted} {
			t.Run(fmt.Sprintf("%s/%s", api, stop), func(t *testing.T) {
				model := *GetModel("openai", "gpt-6-astra")
				model.API = api
				ctx := Context{Messages: []Message{
					AssistantMessage{StopReason: stop},
					SystemMessage{Sections: map[string]*string{"policy": transcriptString("new policy"), "removed": nil}},
					UserMessage{Content: "continue"},
				}}
				var request openAIResponsesRequest
				wantInstructions := ""
				if api == "openai-codex-responses" {
					request = buildOpenAICodexRequest(nil, model, ctx, ProviderStreamOptions{})
					wantInstructions = "You are a helpful assistant."
				} else {
					request = mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
				}
				wantUpdate := "Updated system prompt section \"policy\":\n\nnew policy\n\nRemoved system prompt section \"removed\"."
				if request.Instructions != wantInstructions || len(request.Input) != 2 || request.Input[0]["content"] != wantUpdate || request.Input[1]["role"] != "user" {
					t.Fatalf("dynamic system update was treated as initial: %+v", request)
				}
			})
		}
	}
}

func TestResponsesAnchoredFunctionStreamBoundaries(t *testing.T) {
	for _, mode := range []string{"incremental", "namespace changes", "conflicting terminal", "changed call identity", "call count bound", "partial", "cancel", "malformed capacity output", "http 400"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if mode == "http 400" {
					http.Error(w, "fixture rejected additional_tools", http.StatusBadRequest)
					return
				}
				item := map[string]any{"type": "function_call", "id": "fc_lookup", "call_id": "call_lookup", "name": "lookup", "namespace": "lookup", "arguments": "{}", "status": "completed"}
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "call count bound" {
					items := make([]map[string]any, 0, 129)
					for i := 0; i < 129; i++ {
						items = append(items, map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", i), "call_id": fmt.Sprintf("call_%d", i), "name": "lookup", "namespace": "lookup", "arguments": "{}", "status": "completed"})
					}
					fmt.Fprint(w, buildOpenAICodexSSE(customTerminal(items...)))
					return
				}
				if mode == "malformed capacity output" {
					item["status"], item["name"], item["namespace"] = "model is at capacity", "lookup", "lookup"
					fmt.Fprint(w, buildOpenAICodexSSE(customTerminal(item)))
					return
				}
				fmt.Fprint(w, buildOpenAICodexSSE(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item}))
				if mode == "partial" {
					return
				}
				if mode == "cancel" {
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				if mode == "namespace changes" {
					item["namespace"] = ""
				}
				fmt.Fprint(w, buildOpenAICodexSSE(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item}))
				if mode == "conflicting terminal" {
					item["namespace"] = "other"
				}
				if mode == "changed call identity" {
					item["call_id"] = "call_changed"
				}
				fmt.Fprint(w, buildOpenAICodexSSE(customTerminal(item)))
			}))
			defer server.Close()
			model := *GetModel("openai", "gpt-6-astra")
			model.BaseURL = server.URL
			result := CompleteSimple(model, anchorContext(), SimpleStreamOptions{APIKey: "fixture", RequestContext: ctx, MaxRetries: 2, MaxRetryDelay: 1, OnProviderStreamEvent: func(_ json.RawMessage, _ Model) error {
				if mode == "cancel" {
					cancel()
				}
				return nil
			}})
			if (result.StopReason == StopReasonToolUse) != (mode == "incremental") || requests.Load() != 1 {
				t.Fatalf("requests=%d reason=%s calls=%d error=%s", requests.Load(), result.StopReason, len(result.Content), result.ErrorMessage)
			}
		})
	}
}

func mustBuildOpenAIResponsesRequest(t *testing.T, custom map[string]string, model Model, ctx Context, options ProviderStreamOptions) openAIResponsesRequest {
	t.Helper()
	request, err := buildOpenAIResponsesRequest(custom, model, ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestResponsesToolAnchorsRejectStatefulReplayBeforeDispatch(t *testing.T) {
	for _, midSystem := range []bool{true, false} {
		for _, change := range []string{"add", "remove", "redefine", "remove and replace"} {
			t.Run(fmt.Sprintf("mid_system_%t/%s", midSystem, change), func(t *testing.T) {
				model := *GetModel("openai", "gpt-6-astra")
				model.Compat = &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &midSystem}
				ctx := anchorContext()
				switch change {
				case "remove":
					ctx.Messages = append(ctx.Messages, SystemMessage{ToolsRemoved: []ToolReference{{Name: "lookup"}}})
				case "redefine", "remove and replace":
					tool := anchorFunction("lookup")
					tool.Description = "changed schema revision"
					update := SystemMessage{ToolsAdded: []Tool{tool}}
					if change == "remove and replace" {
						update.ToolsRemoved = []ToolReference{{Name: "lookup"}}
					}
					ctx.Messages = append(ctx.Messages, update)
				}
				options := ProviderStreamOptions{PreviousResponseID: "prior_with_lookup"}
				if request, err := buildOpenAIResponsesRequest(nil, model, ctx, options); err == nil || !strings.Contains(err.Error(), "full history") {
					t.Errorf("stateful dynamic replay accepted: previous=%q tools=%v err=%v", request.PreviousResponseID, request.Tools, err)
				}
				payloads, requests := 0, 0
				options.APIKey = "fixture"
				options.HTTPClient = &http.Client{Transport: samplingRoundTripper(func(*http.Request) (*http.Response, error) { requests++; return nil, fmt.Errorf("unexpected request") })}
				options.OnPayload = func(any, Model) any { payloads++; return nil }
				result := Complete(model, ctx, options)
				if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "full history") || requests != 0 || payloads != 0 {
					t.Errorf("result=%+v requests=%d payloads=%d", result, requests, payloads)
				}
				// Disabling mid-conversation systems still permits the documented
				// stateless fallback; it must not permit inherited server state.
				if !midSystem {
					request := mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
					if countToolAnchors(request.Input) != 0 || !reflect.DeepEqual(request.Tools, convertOpenAIResponsesTools(nil, currentContextTools(ctx))) {
						t.Fatal(request)
					}
				}
				static := Context{Tools: []Tool{anchorFunction("initial")}}
				request := mustBuildOpenAIResponsesRequest(t, nil, model, static, ProviderStreamOptions{PreviousResponseID: "prior_static"})
				if request.PreviousResponseID != "prior_static" {
					t.Fatal("static replay option changed")
				}
			})
		}
	}
}

func anchorFunction(name string) Tool {
	return Tool{Name: name, Description: "Synthetic local lookup", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
}

func TestResponsesToolAnchorsNoMidSystemRejectsBeforeAuthorization(t *testing.T) {
	provider := Provider("test-n3-no-mid-system-auth")
	disabled := false
	authCalls, payloadCalls, httpCalls := 0, 0, 0
	RegisterProviderModule(ProviderModule{Provider: provider, Models: map[string]Model{"fixture": {
		API: "openai-responses", Compat: &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &disabled},
		Capabilities: ModelCapabilities{ToolAdditions: CapabilitySupported},
	}}, Auth: ProviderAuth{ResolveAuthorization: func(Provider, AuthConfig, *http.Client, context.Context) (string, error) {
		authCalls++
		return "fixture", nil
	}}})
	model := *GetModel(provider, "fixture")
	result := Complete(model, anchorContext(), ProviderStreamOptions{
		PreviousResponseID: "prior_with_lookup",
		OnPayload:          func(any, Model) any { payloadCalls++; return nil },
		HTTPClient:         &http.Client{Transport: samplingRoundTripper(func(*http.Request) (*http.Response, error) { httpCalls++; return nil, fmt.Errorf("unexpected request") })},
	})
	if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "full history") || authCalls != 0 || payloadCalls != 0 || httpCalls != 0 {
		t.Fatalf("reason=%s error=%s auth=%d payload=%d http=%d", result.StopReason, result.ErrorMessage, authCalls, payloadCalls, httpCalls)
	}
}

func anchorContext() Context {
	return Context{Messages: []Message{
		SystemMessage{Content: "base policy", ToolsAdded: []Tool{anchorFunction("initial")}},
		UserMessage{Content: "before lookup loaded"},
		SystemMessage{ToolsAdded: []Tool{anchorFunction("lookup")}},
		UserMessage{Content: "after lookup loaded"},
	}}
}

func countToolAnchors(input []map[string]any) int {
	count := 0
	for _, item := range input {
		if item["type"] == "additional_tools" {
			count++
		}
	}
	return count
}

func TestResponsesToolAnchorsKeepDeclarationPositionAndPrefix(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	ctx := anchorContext()
	before := mustBuildOpenAIResponsesRequest(t, nil, model, Context{Messages: ctx.Messages[:2]}, ProviderStreamOptions{})
	after := mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
	if !reflect.DeepEqual(before.Tools, after.Tools) || !reflect.DeepEqual(before.Input, after.Input[:len(before.Input)]) {
		t.Fatalf("append-only change rewrote request prefix: before=%+v after=%+v", before, after)
	}
	if len(after.Tools) != 1 || after.Tools[0]["name"] != "initial" || countToolAnchors(after.Input) != 1 {
		t.Fatalf("wrong declaration split: %+v", after)
	}
	anchor := after.Input[2]
	if anchor["role"] != "developer" || anchor["tools"].([]map[string]any)[0]["name"] != "lookup" {
		t.Fatalf("anchor=%v", anchor)
	}
	// A second append must leave the entire first request prefix byte-identical.
	ctx.Messages = append(ctx.Messages, SystemMessage{ToolsAdded: []Tool{anchorFunction("later")}}, UserMessage{Content: "use later"})
	later := mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
	if mustJSON(after.Input) != mustJSON(later.Input[:len(after.Input)]) || mustJSON(after.Tools) != mustJSON(later.Tools) {
		t.Fatal("second addition changed encoded cache prefix")
	}
	wire, err := SerializeContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DeserializeContext(wire)
	if err != nil || mustJSON(later) != mustJSON(mustBuildOpenAIResponsesRequest(t, nil, model, restored, ProviderStreamOptions{})) {
		t.Fatalf("restored declaration positions changed: %v", err)
	}
}

func TestResponsesToolAnchorsFoldNonAdditiveHistory(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	for _, kind := range []string{"remove", "changed same name", "identical same name", "remove then readd"} {
		t.Run(kind, func(t *testing.T) {
			ctx := anchorContext()
			changed := anchorFunction("lookup")
			if kind == "changed same name" {
				changed.Description = "new schema revision"
				changed.Parameters = map[string]any{"type": "object", "properties": map[string]any{"revision": map[string]any{"type": "integer"}}}
			}
			switch kind {
			case "remove":
				ctx.Messages = append(ctx.Messages, SystemMessage{ToolsRemoved: []ToolReference{{Name: "lookup"}}})
			case "remove then readd":
				ctx.Messages = append(ctx.Messages, SystemMessage{ToolsRemoved: []ToolReference{{Name: "lookup"}}, ToolsAdded: []Tool{changed}})
			default:
				ctx.Messages = append(ctx.Messages, SystemMessage{ToolsAdded: []Tool{changed}})
			}
			request := mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
			want := convertOpenAIResponsesTools(nil, currentContextTools(ctx))
			if countToolAnchors(request.Input) != 0 || !reflect.DeepEqual(request.Tools, want) {
				t.Fatalf("nonadditive history escaped fallback: %+v", request)
			}
		})
	}
}

func TestResponsesToolAnchorsRequireExactCapabilities(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	supported, _ := LookupModelCapabilities(model.Provider, model.ID)
	if supported.Capabilities.ToolAdditions != CapabilitySupported {
		t.Fatal(supported)
	}
	unknown, wrongAPI, noMid := model, model, model
	unknown.ID += "-unknown"
	wrongAPI.API = "openai-completions"
	disabled := false
	noMid.Compat = &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &disabled}
	provider := Provider("test-n3-provider-default")
	RegisterProviderModule(ProviderModule{Provider: provider, ModelCapabilities: ModelCapabilities{ToolAdditions: CapabilitySupported}, Models: map[string]Model{"fixture": {API: model.API, Compat: model.Compat}}})
	models := []Model{unknown, wrongAPI, noMid, *GetModel("openai", "gpt-5.4"), *GetModel("openai-codex", "gpt-6-astra"), *GetModel(provider, "fixture")}
	for _, other := range models {
		other.Capabilities.ToolAdditions = CapabilitySupported // cannot grant support per request
		request := mustBuildOpenAIResponsesRequest(t, nil, other, anchorContext(), ProviderStreamOptions{})
		if countToolAnchors(request.Input) != 0 || len(request.Tools) != 2 {
			t.Fatalf("capability leaked to %s/%s/%s: %+v", other.Provider, other.ID, other.API, request)
		}
	}
	codex := buildOpenAICodexRequest(nil, *GetModel("openai-codex", "gpt-6-astra"), anchorContext(), ProviderStreamOptions{})
	if countToolAnchors(codex.Input) != 0 || len(codex.Tools) != 2 {
		t.Fatalf("Codex unexpectedly anchors: %+v", codex)
	}
}

func TestResponsesToolAnchorsCustomAndBoundsFallback(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	ctx := anchorContext()
	first := ctx.Messages[0].(SystemMessage)
	first.ToolsAdded = append(first.ToolsAdded, samplingTool("text", "require"))
	ctx.Messages[0] = first
	request := mustBuildOpenAIResponsesRequest(t, map[string]string{"code": "code"}, model, ctx, ProviderStreamOptions{})
	if countToolAnchors(request.Input) != 1 || request.Tools[1]["type"] != "custom" {
		t.Fatalf("static custom plus dynamic function changed: %+v", request)
	}
	ctx = anchorContext()
	ctx.Messages = append(ctx.Messages, SystemMessage{ToolsAdded: []Tool{samplingTool("text", "require")}})
	request = mustBuildOpenAIResponsesRequest(t, map[string]string{"code": "code"}, model, ctx, ProviderStreamOptions{})
	if countToolAnchors(request.Input) != 0 || request.Tools[2]["type"] != "custom" {
		t.Fatalf("dynamic custom did not use N2 fallback: %+v", request)
	}
	for _, mode := range []string{"tools", "items", "single schema", "aggregate"} {
		t.Run(mode, func(t *testing.T) {
			ctx := anchorContext()
			count := maxAnchoredTools + 1
			if mode == "items" {
				count = maxToolAnchors + 1
			}
			if mode == "single schema" {
				count = 1
			}
			if mode == "aggregate" {
				count = 24
			}
			var tools []Tool
			for i := 0; i < count; i++ {
				tool := anchorFunction(fmt.Sprintf("more_%d", i))
				if mode == "single schema" {
					tool.Description = strings.Repeat("x", maxAnchoredToolBytes)
				}
				if mode == "aggregate" {
					tool.Description = strings.Repeat("x", 48<<10)
				}
				if mode == "items" {
					ctx.Messages = append(ctx.Messages, SystemMessage{ToolsAdded: []Tool{tool}})
				} else {
					tools = append(tools, tool)
				}
			}
			ctx.Messages = append(ctx.Messages, SystemMessage{ToolsAdded: tools})
			request := mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
			if countToolAnchors(request.Input) != 0 || len(request.Tools) != count+2 {
				t.Fatalf("native bounds fallback lost declarations: anchors=%d tools=%d", countToolAnchors(request.Input), len(request.Tools))
			}
		})
	}
}

func TestResponsesToolAnchorNamespaceReplay(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	ctx := anchorContext()
	ctx.Messages = append(ctx.Messages,
		AssistantMessage{Provider: model.Provider, API: model.API, Model: model.ID, StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "call_lookup|fc_lookup", Name: "lookup", Namespace: "lookup", Arguments: map[string]any{}}}},
		ToolResultMessage{ToolCallID: "call_lookup|fc_lookup", ToolName: "lookup", Content: []ContentBlock{TextContent{Text: "result"}}},
	)
	for _, mode := range []string{"native", "model switch", "removed", "unknown namespace"} {
		t.Run(mode, func(t *testing.T) {
			copy := contextFromTranscript(NormalizeContext(ctx))
			target := model
			if mode == "model switch" {
				target = *GetModel("openai", "gpt-5.4")
			}
			if mode == "removed" {
				copy.Messages = append(copy.Messages, SystemMessage{ToolsRemoved: []ToolReference{{Name: "lookup"}}})
			}
			if mode == "unknown namespace" {
				message := copy.Messages[4].(AssistantMessage)
				call := message.Content[0].(ToolCall)
				call.Namespace = "unrelated"
				message.Content[0] = call
				copy.Messages[4] = message
			}
			wire, err := SerializeContext(copy)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := DeserializeContext(wire)
			if err != nil {
				t.Fatal(err)
			}
			request := mustBuildOpenAIResponsesRequest(t, nil, target, restored, ProviderStreamOptions{})
			found := false
			for _, item := range request.Input {
				if item["type"] == "function_call" {
					found = true
					if (item["namespace"] == "lookup") != (mode == "native") || item["call_id"] != "call_lookup" {
						t.Fatalf("mode=%s call=%v", mode, item)
					}
					if (item["id"] == "fc_lookup") != (mode == "native") {
						t.Fatalf("incompatible namespace kept provider item identity: mode=%s call=%v", mode, item)
					}
				}
			}
			if !found || request.Input[len(request.Input)-1]["call_id"] != "call_lookup" {
				t.Fatal(request.Input)
			}
		})
	}
}

func TestResponsesToolAnchorNamespaceCannotAliasAnotherSurface(t *testing.T) {
	for _, namespace := range []string{"", "lookup", "unrelated", "model is at capacity"} {
		t.Run(namespace, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				item := map[string]any{"type": "function_call", "id": "fc_lookup", "call_id": "call_lookup", "name": "lookup", "namespace": namespace, "arguments": "{}", "status": "completed"}
				fmt.Fprint(w, buildOpenAICodexSSE(customTerminal(item)))
			}))
			defer server.Close()
			model := *GetModel("openai", "gpt-6-astra")
			model.BaseURL = server.URL
			result := CompleteSimple(model, anchorContext(), SimpleStreamOptions{APIKey: "fixture", MaxRetries: 2, MaxRetryDelay: 1})
			valid := namespace == "" || namespace == "lookup"
			if (result.StopReason == StopReasonToolUse) != valid || requests.Load() != 1 {
				t.Fatalf("namespace=%q requests=%d result=%+v", namespace, requests.Load(), result)
			}
			if valid && result.Content[0].(ToolCall).Namespace != namespace {
				t.Fatal(result.Content)
			}
		})
	}
}

func TestResponsesToolAnchorPlanDoesNotRequeryCatalog(t *testing.T) {
	provider := Provider("test-n3-plan-snapshot")
	enabled := true
	module := normalizeProviderModule(provider, ProviderModule{Provider: provider, Models: map[string]Model{"fixture": {API: "openai-responses", Compat: &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &enabled}, Capabilities: ModelCapabilities{ToolAdditions: CapabilitySupported}}}})
	RegisterProviderModule(module)
	model := *GetModel(provider, "fixture")
	ctx := anchorContext()
	plan := resolveOpenAIResponsesToolPlan(nil, model, ctx)
	changed := module.Models[model.ID]
	changed.Capabilities.ToolAdditions = CapabilityUnsupported
	module.Models[model.ID] = changed
	if !providerRegistry.Replace(provider, &module) {
		t.Fatal("replace catalog")
	}
	input := convertOpenAIResponsesMessages(plan, nil, model, ctx, true)
	if !plan.Anchor || countToolAnchors(input) != 1 || resolveOpenAIResponsesToolPlan(nil, model, ctx).Anchor {
		t.Fatal("resolved request changed, or next request ignored catalog change")
	}
	// Verify the encoder still emits ordinary JSON, with no executable metadata.
	if _, err := json.Marshal(input); err != nil {
		t.Fatal(err)
	}
}
