package pigo

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func makeOpenAICodexToken(accountID string) string {
	payloadBytes, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
		},
	})
	return "aaa." + base64.RawURLEncoding.EncodeToString(payloadBytes) + ".bbb"
}

func buildOpenAICodexSSE(events ...map[string]any) string {
	lines := make([]string, 0, len(events)+1)
	for _, event := range events {
		payload, _ := json.Marshal(event)
		lines = append(lines, "data: "+string(payload))
	}
	lines = append(lines, "data: [DONE]")
	return strings.Join(lines, "\n\n") + "\n\n"
}

func TestCompleteSimpleOpenAICodexBuildsRequestAndParsesText(t *testing.T) {
	var requestBody openAICodexRequest
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/responses" {
			t.Fatalf("expected /codex/responses path, got %s", r.URL.Path)
		}
		if got := r.Header.Get("authorization"); got != "Bearer "+token {
			t.Fatalf("expected bearer auth header, got %q", got)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "acc_test" {
			t.Fatalf("expected account id header, got %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request body: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_1",
					"content": []map[string]any{
						{
							"type": "output_text",
							"text": "hello from codex",
						},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_123",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  20,
						"output_tokens": 5,
						"total_tokens":  25,
						"input_tokens_details": map[string]any{
							"cached_tokens": 2,
						},
					},
				},
			},
		)))
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		SystemPrompt: "You are concise.",
		Messages: []Message{
			UserMessage{Content: "Say hello"},
		},
	}, CompleteOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response, got %q", response.StopReason)
	}
	text, ok := response.Content[0].(TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", response.Content[0])
	}
	if text.Text != "hello from codex" {
		t.Fatalf("expected response text, got %q", text.Text)
	}
	if response.Usage.Input != 18 || response.Usage.CacheRead != 2 {
		t.Fatalf("expected usage with cached token split, got %+v", response.Usage)
	}
	if requestBody.Model != "gpt-5.4" || !requestBody.Stream {
		t.Fatalf("expected stream codex request, got %+v", requestBody)
	}
	if requestBody.Instructions != "You are concise." {
		t.Fatalf("expected instructions in request, got %q", requestBody.Instructions)
	}
}

func TestCompleteSimpleOpenAICodexParsesToolCallResponse(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type":      "function_call",
					"id":        "fc_test",
					"call_id":   "call_test",
					"name":      "edit",
					"arguments": `{"path":"README.md"}`,
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_124",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  10,
						"output_tokens": 3,
						"total_tokens":  13,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
						},
					},
				},
			},
		)))
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.2-codex")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Edit the file"},
		},
	}, CompleteOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonToolUse {
		t.Fatalf("expected toolUse stop reason, got %q", response.StopReason)
	}
	call, ok := response.Content[0].(ToolCall)
	if !ok {
		t.Fatalf("expected tool call content, got %T", response.Content[0])
	}
	if call.ID != "call_test|fc_test" || call.Name != "edit" {
		t.Fatalf("expected parsed tool call, got %+v", call)
	}
}

func TestCompleteSimpleOpenAICodexNormalizesForeignToolCallIDsInRequest(t *testing.T) {
	var requestBody openAICodexRequest
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request body: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_1",
					"content": []map[string]any{
						{"type": "output_text", "text": "ok"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_125",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  10,
						"output_tokens": 1,
						"total_tokens":  11,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
						},
					},
				},
			},
		)))
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.3-codex")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	_ = CompleteSimple(*model, Context{
		Messages: []Message{
			AssistantMessage{
				Content: []ContentBlock{
					ToolCall{
						ID:        copilotRawToolCallID,
						Name:      "edit",
						Arguments: map[string]any{"path": "README.md"},
					},
				},
				API:        "openai-responses",
				Provider:   "github-copilot",
				Model:      "gpt-5.3-codex",
				StopReason: StopReasonToolUse,
			},
			ToolResultMessage{
				ToolCallID: copilotRawToolCallID,
				ToolName:   "edit",
				Content:    []ContentBlock{TextContent{Text: "ok"}},
			},
		},
	}, CompleteOptions{
		APIKey: token,
	})

	if len(requestBody.Input) != 2 {
		t.Fatalf("expected 2 input items, got %d", len(requestBody.Input))
	}
	functionCall := requestBody.Input[0]
	if functionCall["type"] != "function_call" {
		t.Fatalf("expected function_call input item, got %#v", functionCall)
	}
	callID, ok := functionCall["call_id"].(string)
	if !ok {
		t.Fatalf("expected function_call call_id, got %#v", functionCall["call_id"])
	}
	if strings.ContainsAny(callID, "|+/= ") {
		t.Fatalf("expected normalized foreign call_id, got %#v", callID)
	}
	if _, exists := functionCall["id"]; exists {
		t.Fatalf("expected foreign function_call item id to be omitted, got %#v", functionCall["id"])
	}
	functionCallOutput := requestBody.Input[1]
	if functionCallOutput["call_id"] != callID {
		t.Fatalf("expected function_call_output call_id %q, got %#v", callID, functionCallOutput["call_id"])
	}
}

func TestCompleteSimpleOpenAICodexReturnsProviderErrorMessage(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"type":    "rate_limit_exceeded",
				"message": "usage limit reached",
			},
		})
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "hello"},
		},
	}, CompleteOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonError {
		t.Fatalf("expected error stop reason, got %q", response.StopReason)
	}
	if response.ErrorMessage != "usage limit reached" {
		t.Fatalf("expected provider error message, got %q", response.ErrorMessage)
	}
}
