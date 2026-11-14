package pigo

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTerminalHTTPStatusRoundtrip(t *testing.T) {
	for _, status := range []int{401, 503} {
		for _, broken := range []bool{false, true} {
			calls := 0
			client := &http.Client{Transport: observationRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				var body io.Reader = strings.NewReader(`{"error":{"message":"opaque"}}`)
				if broken {
					body = io.MultiReader(body, failureReader{io.ErrUnexpectedEOF})
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(body)}, nil
			})}
			model := Model{API: "openai-completions", Provider: "commandcode", ID: "synthetic", BaseURL: "https://provider.invalid/v1"}
			msg := Complete(model, Context{}, ProviderStreamOptions{APIKey: "test", HTTPClient: client, RequestContext: t.Context(), MaxRetries: 0})
			if got, ok := ProviderHTTPStatusOf(msg); !ok || got != status {
				t.Fatalf("status %d broken %v got %d %v", status, broken, got, ok)
			}
			if calls != 1 {
				t.Fatalf("changed retry count %d", calls)
			}
			wantReason := "http_error"
			if broken {
				wantReason = "body_read"
			}
			if f, ok := ProviderFailureOf(msg); !ok || f.Reason != wantReason {
				t.Fatalf("changed failure %+v", f)
			}
			data, err := json.Marshal(Context{Messages: []Message{cloneAssistantMessage(msg)}})
			if err != nil {
				t.Fatal(err)
			}
			var restored Context
			if err = json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			if got, ok := ProviderHTTPStatusOf(restored.Messages[0].(AssistantMessage)); !ok || got != status {
				t.Fatalf("restore %d %v", got, ok)
			}
		}
	}
}

func TestTerminalHTTPStatusMissingOrInvalid(t *testing.T) {
	for _, value := range []any{nil, 200, 0, 600, "503", 503.5, json.Number("503.5")} {
		msg := AssistantMessage{}
		recordProviderFailure(&msg, errors.New("local"))
		msg.Diagnostics[0].Details["http_status"] = value
		if got, ok := ProviderHTTPStatusOf(msg); ok || got != 0 {
			t.Fatalf("accepted %v: %d %v", value, got, ok)
		}
	}
	msg := AssistantMessage{}
	recordProviderFailure(&msg, withProviderErrorDetails(errors.New("stream error"), "rate_limit_error", "error"))
	if _, ok := ProviderHTTPStatusOf(msg); ok {
		t.Fatal("inferred HTTP status from stream code")
	}
}
