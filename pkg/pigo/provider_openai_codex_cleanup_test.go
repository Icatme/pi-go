package pigo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCodexSessionResourceCleanupIsScopedAndReusable(t *testing.T) {
	clearOpenAICodexWebSocketSessionCache()
	defer clearOpenAICodexWebSocketSessionCache()

	previousTTL := getOpenAICodexWebSocketCacheTTL()
	setOpenAICodexWebSocketCacheTTL(time.Minute)
	defer func() {
		setOpenAICodexWebSocketCacheTTL(previousTTL)
	}()

	token := makeOpenAICodexToken("acc_test")
	sessionID := "ws-session-reuse"
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	var (
		mu                sync.Mutex
		upgradeCount      int
		requestCount      int
		providerResponses []ProviderResponse
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected websocket", http.StatusBadRequest)
			return
		}

		mu.Lock()
		upgradeCount++
		mu.Unlock()

		upgradeHeaders := http.Header{}
		upgradeHeaders.Set("x-codex-trace", "ws-upgrade")
		conn, err := upgrader.Upgrade(w, r, upgradeHeaders)
		if err != nil {
			t.Fatalf("expected websocket upgrade: %v", err)
		}

		go func() {
			defer conn.Close()
			for {
				var payload map[string]any
				if err := conn.ReadJSON(&payload); err != nil {
					return
				}

				mu.Lock()
				requestCount++
				currentRequest := requestCount
				mu.Unlock()

				_ = conn.WriteJSON(map[string]any{
					"type": "response.output_item.done",
					"item": map[string]any{
						"type": "message",
						"id":   "msg_reuse",
						"content": []map[string]any{
							{"type": "output_text", "text": "reply"},
						},
					},
				})
				_ = conn.WriteJSON(map[string]any{
					"type": "response.completed",
					"response": map[string]any{
						"id":     "resp_reuse_" + string(rune('0'+currentRequest)),
						"status": "completed",
						"usage": map[string]any{
							"input_tokens":  4,
							"output_tokens": 1,
							"total_tokens":  5,
							"input_tokens_details": map[string]any{
								"cached_tokens": 0,
							},
						},
					},
				})
			}
		}()
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.5")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL
	onResponse := func(providerResponse ProviderResponse, _ Model) {
		mu.Lock()
		providerResponses = append(providerResponses, providerResponse)
		mu.Unlock()
	}

	first := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "one"}},
	}, SimpleStreamOptions{
		APIKey:     token,
		Transport:  TransportWebSocket,
		SessionID:  sessionID,
		OnResponse: onResponse,
	})

	if first.StopReason == StopReasonError {
		t.Fatal(first.ErrorMessage)
	}
	call := func(id string) {
		t.Helper()
		result := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: "next"}}}, SimpleStreamOptions{APIKey: token, Transport: TransportWebSocket, SessionID: id})
		if result.StopReason == StopReasonError {
			t.Fatal(result.ErrorMessage)
		}
	}
	other := sessionID + "-other"
	call(other)
	for _, id := range []string{"", "unknown", sessionID, sessionID} {
		if err := CleanupSessionResources(id); err != nil {
			t.Fatal(err)
		}
	}
	openAICodexWebSocketCacheMu.Lock()
	targetCount, otherCount := 0, 0
	for key, entry := range openAICodexWebSocketSessionCache {
		if key.sessionID == sessionID {
			targetCount++
		}
		if key.sessionID == other && !entry.closed {
			otherCount++
		}
	}
	openAICodexWebSocketCacheMu.Unlock()
	if targetCount != 0 || otherCount != 1 {
		t.Fatalf("target=%d other=%d", targetCount, otherCount)
	}
	call(other)
	mu.Lock()
	upgrades := upgradeCount
	mu.Unlock()
	if upgrades != 2 {
		t.Fatalf("other session lost reuse: upgrades=%d", upgrades)
	}
	call(sessionID)
	mu.Lock()
	upgrades = upgradeCount
	mu.Unlock()
	if upgrades != 3 {
		t.Fatalf("cleaned session did not reconnect: %d", upgrades)
	}
}

func TestCodexSessionCleanupRacesReleaseSafely(t *testing.T) {
	clearOpenAICodexWebSocketSessionCache()
	defer clearOpenAICodexWebSocketSessionCache()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	closed := make(chan struct{}, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		_, _, _ = c.ReadMessage()
		closed <- struct{}{}
	}))
	defer server.Close()
	for i := 0; i < 20; i++ {
		acquired, e := acquireOpenAICodexWebSocket(websocket.Dialer{}, "ws"+strings.TrimPrefix(server.URL, "http"), http.Header{}, "account", "cleanup-race", t.Context(), nil)
		if e != nil {
			t.Fatal(e)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); acquired.release(true) }()
		go func() {
			defer wg.Done()
			if e := CleanupSessionResources("cleanup-race"); e != nil {
				t.Error(e)
			}
		}()
		wg.Wait()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("socket not closed")
		}
		openAICodexWebSocketCacheMu.Lock()
		n := len(openAICodexWebSocketSessionCache)
		openAICodexWebSocketCacheMu.Unlock()
		if n != 0 {
			t.Fatalf("release resurrected cache: %d", n)
		}
	}
}
