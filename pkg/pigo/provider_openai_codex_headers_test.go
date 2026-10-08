package pigo

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

func TestCodexIdentityHeaderPrecedence(t *testing.T) {
	for _, transport := range []Transport{TransportSSE, TransportWebSocket} {
		for _, test := range []struct {
			name                  string
			model, caller         map[string]string
			originator, userAgent string
		}{
			{"defaults", nil, nil, "pi", openAICodexUserAgent()},
			{"model", map[string]string{"Originator": "model-app", "User-Agent": "model/1"}, nil, "model-app", "model/1"},
			{"caller", map[string]string{"Originator": "model-app", "User-Agent": "model/1"}, map[string]string{"ORIGINATOR": "caller-app", "user-agent": "caller/1", "Authorization": "ignored", "ChatGPT-Account-ID": "ignored"}, "caller-app", "caller/1"},
		} {
			t.Run(string(transport)+"/"+test.name, func(t *testing.T) {
				token := makeOpenAICodexToken("test-account")
				terminal := map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_headers", "status": "completed", "output": []any{}}}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					for key, want := range map[string]string{"Originator": test.originator, "User-Agent": test.userAgent, "Authorization": "Bearer " + token, "ChatGPT-Account-ID": "test-account"} {
						if got := r.Header.Get(key); got != want {
							t.Errorf("%s = %q, want %q", key, got, want)
						}
					}
					if transport == TransportWebSocket {
						upgrader := websocket.Upgrader{}
						conn, err := upgrader.Upgrade(w, r, nil)
						if err != nil {
							t.Errorf("upgrade: %v", err)
							return
						}
						defer conn.Close()
						var payload map[string]any
						if err := conn.ReadJSON(&payload); err != nil {
							t.Errorf("read: %v", err)
							return
						}
						if err := conn.WriteJSON(terminal); err != nil {
							t.Errorf("write: %v", err)
						}
					} else {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte(buildOpenAICodexSSE(terminal)))
					}
				}))
				defer server.Close()
				t.Cleanup(clearOpenAICodexWebSocketSessionCache)
				model := *GetModel("openai-codex", "gpt-5.5")
				model.BaseURL, model.Headers = server.URL, test.model
				beforeModel, beforeCaller := cloneStringMap(test.model), cloneStringMap(test.caller)
				result := Complete(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, ProviderStreamOptions{APIKey: token, Transport: transport, Headers: test.caller})
				if result.StopReason != StopReasonStop {
					t.Fatalf("stream failed: %+v", result)
				}
				if !reflect.DeepEqual(test.model, beforeModel) || !reflect.DeepEqual(test.caller, beforeCaller) {
					t.Fatal("header maps were mutated")
				}
			})
		}
	}
}

func TestCodexWebSocketReopensWhenIdentityHeadersChange(t *testing.T) {
	clearOpenAICodexWebSocketSessionCache()
	t.Cleanup(clearOpenAICodexWebSocketSessionCache)
	var mu sync.Mutex
	var identities []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := r.Header.Get("Originator") + "/" + r.Header.Get("User-Agent")
		mu.Lock()
		identities = append(identities, identity)
		mu.Unlock()
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			var payload map[string]any
			if err := conn.ReadJSON(&payload); err != nil {
				return
			}
			if err := conn.WriteJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": "headers", "status": "completed", "output": []any{}}}); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	model := *GetModel("openai-codex", "gpt-5.5")
	model.BaseURL = server.URL
	for _, headers := range []map[string]string{
		{"originator": "a", "User-Agent": "one"},
		{"ORIGINATOR": "a", "user-agent": "one"},
		{"originator": "b", "User-Agent": "one"},
		{"originator": "b", "User-Agent": "two"},
	} {
		result := Complete(model, Context{}, ProviderStreamOptions{
			APIKey: makeOpenAICodexToken("same-account"), SessionID: "same-session",
			Transport: TransportWebSocket, Headers: headers,
		})
		if result.StopReason != StopReasonStop {
			t.Fatalf("stream: %+v", result)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(identities, []string{"a/one", "b/one", "b/two"}) {
		t.Fatalf("identity changes must renegotiate; equivalent casing must reuse: %v", identities)
	}
}
