package pigo

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCodexWebSocketHandshakeHTTPStatus(t *testing.T) {
	for _, status := range []int{403, 502, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(status) }))
			defer server.Close()
			model := GetModel("openai-codex", "gpt-5.5")
			model.BaseURL = server.URL
			response := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{APIKey: makeOpenAICodexToken("fixture"), Transport: TransportWebSocket})
			if response.StopReason != StopReasonError {
				t.Fatalf("unexpected success %+v", response)
			}
			got, ok := ProviderHTTPStatusOf(response)
			if status == 200 {
				if ok {
					t.Fatalf("successful HTTP response mislabeled %d", got)
				}
			} else if !ok || got != status {
				t.Fatalf("want %d got %d %v", status, got, ok)
			}
			if calls.Load() != 1 {
				t.Fatalf("unexpected retries %d", calls.Load())
			}
			data, err := json.Marshal(Context{Messages: []Message{cloneAssistantMessage(response)}})
			if err != nil {
				t.Fatal(err)
			}
			var restored Context
			if err = json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			restoredStatus, restoredOK := ProviderHTTPStatusOf(restored.Messages[0].(AssistantMessage))
			if got != restoredStatus || ok != restoredOK {
				t.Fatal("status lost")
			}
		})
	}
}

func TestCodexHandshakeStatusPreservesErrorIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	_, err := connectOpenAICodexWebSocket(websocket.Dialer{}, "ws"+strings.TrimPrefix(server.URL, "http"), nil, t.Context(), nil)
	if !errors.Is(err, websocket.ErrBadHandshake) {
		t.Fatalf("lost handshake cause %v", err)
	}
	if got := recordedProviderHTTPStatus(err); got != 403 {
		t.Fatalf("status %d", got)
	}
}
