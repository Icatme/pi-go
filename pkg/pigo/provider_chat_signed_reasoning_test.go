package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatReasoningSignatureOnlyAndSeparateBlocks(t *testing.T) {
	for _, two := range []bool{false, true} {
		t.Run(fmt.Sprint(two), func(t *testing.T) {
			details := []string{`{"type":"reasoning.text","text":"first","index":0,"format":"anthropic-claude-v1"}`, `{"type":"reasoning.text","signature":"sig_one","index":0,"format":"anthropic-claude-v1"}`}
			if two {
				details = append(details, `{"type":"reasoning.text","text":"second","index":0,"format":"anthropic-claude-v1"}`, `{"type":"reasoning.text","signature":"sig_two","index":0,"format":"anthropic-claude-v1"}`)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, detail := range details {
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning_details\":[%s]}}]}\n\n", detail)
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
			}))
			defer server.Close()
			result := Complete(Model{ID: "fixture", Provider: "openrouter", API: "openai-completions", BaseURL: server.URL}, Context{}, ProviderStreamOptions{APIKey: "fixture"})
			if result.StopReason != StopReasonStop || len(result.Content) != 2 {
				t.Fatalf("%+v", result)
			}
			thinking := result.Content[0].(ThinkingContent)
			var got []map[string]any
			if err := json.Unmarshal([]byte(thinking.ThinkingSignature), &got); err != nil {
				t.Fatal(err)
			}
			want := 1
			if two {
				want = 2
			}
			if len(got) != want {
				t.Fatalf("details=%v", got)
			}

			request := buildOpenAICompletionsRequest(Model{ID: "fixture", Provider: "openrouter", API: "openai-completions"}, Context{Messages: []Message{result}}, ProviderStreamOptions{})
			if len(request.Messages) != 1 || len(request.Messages[0].ReasoningDetails) != want {
				t.Fatalf("replay=%+v", request.Messages)
			}
			for i := range got {
				word, sig := "first", "sig_one"
				if i == 1 {
					word, sig = "second", "sig_two"
				}
				var replay map[string]any
				if err := json.Unmarshal(request.Messages[0].ReasoningDetails[i], &replay); err != nil {
					t.Fatal(err)
				}
				if replay["text"] != word || replay["signature"] != sig {
					t.Fatal("replay changed", replay)
				}
				if got[i]["text"] != word || got[i]["signature"] != sig {
					t.Fatalf("details=%v", got)
				}
			}
		})
	}
}

func TestChatReasoningSignatureMergeBoundaries(t *testing.T) {
	parse := func(s string) map[string]json.RawMessage {
		return parseOpenAICompletionsStreamingReasoningDetail([]byte(s))
	}
	previous := parse(`{"type":"reasoning.text","text":"one","id":"a","format":"fmt","index":0}`)
	for _, tc := range []struct {
		raw   string
		merge bool
	}{
		{`{"type":"reasoning.text","signature":"sig","id":"a","index":0}`, true},
		{`{"type":"reasoning.text","text":"next","id":"b","index":0}`, false},
		{`{"type":"reasoning.text","signature":"sig","id":"b","index":0}`, false},
		{`{"type":"reasoning.text","signature":"sig","format":"other","index":0}`, false},
	} {
		next := parse(tc.raw)
		if next == nil || canMergeOpenAICompletionsReasoningDetail(previous, next, "reasoning.text") != tc.merge {
			t.Fatal(tc)
		}
	}
	signed := parse(`{"type":"reasoning.text","text":"one","signature":"sig"}`)
	if canMergeOpenAICompletionsReasoningDetail(signed, previous, "reasoning.text") {
		t.Fatal("signed block reopened")
	}
	for _, raw := range []string{`{"type":"reasoning.text","text":42,"signature":"sig"}`, `{"type":"reasoning.text","signature":42}`, `{"type":"reasoning.text"}`, `{"type":"unknown","signature":"sig"}`, `{"type":"reasoning.text","signature":""}`} {
		if parse(raw) != nil {
			t.Fatal("invalid accepted", raw)
		}
	}
	signatureOnly := `{"type":"reasoning.text","signature":"sig","text":null,"index":0}`
	if parse(signatureOnly) == nil {
		t.Fatal("stream null text rejected")
	}
	if parseOpenAICompletionsReasoningDetail([]byte(signatureOnly)) != nil {
		t.Fatal("persisted validation relaxed")
	}
}
