package pigo

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeepSeekBuildRequestDefaultsToProThinkingMax(t *testing.T) {
	model := GetModel("deepseek", "deepseek-v4-pro")
	if model == nil {
		t.Fatal("expected deepseek-v4-pro model")
	}
	var captured map[string]any
	response := Complete(*model, Context{
		SystemPrompt: "You are concise.",
		Messages: []Message{
			UserMessage{Content: "ping"},
		},
	}, ProviderStreamOptions{
		APIKey: "test-key",
		OnPayload: func(payload any, _ Model) any {
			bytes, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			if err := json.Unmarshal(bytes, &captured); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			return payload
		},
		HTTPClient: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return sseResponse(`data: {"id":"chatcmpl-test","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"), nil
		}).Client(),
	})
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response, got %s error=%s", response.StopReason, response.ErrorMessage)
	}
	if captured["model"] != "deepseek-v4-pro" {
		t.Fatalf("expected deepseek-v4-pro model, got %#v", captured["model"])
	}
	thinking, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking object, got %#v", captured["thinking"])
	}
	if thinking["type"] != "enabled" || thinking["reasoning_effort"] != "max" {
		t.Fatalf("expected thinking enabled/max, got %#v", thinking)
	}
}

func TestDeepSeekSimpleOptionsDoNotRecurse(t *testing.T) {
	model := GetModel("deepseek", "deepseek-v4-flash")
	if model == nil {
		t.Fatal("expected deepseek-v4-flash model")
	}
	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "ping"},
		},
	}, SimpleStreamOptions{
		APIKey: "test-key",
		HTTPClient: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return sseResponse(`data: {"id":"chatcmpl-test","model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":"stop"}]}` + "\n\n" + `data: [DONE]` + "\n\n"), nil
		}).Client(),
	})
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response, got %s error=%s", response.StopReason, response.ErrorMessage)
	}
}

func TestDeepSeekStreamsReasoningAndText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.URL.Path; got != "/chat/completions" {
			t.Fatalf("expected /chat/completions, got %s", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("expected bearer auth, got %q", got)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte(`data: {"id":"chatcmpl-test","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"reasoning_content":"think"},"finish_reason":null}]}` + "\n\n"))
		_, _ = writer.Write([]byte(`data: {"id":"chatcmpl-test","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5,"prompt_cache_hit_tokens":1}}` + "\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model := GetModel("deepseek", "deepseek-v4-pro")
	if model == nil {
		t.Fatal("expected deepseek-v4-pro model")
	}
	model.BaseURL = server.URL
	response := Complete(*model, Context{Messages: []Message{UserMessage{Content: "ping"}}}, ProviderStreamOptions{APIKey: "test-key"})
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response, got %s error=%s", response.StopReason, response.ErrorMessage)
	}
	if len(response.Content) != 2 {
		t.Fatalf("expected thinking and text blocks, got %#v", response.Content)
	}
	if thinking, ok := response.Content[0].(ThinkingContent); !ok || thinking.Thinking != "think" {
		t.Fatalf("expected thinking block, got %#v", response.Content[0])
	}
	if text, ok := response.Content[1].(TextContent); !ok || text.Text != "answer" {
		t.Fatalf("expected answer text, got %#v", response.Content[1])
	}
	if response.Usage.TotalTokens != 5 || response.Usage.CacheRead != 1 {
		t.Fatalf("expected usage to be mapped, got %+v", response.Usage)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (f roundTripFunc) Client() *http.Client {
	return &http.Client{Transport: f}
}

func sseResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
