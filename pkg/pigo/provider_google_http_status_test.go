package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGoogleTerminalHTTPStatus(t *testing.T) {
	for _, status := range []int{403, 503, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if status == 200 {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"error\":{\"code\":503,\"message\":\"unavailable\"}}\n\n")
				} else {
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":{"message":"opaque"}}`)
				}
			}))
			defer server.Close()
			model := Model{ID: "fixture", API: "google-generative-ai", Provider: "google", BaseURL: server.URL}
			msg := streamGoogle(model, Context{Messages: []Message{UserMessage{Content: "hi"}}}, ProviderStreamOptions{APIKey: "test"}).Result()
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
