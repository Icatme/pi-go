package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
