package pigo

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// Gateway error codes can arrive inside HTTP 200 SSE responses. Keep the
// evidence, including unknown codes, without turning it into retry authority.
// A nonzero retry budget proves new metadata alone does not grant a replay.
func TestResponsesProviderErrorDetails(t *testing.T) {
	for _, tc := range []struct{ name, event, code, errorType, message string }{
		{"gateway", `{"type":"response.failed","response":{"error":{"code":"upstream_stream_read_error","type":"server_error","message":"Upstream response stream was interrupted"}}}`, "upstream_stream_read_error", "server_error", "Upstream response stream was interrupted"},
		{"code only", `{"type":"response.failed","response":{"error":{"code":"upstream_error"}}}`, "upstream_error", "", "response failed"},
		{"unknown", `{"type":"response.failed","response":{"error":{"code":"future_code","type":"future_type","message":"future failure"}}}`, "future_code", "future_type", "future failure"},
		{"event code", `{"type":"error","code":"server_error","message":"stream disconnected before completion"}`, "server_error", "", "stream disconnected before completion"},
		{"not inferred", `{"type":"error","message":"upstream_error 502"}`, "", "", "upstream_error 502"},
		{"non string code", `{"type":"error","code":502,"message":"failure"}`, "", "", "failure"},
		{"missing error", `{"type":"response.failed","response":{}}`, "", "", "response failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := &http.Client{Transport: observationRoundTripper(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + tc.event + "\n\n"))}, nil
			})}
			model := Model{API: "openai-responses", Provider: "commandcode", ID: "synthetic", BaseURL: "https://provider.invalid/v1"}
			result := Complete(model, Context{}, ProviderStreamOptions{APIKey: "test", HTTPClient: client, RequestContext: t.Context(), MaxRetries: 3})
			want := ProviderFailure{Kind: ProviderFailureUpstream, Reason: "error_event", Message: tc.message, Code: tc.code, Type: tc.errorType}
			if got, ok := ProviderFailureOf(result); !ok || got != want {
				t.Fatalf("failure=%+v want=%+v", got, want)
			}
			if result.StopReason != StopReasonError || calls.Load() != 1 {
				t.Fatalf("stop=%s calls=%d", result.StopReason, calls.Load())
			}
			encoded, err := json.Marshal(Context{Messages: []Message{cloneAssistantMessage(result)}})
			if err != nil {
				t.Fatal(err)
			}
			var restored Context
			if err = json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			if got, ok := ProviderFailureOf(restored.Messages[0].(AssistantMessage)); !ok || got != want {
				t.Fatalf("restored=%+v", got)
			}
		})
	}
}
