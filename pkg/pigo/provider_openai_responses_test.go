package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveOpenAIResponsesSDKBaseURL(t *testing.T) {
	if got := resolveOpenAIResponsesSDKBaseURL(""); got != "https://api.openai.com/v1" {
		t.Fatalf("expected sdk base url to default to /v1, got %q", got)
	}
	if got := resolveOpenAIResponsesSDKBaseURL("https://api.openai.com"); got != "https://api.openai.com/v1" {
		t.Fatalf("expected root base url to become /v1 for sdk, got %q", got)
	}
	if got := resolveOpenAIResponsesSDKBaseURL("https://api.openai.com/v1/responses"); got != "https://api.openai.com/v1" {
		t.Fatalf("expected responses path to trim to sdk base url, got %q", got)
	}
}

func TestResolveOpenAIResponsesURLAddsV1ForRootBaseURL(t *testing.T) {
	if got := resolveOpenAIResponsesURL(""); got != "https://api.openai.com/v1/responses" {
		t.Fatalf("expected default openai responses url with /v1 prefix, got %q", got)
	}
	if got := resolveOpenAIResponsesURL("https://api.openai.com"); got != "https://api.openai.com/v1/responses" {
		t.Fatalf("expected official root base url to receive /v1/responses, got %q", got)
	}
	if got := resolveOpenAIResponsesURL("https://api.openai.com/v1"); got != "https://api.openai.com/v1/responses" {
		t.Fatalf("expected explicit /v1 base url to receive /responses suffix, got %q", got)
	}
}

func TestCompleteSimpleOpenAIResponsesUsesAuthConfigAndV1ResponsesPath(t *testing.T) {
	var (
		requestPath string
		authHeader  string
		requestBody openAIResponsesRequest
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		authHeader = r.Header.Get("authorization")
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request body: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_openai_1",
					"content": []map[string]any{
						{"type": "output_text", "text": "openai ok"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_openai_1",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  5,
						"output_tokens": 2,
						"total_tokens":  7,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
						},
					},
				},
			},
		)))
	}))
	defer server.Close()

	model := GetModel("openai", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		Auth: map[Provider]AuthConfig{
			"openai": {
				Type:   AuthTypeAPIKey,
				APIKey: "openai-test-key",
			},
		},
	})

	if requestPath != "/v1/responses" {
		t.Fatalf("expected openai responses request path /v1/responses, got %q", requestPath)
	}
	if authHeader != "Bearer openai-test-key" {
		t.Fatalf("expected bearer auth header from options.Auth, got %q", authHeader)
	}
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected successful openai response, got %+v", response)
	}
	if requestBody.Model != "gpt-5.4" || !requestBody.Stream {
		t.Fatalf("expected openai responses request payload, got %+v", requestBody)
	}
}

func TestCompleteOpenAIResponsesForwardsSessionHeadersAndOnResponse(t *testing.T) {
	var (
		requestPath            string
		authHeader             string
		sessionHeader          string
		requestIDHeader        string
		customHeader           string
		onResponseStatus       int
		onResponseRequestTrace string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		authHeader = r.Header.Get("authorization")
		sessionHeader = r.Header.Get("session_id")
		requestIDHeader = r.Header.Get("x-client-request-id")
		customHeader = r.Header.Get("x-test-header")
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("x-request-trace", "trace_123")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_openai_2",
					"content": []map[string]any{
						{"type": "output_text", "text": "ok"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_openai_2",
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
			},
		)))
	}))
	defer server.Close()

	model := GetModel("openai", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai model")
	}
	model.BaseURL = server.URL

	response := Complete(*model, Context{
		Messages: []Message{UserMessage{Content: "hello"}},
	}, ProviderStreamOptions{
		Auth: map[Provider]AuthConfig{
			"openai": {
				Type:   AuthTypeAPIKey,
				APIKey: "openai-session-key",
			},
		},
		SessionID: "sess_abc",
		Headers: map[string]string{
			"x-test-header": "enabled",
		},
		OnResponse: func(providerResponse ProviderResponse, _ Model) {
			onResponseStatus = providerResponse.Status
			onResponseRequestTrace = providerResponse.Headers["X-Request-Trace"]
		},
	})

	if requestPath != "/v1/responses" {
		t.Fatalf("expected /v1/responses path, got %q", requestPath)
	}
	if authHeader != "Bearer openai-session-key" {
		t.Fatalf("expected bearer auth header, got %q", authHeader)
	}
	if sessionHeader != "sess_abc" {
		t.Fatalf("expected session_id header, got %q", sessionHeader)
	}
	if requestIDHeader != "sess_abc" {
		t.Fatalf("expected x-client-request-id header, got %q", requestIDHeader)
	}
	if customHeader != "enabled" {
		t.Fatalf("expected custom header passthrough, got %q", customHeader)
	}
	if onResponseStatus != 200 {
		t.Fatalf("expected OnResponse status=200, got %d", onResponseStatus)
	}
	if onResponseRequestTrace != "trace_123" {
		t.Fatalf("expected OnResponse response header propagation, got %q", onResponseRequestTrace)
	}
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected successful response, got %+v", response)
	}
}
