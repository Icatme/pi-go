package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompletionsGoogleToolSignatureRoundTrip(t *testing.T) {
	for _, fullMessage := range []bool{false, true} {
		t.Run(fmt.Sprintf("messageFallback=%v", fullMessage), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if fullMessage {
					fmt.Fprint(w, `data: {"choices":[{"message":{"tool_calls":[{"id":"a","function":{"name":"lookup","arguments":"{\"x\":1}"},"extra_content":{"google":{"thought_signature":"opaque+/=="}}},{"id":"b","function":{"name":"lookup","arguments":"{\"x\":2}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
				} else {
					for _, calls := range []string{
						`[{"index":0,"id":"a","function":{"name":"lookup","arguments":"{\"x\":"}},{"index":1,"id":"b","function":{"name":"lookup","arguments":"{\"x\":2}"}}]`,
						`[{"index":0,"extra_content":{"google":{"thought_signature":"opaque+/=="}}}]`,
						`[{"index":0,"function":{"arguments":"1}"}}]`,
					} {
						fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":%s}}]}\n\n", calls)
					}
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			model := Model{ID: "gemini-test", Provider: "custom-gateway", API: "openai-completions", BaseURL: server.URL}
			stream := streamOpenAICompletions(model, Context{Messages: []Message{UserMessage{Content: "lookup"}}}, ProviderStreamOptions{APIKey: "test"})
			events := collectAssistantMessageEvents(stream.Events())
			result := stream.Result()
			if result.StopReason != StopReasonToolUse {
				t.Fatalf("stop=%s error=%s", result.StopReason, result.ErrorMessage)
			}
			if len(result.Content) != 2 {
				t.Fatalf("content=%#v", result.Content)
			}
			first := result.Content[0].(ToolCall)
			second := result.Content[1].(ToolCall)
			if first.ThoughtSignature != "opaque+/==" || second.ThoughtSignature != "" {
				t.Fatalf("signatures %#v %#v", first, second)
			}
			for _, event := range events {
				if event.Type == AssistantMessageEventToolCallEnd && event.ToolCall.ID == "a" && event.ToolCall.ThoughtSignature != first.ThoughtSignature {
					t.Fatal("end event lost signature")
				}
			}
			data, err := SerializeContext(Context{Messages: []Message{result, ToolResultMessage{ToolCallID: "a", ToolName: "lookup", Content: []ContentBlock{TextContent{Text: "one"}}}, ToolResultMessage{ToolCallID: "b", ToolName: "lookup", Content: []ContentBlock{TextContent{Text: "two"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			restored, err := DeserializeContext(data)
			if err != nil {
				t.Fatal(err)
			}
			for targetIndex, target := range []Model{model, {ID: "other", Provider: model.Provider, API: model.API}, {ID: model.ID, Provider: "other", API: model.API}, {ID: model.ID, Provider: model.Provider, API: "other"}} {
				messages := convertOpenAICompletionsMessages(target, restored, resolvedOpenAICompletionsCompat{})
				wire, err := json.Marshal(messages)
				if err != nil {
					t.Fatal(err)
				}
				if targetIndex == 0 {
					var parsed []map[string]any
					if err = json.Unmarshal(wire, &parsed); err != nil {
						t.Fatal(err)
					}
					calls := parsed[0]["tool_calls"].([]any)
					a := calls[0].(map[string]any)
					b := calls[1].(map[string]any)
					sig := a["extra_content"].(map[string]any)["google"].(map[string]any)["thought_signature"]
					if sig != "opaque+/==" {
						t.Fatalf("wire %s", wire)
					}
					if _, ok := b["extra_content"]; ok {
						t.Fatalf("fabricated signature: %s", wire)
					}
				} else if strings.Contains(string(wire), "extra_content") {
					t.Fatalf("cross model/provider/API leak %s", wire)
				}
			}
		})
	}
}

func TestOpenAICompletionsGoogleSignatureKeepsReasoningDetails(t *testing.T) {
	model := Model{ID: "gemini-test", Provider: "gateway", API: "openai-completions"}
	msg := AssistantMessage{Model: model.ID, Provider: model.Provider, API: model.API, Content: []ContentBlock{
		ThinkingContent{ThinkingSignature: `[{"type":"reasoning.encrypted","data":"router-opaque"}]`},
		ToolCall{ID: "call1", Name: "lookup", Arguments: map[string]any{}, ThoughtSignature: "google-opaque"},
	}}
	out, ok := openAICompletionsAssistantMessage(model, msg, resolvedOpenAICompletionsCompat{})
	if !ok || len(out.ReasoningDetails) != 1 || !strings.Contains(string(out.ReasoningDetails[0]), "router-opaque") {
		t.Fatalf("reasoning lost: %#v", out)
	}
	if out.ToolCalls[0].ExtraContent.Google.ThoughtSignature != "google-opaque" {
		t.Fatal("tool signature lost")
	}
}
