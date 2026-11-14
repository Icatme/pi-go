package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponsesLatePhaseRoundTrip(t *testing.T) {
	for _, api := range []API{"openai-responses", "openai-codex-responses"} {
		for _, phase := range []any{"commentary", "final_answer", "", "unknown", nil} {
			for _, added := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%v/%v", api, added, phase), func(t *testing.T) {
					item := map[string]any{"type": "message", "id": "msg_native", "role": "assistant", "phase": phase, "content": []map[string]any{{"type": "output_text", "text": "Working"}}}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						if added {
							start, _ := json.Marshal(map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "message", "id": "msg_native", "role": "assistant", "content": []any{}}})
							fmt.Fprintf(w, "data: %s\n\n", start)
						}
						{
							delete(item, "phase")
							event, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "item": item})
							fmt.Fprintf(w, "data: %s\n\n", event)
						}
						item["phase"] = phase
						event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_test", "status": "completed", "output": []any{item}}})
						fmt.Fprintf(w, "data: %s\n\n", event)
					}))
					defer server.Close()
					model := Model{ID: "fixture", Provider: "openai", API: api, BaseURL: server.URL}
					stream := Stream(model, Context{}, ProviderStreamOptions{APIKey: makeOpenAICodexToken("acc_test"), Transport: TransportSSE})
					ends, done := 0, 0
					for event := range stream.Events() {
						if event.Type == AssistantMessageEventTextEnd {
							ends++
							var sig TextSignatureV1
							json.Unmarshal([]byte(event.Partial.Content[0].(TextContent).TextSignature), &sig)
							if sig.Phase != "" {
								t.Fatal("early snapshot enriched")
							}
						}
						if event.Type == AssistantMessageEventDone {
							done++
						}
					}
					result := stream.Result()
					if ends != 1 || done != 1 {
						t.Fatalf("duplicate lifecycle: %d %d", ends, done)
					}
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

func TestResponsesLatePhaseMetadataBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, signature, phase string
		duplicate              bool
		want                   bool
	}{
		{"missing", `{"v":1,"id":"msg","huge":9007199254740993,"opaque":{"x":1}}`, "commentary", false, true},
		{"null", `{"v":1,"id":"msg","phase":null}`, "final_answer", false, true},
		{"empty", `{"v":1,"id":"msg","phase":""}`, "final_answer", false, true},
		{"existing", `{"v":1,"id":"msg","phase":"commentary"}`, "final_answer", false, false},
		{"unknown-existing", `{"v":1,"id":"msg","phase":"future"}`, "final_answer", false, false},
		{"wrong-type", `{"v":1,"id":"msg","phase":1}`, "commentary", false, false},
		{"wrong-version", `{"v":2,"id":"msg"}`, "commentary", false, false},
		{"malformed", `{`, "commentary", false, false},
		{"other-id", `{"v":1,"id":"other"}`, "commentary", false, false},
		{"unknown-terminal", `{"v":1,"id":"msg"}`, "future", false, false},
		{"empty-terminal", `{"v":1,"id":"msg"}`, "", false, false},
		{"ambiguous", `{"v":1,"id":"msg"}`, "commentary", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := TextContent{Text: "unchanged", TextSignature: tc.signature}
			r := AssistantMessage{Content: []ContentBlock{block}}
			if tc.duplicate {
				r.Content = append(r.Content, block)
			}
			old := r.Content
			fillOpenAIResponsesFinalizedPhase(&r, nil, openAIResponsesResponseItem{Type: "message", ID: "msg", Phase: tc.phase})
			got := r.Content[0].(TextContent)
			if got.Text != "unchanged" || old[0].(TextContent).TextSignature != tc.signature {
				t.Fatal("mutated text or old snapshot")
			}
			if !tc.want {
				if got.TextSignature != tc.signature {
					t.Fatalf("unexpected enrichment %s", got.TextSignature)
				}
				return
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(got.TextSignature), &raw); err != nil {
				t.Fatal(err)
			}
			if string(raw["phase"]) != fmt.Sprintf("%q", tc.phase) {
				t.Fatal(got.TextSignature)
			}
			if tc.name == "missing" && (string(raw["huge"]) != "9007199254740993" || string(raw["opaque"]) != "{\"x\":1}") {
				t.Fatal(got.TextSignature)
			}
		})
	}
}

func TestResponsesLatePhaseAmbiguousTerminalIDs(t *testing.T) {
	for _, id := range []string{"msg", ""} {
		block := TextContent{Text: "keep", TextSignature: encodeOpenAIResponsesTextSignatureV1(id, "")}
		response := AssistantMessage{Content: []ContentBlock{block}}
		item := openAIResponsesResponseItem{Type: "message", ID: id, Phase: "commentary"}
		state := &openAIResponsesStreamingState{FinalizedItemKeys: map[string]bool{openAIResponsesItemKey(item): true}}
		items := []openAIResponsesResponseItem{item}
		if id != "" {
			other := item
			other.Phase = "final_answer"
			items = append(items, other)
		}
		if err := emitOpenAIResponsesTerminalOutputIfNeeded(&response, nil, state, openAIResponsesResponse{Output: items}); err != nil {
			t.Fatal(err)
		}
		if response.Content[0].(TextContent).TextSignature != block.TextSignature {
			t.Fatal("guessed ambiguous/empty identity")
		}
	}
}

func TestResponsesLatePhasePrivateIndexBoundary(t *testing.T) {
	for _, index := range []int{-1, 1, 99} {
		response := AssistantMessage{Content: []ContentBlock{TextContent{Text: "keep"}}}
		state := &openAIResponsesStreamingState{FinalizedTextIndexes: map[string]int{"msg": index}}
		fillOpenAIResponsesFinalizedPhase(&response, state, openAIResponsesResponseItem{Type: "message", ID: "msg", Phase: "commentary"})
		if response.Content[0].(TextContent).TextSignature != "" {
			t.Fatal("invalid private index enriched")
		}
	}
}

func TestResponsesLatePhaseMixedIdentityIsAmbiguous(t *testing.T) {
	for _, index := range []int{-1, 0} {
		block := TextContent{Text: "signed", TextSignature: encodeOpenAIResponsesTextSignatureV1("msg", "")}
		response := AssistantMessage{Content: []ContentBlock{TextContent{Text: "done-only"}, block}}
		state := &openAIResponsesStreamingState{FinalizedTextIndexes: map[string]int{"msg": index}}
		fillOpenAIResponsesFinalizedPhase(&response, state, openAIResponsesResponseItem{Type: "message", ID: "msg", Phase: "commentary"})
		if response.Content[1].(TextContent).TextSignature != block.TextSignature || response.Content[0].(TextContent).TextSignature != "" {
			t.Fatal("mixed identity guessed")
		}
	}
}
