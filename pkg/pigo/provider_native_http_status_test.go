package pigo

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNativeTerminalHTTPStatus(t *testing.T) {
	for _, api := range []string{"anthropic-messages", "openai-responses"} {
		for _, status := range []int{403, 503, 200} {
			t.Run(api+fmt.Sprint(status), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if status == 200 {
						w.Header().Set("Content-Type", "text/event-stream")
						if api == "anthropic-messages" {
							fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"503 unavailable\"}}\n\n")
						} else {
							fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"code\":\"503\",\"message\":\"unavailable\"}\n\n")
						}
					} else {
						w.WriteHeader(status)
						fmt.Fprint(w, `{"error":{"message":"opaque"}}`)
					}
				}))
				defer server.Close()
				model := Model{ID: "fixture", API: API(api), Provider: "fixture", BaseURL: server.URL}
				msg := Stream(model, Context{Messages: []Message{UserMessage{Content: "hi"}}}, ProviderStreamOptions{APIKey: "test"}).Result()
				if msg.StopReason != StopReasonError {
					t.Fatalf("stop reason %s", msg.StopReason)
				}
				got, ok := ProviderHTTPStatusOf(msg)
				if status == 200 {
					if ok {
						t.Fatalf("inferred status %d from stream payload", got)
					}
				} else if !ok || got != status {
					t.Fatalf("want %d got %d %v", status, got, ok)
				}
				if calls.Load() != 1 {
					t.Fatalf("requests %d", calls.Load())
				}
				data, err := json.Marshal(Context{Messages: []Message{cloneAssistantMessage(msg)}})
				if err != nil {
					t.Fatal(err)
				}
				var restored Context
				if err = json.Unmarshal(data, &restored); err != nil {
					t.Fatal(err)
				}
				restoredStatus, restoredOK := ProviderHTTPStatusOf(restored.Messages[0].(AssistantMessage))
				if restoredStatus != got || restoredOK != ok {
					t.Fatal("status lost on roundtrip")
				}
			})
		}
	}
}

func TestResponsesTerminalStatusUsesFinalAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := 503
		if calls.Add(1) > 1 {
			status = 403
		}
		w.WriteHeader(status)
		fmt.Fprint(w, `{"error":{"message":"opaque","type":"server_error"}}`)
	}))
	defer server.Close()
	model := Model{ID: "fixture", API: "openai-responses", Provider: "fixture", BaseURL: server.URL}
	msg := Stream(model, Context{}, ProviderStreamOptions{APIKey: "test", MaxRetries: 1}).Result()
	if got, ok := ProviderHTTPStatusOf(msg); !ok || got != 403 {
		t.Fatalf("final status %d %v", got, ok)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests %d", calls.Load())
	}
	if failure, ok := ProviderFailureOf(msg); !ok || failure.Kind != ProviderFailureUpstream || failure.Reason != "http_error" {
		t.Fatalf("changed classification %+v", failure)
	}
}

func TestAnthropicHTTPStatusWithBodyReadFailure(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: observationRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Status: "503 Service Unavailable", Header: make(http.Header), Body: io.NopCloser(io.MultiReader(strings.NewReader(`{"error":{"message":"opaque"}}`), failureReader{io.ErrUnexpectedEOF}))}, nil
	})}
	msg := Stream(Model{ID: "fixture", API: "anthropic-messages", Provider: "fixture", BaseURL: "https://provider.invalid"}, Context{}, ProviderStreamOptions{APIKey: "test", HTTPClient: client}).Result()
	if got, ok := ProviderHTTPStatusOf(msg); !ok || got != 503 {
		t.Fatalf("status %d %v", got, ok)
	}
	if failure, ok := ProviderFailureOf(msg); !ok || failure.Reason != "body_read" {
		t.Fatalf("classification %+v", failure)
	}
	if calls != 1 || !strings.Contains(msg.ErrorMessage, io.ErrUnexpectedEOF.Error()) {
		t.Fatalf("changed read error %q calls %d", msg.ErrorMessage, calls)
	}
}
