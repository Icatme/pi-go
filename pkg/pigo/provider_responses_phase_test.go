package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponsesAssistantPhaseRoundTrip(t *testing.T) {
	for _, api := range []API{"openai-responses", "openai-codex-responses"} {
		for _, phase := range []any{"commentary", "final_answer", "", "unknown", nil} {
			for _, terminalOnly := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%v/%v", api, terminalOnly, phase), func(t *testing.T) {
					item := map[string]any{"type": "message", "id": "msg_native", "role": "assistant", "phase": phase, "content": []map[string]any{{"type": "output_text", "text": "Working"}}}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						if !terminalOnly {
							event, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "item": item})
							fmt.Fprintf(w, "data: %s\n\n", event)
						}
						event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_test", "status": "completed", "output": []any{item}}})
						fmt.Fprintf(w, "data: %s\n\n", event)
					}))
					defer server.Close()
					model := Model{ID: "fixture", Provider: "openai", API: api, BaseURL: server.URL}
					result := Complete(model, Context{}, ProviderStreamOptions{APIKey: makeOpenAICodexToken("acc_test"), Transport: TransportSSE})
					if result.StopReason != StopReasonStop || len(result.Content) != 1 {
						t.Fatalf("%+v", result)
					}
					ctx := Context{Messages: []Message{result}}
					raw, err := SerializeContext(ctx)
					if err != nil {
						t.Fatal(err)
					}
					ctx, err = DeserializeContext(raw)
					if err != nil {
						t.Fatal(err)
					}
					// The shared converter is used by both builders; observe serialized wire.
					var request any
					if api == "openai-responses" {
						request, err = buildOpenAIResponsesRequest(nil, model, ctx, ProviderStreamOptions{})
					} else {
						request = buildOpenAICodexRequest(nil, model, ctx, ProviderStreamOptions{})
					}
					if err != nil {
						t.Fatal(err)
					}
					wire, _ := json.Marshal(request)
					var decoded struct {
						Input []map[string]any `json:"input"`
					}
					json.Unmarshal(wire, &decoded)
					want, _ := phase.(string)
					want = openAIResponsesAssistantPhase(want)
					got, _ := decoded.Input[0]["phase"].(string)
					if len(decoded.Input) != 1 || got != want {
						t.Fatalf("phase lost %s", wire)
					}
				})
			}
		}
	}

}

func TestResponsesAssistantPhaseReplayBoundary(t *testing.T) {
	model := Model{ID: "fixture", Provider: "openai", API: "openai-responses"}
	for _, phase := range []string{"commentary", "final_answer", "", "unknown"} {
		for _, identity := range []string{"native", "provider", "api", "model"} {
			t.Run(phase+"/"+identity, func(t *testing.T) {
				message := AssistantMessage{Provider: model.Provider, API: model.API, Model: model.ID, Content: []ContentBlock{TextContent{Text: "answer", TextSignature: encodeOpenAIResponsesTextSignatureV1("msg_native", phase)}}}
				switch identity {
				case "provider":
					message.Provider = "other"
				case "api":
					message.API = "other"
				case "model":
					message.Model = "other"
				}
				request, err := buildOpenAIResponsesRequest(nil, model, Context{Messages: []Message{message}}, ProviderStreamOptions{})
				if err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(request)
				var wire struct {
					Input []map[string]any `json:"input"`
				}
				if err = json.Unmarshal(data, &wire); err != nil {
					t.Fatal(err)
				}
				want := openAIResponsesAssistantPhase(phase)
				if identity != "native" {
					want = ""
				}
				got, _ := wire.Input[0]["phase"].(string)
				if got != want {
					t.Fatalf("phase=%q want %q wire=%s", got, want, data)
				}
				if wire.Input[0]["id"] != "msg_0" {
					t.Fatalf("changed synthetic ID: %s", data)
				}
			})
		}
	}
}

func TestResponsesAssistantPhaseLegacySignatures(t *testing.T) {
	model := Model{ID: "fixture", Provider: "openai", API: "openai-responses"}
	for _, signature := range []string{"", "msg_legacy", `{`, `{"v":0,"id":"msg_old","phase":"commentary"}`, `{"v":1,"id":"msg_old"}`} {
		message := AssistantMessage{Provider: model.Provider, API: model.API, Model: model.ID, Content: []ContentBlock{TextContent{Text: "answer", TextSignature: signature}}}
		ctx := Context{Messages: []Message{message}}
		before, err := SerializeContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		request, err := buildOpenAIResponsesRequest(nil, model, ctx, ProviderStreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(request)
		var decoded struct {
			Input []map[string]any `json:"input"`
		}
		if err = json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, ok := decoded.Input[0]["phase"]; ok {
			t.Fatalf("invented phase %s", wire)
		}
		after, err := SerializeContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("source history mutated")
		}
	}
}
