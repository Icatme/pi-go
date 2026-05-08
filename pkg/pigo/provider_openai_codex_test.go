package pigo

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

func clearOpenAICodexWebSocketSessionCache() {
	openAICodexWebSocketCacheMu.Lock()
	entries := make([]*cachedOpenAICodexWebSocketConnection, 0, len(openAICodexWebSocketSessionCache))
	for sessionID, entry := range openAICodexWebSocketSessionCache {
		delete(openAICodexWebSocketSessionCache, sessionID)
		entries = append(entries, entry)
	}
	openAICodexWebSocketCacheMu.Unlock()

	for _, entry := range entries {
		closeOpenAICodexWebSocket(entry)
	}
}

func TestCompleteSimpleOpenAICodexBuildsRequestAndParsesText(t *testing.T) {
	var requestBody openAIResponsesRequest
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
	}, SimpleStreamOptions{
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
	}, SimpleStreamOptions{
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
	var requestBody openAIResponsesRequest
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
	}, SimpleStreamOptions{
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
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonError {
		t.Fatalf("expected error stop reason, got %q", response.StopReason)
	}
	if response.ErrorMessage != "You have hit your ChatGPT usage limit." {
		t.Fatalf("expected friendly usage limit message, got %q", response.ErrorMessage)
	}
}

func TestParseOpenAICodexErrorBuildsFriendlyUsageLimitMessageWithPlanAndReset(t *testing.T) {
	message := parseOpenAIResponsesErrorWithProvider([]byte(fmt.Sprintf(
		`{"error":{"type":"usage_limit_reached","message":"usage limit reached","plan_type":"PRO","resets_at":%d}}`,
		time.Now().Unix()+120,
	)), "429 Too Many Requests", "codex")

	if !strings.Contains(message, "You have hit your ChatGPT usage limit (pro plan).") {
		t.Fatalf("expected friendly usage limit message with plan, got %q", message)
	}
	if !strings.Contains(message, "Try again in ~") {
		t.Fatalf("expected retry window in friendly usage limit message, got %q", message)
	}
}

func TestParseOpenAICodexErrorFallsBackToProviderMessageForNonUsageErrors(t *testing.T) {
	message := parseOpenAIResponsesError([]byte(`{"error":{"type":"invalid_request_error","message":"bad request body"}}`), "400 Bad Request")
	if message != "bad request body" {
		t.Fatalf("expected provider message for non-usage error, got %q", message)
	}
}

func TestCompleteSimpleOpenAICodexParsesTerminalOnlyMixedOutput(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.created",
				"response": map[string]any{
					"id": "resp_created_only",
				},
			},
			map[string]any{
				"type": "response.done",
				"response": map[string]any{
					"status": "completed",
					"output": []map[string]any{
						{
							"type": "reasoning",
							"id":   "rs_1",
							"summary": []map[string]any{
								{"type": "summary_text", "text": "thinking first"},
							},
						},
						{
							"type": "message",
							"id":   "msg_1",
							"content": []map[string]any{
								{"type": "output_text", "text": "hello"},
							},
						},
						{
							"type":      "function_call",
							"id":        "fc_1",
							"call_id":   "call_1",
							"name":      "edit",
							"arguments": `{"path":"README.md"}`,
						},
					},
					"usage": map[string]any{
						"input_tokens":  8,
						"output_tokens": 4,
						"total_tokens":  12,
						"input_tokens_details": map[string]any{
							"cached_tokens": 1,
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
		Messages: []Message{
			UserMessage{Content: "hello"},
		},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.ResponseID != "resp_created_only" {
		t.Fatalf("expected response id from response.created, got %q", response.ResponseID)
	}
	if len(response.Content) != 3 {
		t.Fatalf("expected mixed terminal output blocks, got %+v", response.Content)
	}
	if thinking, ok := response.Content[0].(ThinkingContent); !ok || thinking.Thinking != "thinking first" {
		t.Fatalf("expected first terminal block to be thinking, got %#v", response.Content[0])
	}
	if text, ok := response.Content[1].(TextContent); !ok || text.Text != "hello" {
		t.Fatalf("expected second terminal block to be text, got %#v", response.Content[1])
	}
	if call, ok := response.Content[2].(ToolCall); !ok || call.ID != "call_1|fc_1" || call.Name != "edit" {
		t.Fatalf("expected third terminal block to be tool call, got %#v", response.Content[2])
	}
	if response.StopReason != StopReasonToolUse {
		t.Fatalf("expected toolUse stop reason from terminal output, got %q", response.StopReason)
	}
	if response.Usage.Input != 7 || response.Usage.CacheRead != 1 {
		t.Fatalf("expected cached token split from terminal response, got %+v", response.Usage)
	}
}

func TestCompleteSimpleOpenAICodexMapsIncompleteResponseToLength(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_1",
					"content": []map[string]any{
						{"type": "output_text", "text": "truncated"},
					},
				},
			},
			map[string]any{
				"type": "response.incomplete",
				"response": map[string]any{
					"id":     "resp_incomplete",
					"status": "incomplete",
					"usage": map[string]any{
						"input_tokens":  6,
						"output_tokens": 2,
						"total_tokens":  8,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
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
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonLength {
		t.Fatalf("expected length stop reason, got %+v", response)
	}
}

func TestCompleteSimpleOpenAICodexReturnsTerminalFailureMessage(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.failed",
				"response": map[string]any{
					"id":     "resp_failed",
					"status": "failed",
					"error": map[string]any{
						"type":    "server_error",
						"message": "backend exploded",
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
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonError {
		t.Fatalf("expected error stop reason, got %+v", response)
	}
	if response.ErrorMessage != "backend exploded" {
		t.Fatalf("expected terminal failure message, got %q", response.ErrorMessage)
	}
}

func TestCompleteSimpleOpenAICodexReturnsTopLevelErrorEventMessage(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type":    "error",
				"message": "upstream gateway refused request",
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
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonError {
		t.Fatalf("expected error stop reason, got %+v", response)
	}
	if response.ErrorMessage != "upstream gateway refused request" {
		t.Fatalf("expected top-level error event message, got %q", response.ErrorMessage)
	}
}

func TestCompleteSimpleOpenAICodexParsesRefusalMessageContent(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_refusal",
					"content": []map[string]any{
						{"type": "refusal", "refusal": "cannot comply"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_refusal",
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

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if len(response.Content) != 1 {
		t.Fatalf("expected one refusal text block, got %+v", response.Content)
	}
	text, ok := response.Content[0].(TextContent)
	if !ok || text.Text != "cannot comply" {
		t.Fatalf("expected refusal to map to text content, got %#v", response.Content[0])
	}
}

func TestCompleteSimpleOpenAICodexCombinesMultipleMessageContentParts(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.done",
				"response": map[string]any{
					"id":     "resp_multi_parts",
					"status": "completed",
					"output": []map[string]any{
						{
							"type": "message",
							"id":   "msg_multi",
							"content": []map[string]any{
								{"type": "output_text", "text": "hello"},
								{"type": "output_text", "text": " there"},
								{"type": "refusal", "refusal": "!"},
							},
						},
					},
					"usage": map[string]any{
						"input_tokens":  6,
						"output_tokens": 3,
						"total_tokens":  9,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
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
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if len(response.Content) != 1 {
		t.Fatalf("expected combined message content block, got %+v", response.Content)
	}
	text, ok := response.Content[0].(TextContent)
	if !ok || text.Text != "hello there!" {
		t.Fatalf("expected combined message text, got %#v", response.Content[0])
	}
}

func TestCompleteSimpleOpenAICodexFailedWithoutMessageUsesFallback(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.failed",
				"response": map[string]any{
					"id":     "resp_failed_fallback",
					"status": "failed",
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
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonError {
		t.Fatalf("expected error stop reason, got %+v", response)
	}
	if response.ErrorMessage != "codex response failed" {
		t.Fatalf("expected failed fallback message, got %q", response.ErrorMessage)
	}
	if response.ResponseID != "resp_failed_fallback" {
		t.Fatalf("expected failed response id, got %q", response.ResponseID)
	}
}

func TestCompleteSimpleOpenAICodexTopLevelErrorPreservesCreatedResponseID(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.created",
				"response": map[string]any{
					"id": "resp_created_error",
				},
			},
			map[string]any{
				"type":    "error",
				"message": "transport broke",
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
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonError {
		t.Fatalf("expected error stop reason, got %+v", response)
	}
	if response.ResponseID != "resp_created_error" {
		t.Fatalf("expected created response id to survive top-level error, got %q", response.ResponseID)
	}
}

func TestCompleteSimpleOpenAICodexClampsNegativeComputedInputUsage(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_usage_clamp",
					"status": "completed",
					"output": []map[string]any{
						{
							"type": "message",
							"id":   "msg_usage",
							"content": []map[string]any{
								{"type": "output_text", "text": "ok"},
							},
						},
					},
					"usage": map[string]any{
						"input_tokens":  1,
						"output_tokens": 2,
						"total_tokens":  3,
						"input_tokens_details": map[string]any{
							"cached_tokens": 4,
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
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.Usage.Input != 0 {
		t.Fatalf("expected negative derived input tokens to clamp to 0, got %+v", response.Usage)
	}
	if response.Usage.CacheRead != 4 {
		t.Fatalf("expected cache read tokens to be preserved, got %+v", response.Usage)
	}
}

func TestCompleteSimpleOpenAICodexFailsOnMalformedTerminalResponse(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.completed",
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
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonError {
		t.Fatalf("expected malformed terminal response to fail, got %+v", response)
	}
	if response.ErrorMessage != "missing terminal response" {
		t.Fatalf("expected malformed terminal response error, got %q", response.ErrorMessage)
	}
}

func TestCompleteSimpleOpenAICodexSetsSessionCacheAndReasoningOptions(t *testing.T) {
	var (
		requestBody openAIResponsesRequest
		headers     http.Header
	)
	token := makeOpenAICodexToken("acc_test")
	sessionID := "session-test-123"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
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
					"id":     "resp_126",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  5,
						"output_tokens": 1,
						"total_tokens":  6,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
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

	_ = Complete(*model, Context{
		SystemPrompt: "Be precise.",
		Messages: []Message{
			UserMessage{Content: "hi"},
		},
	}, ProviderStreamOptions{
		APIKey:           token,
		SessionID:        sessionID,
		Reasoning:        ThinkingLevelMinimal,
		ReasoningSummary: "detailed",
		TextVerbosity:    "high",
	})

	if headers.Get("conversation_id") != sessionID {
		t.Fatalf("expected conversation_id header %q, got %q", sessionID, headers.Get("conversation_id"))
	}
	if headers.Get("session_id") != sessionID {
		t.Fatalf("expected session_id header %q, got %q", sessionID, headers.Get("session_id"))
	}
	if requestBody.PromptCacheKey != sessionID {
		t.Fatalf("expected prompt_cache_key %q, got %q", sessionID, requestBody.PromptCacheKey)
	}
	if requestBody.Reasoning == nil || requestBody.Reasoning.Effort != "low" || requestBody.Reasoning.Summary != "detailed" {
		t.Fatalf("expected clamped reasoning options, got %+v", requestBody.Reasoning)
	}
	if requestBody.Text == nil || requestBody.Text.Verbosity != "high" {
		t.Fatalf("expected text verbosity high, got %+v", requestBody.Text)
	}
	if len(requestBody.Include) != 1 || requestBody.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("expected reasoning include payload, got %+v", requestBody.Include)
	}
}

func TestCompleteSimpleOpenAICodexRefreshesExpiredOAuthBeforeRequest(t *testing.T) {
	var (
		requestToken string
		refreshCalls int
	)
	refreshedToken := makeOpenAICodexToken("acc_fresh")

	previousURL := openAICodexOAuthTokenURL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			refreshCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  refreshedToken,
				"refresh_token": "refresh-new",
				"expires_in":    3600,
			})
		case "/codex/responses":
			requestToken = strings.TrimPrefix(r.Header.Get("authorization"), "Bearer ")
			w.Header().Set("content-type", "text/event-stream")
			_, _ = w.Write([]byte(buildOpenAICodexSSE(
				map[string]any{
					"type": "response.output_item.done",
					"item": map[string]any{
						"type": "message",
						"id":   "msg_1",
						"content": []map[string]any{
							{"type": "output_text", "text": "refreshed"},
						},
					},
				},
				map[string]any{
					"type": "response.completed",
					"response": map[string]any{
						"id":     "resp_127",
						"status": "completed",
						"usage": map[string]any{
							"input_tokens":  5,
							"output_tokens": 1,
							"total_tokens":  6,
							"input_tokens_details": map[string]any{
								"cached_tokens": 0,
							},
						},
					},
				},
			)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer func() {
		openAICodexOAuthTokenURL = previousURL
	}()
	openAICodexOAuthTokenURL = server.URL + "/oauth/token"

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "hi"},
		},
	}, SimpleStreamOptions{
		Auth: map[Provider]AuthConfig{
			"openai-codex": {
				Type: AuthTypeOAuth,
				OAuth: &OAuthCredentials{
					AccessToken:  "expired-token",
					RefreshToken: "refresh-old",
					ExpiresUnix:  1,
				},
			},
		},
		HTTPClient: server.Client(),
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected successful response after refresh, got %q with %q", response.StopReason, response.ErrorMessage)
	}
	if refreshCalls != 1 {
		t.Fatalf("expected exactly one refresh call, got %d", refreshCalls)
	}
	if requestToken != refreshedToken {
		t.Fatalf("expected refreshed token on request, got %q", requestToken)
	}
}

func TestCompleteSimpleOpenAICodexUsesRequestedServiceTierWhenResponseEchoesDefault(t *testing.T) {
	var requestBody openAIResponsesRequest
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
					"id":   "msg_service_tier",
					"content": []map[string]any{
						{"type": "output_text", "text": "priority"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":           "resp_service_tier",
					"status":       "completed",
					"service_tier": "default",
					"usage": map[string]any{
						"input_tokens":  1000000,
						"output_tokens": 1000000,
						"total_tokens":  2000000,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
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
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey:      token,
		ServiceTier: "priority",
	})

	if requestBody.ServiceTier != "priority" {
		t.Fatalf("expected service_tier priority in request body, got %q", requestBody.ServiceTier)
	}
	if response.Usage.Cost.Input != 5 || response.Usage.Cost.Output != 30 || response.Usage.Cost.Total != 35 {
		t.Fatalf("expected priority pricing multiplier to apply, got %+v", response.Usage.Cost)
	}
}

func TestCompleteSimpleOpenAICodexAutoTransportFallsBackToSSEWhenWebSocketSetupFails(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	var websocketAttempts, sseAttempts int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			websocketAttempts++
			http.Error(w, "websocket unavailable", http.StatusBadGateway)
			return
		}

		sseAttempts++
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_auto_fallback",
					"content": []map[string]any{
						{"type": "output_text", "text": "fell back to sse"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_auto_fallback",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  4,
						"output_tokens": 3,
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

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey:    token,
		Transport: TransportAuto,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected fallback request to succeed, got %+v", response)
	}
	if websocketAttempts != 1 {
		t.Fatalf("expected one websocket attempt before fallback, got %d", websocketAttempts)
	}
	if sseAttempts != 1 {
		t.Fatalf("expected one sse fallback request, got %d", sseAttempts)
	}
	text, ok := response.Content[0].(TextContent)
	if !ok || text.Text != "fell back to sse" {
		t.Fatalf("expected sse fallback content, got %+v", response.Content)
	}
}

func TestCompleteSimpleOpenAICodexWebSocketTransportDoesNotFallbackToSSE(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	var websocketAttempts, sseAttempts int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			websocketAttempts++
			http.Error(w, "websocket unavailable", http.StatusBadGateway)
			return
		}

		sseAttempts++
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_sse_should_not_run",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  1,
						"output_tokens": 1,
						"total_tokens":  2,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
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
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey:    token,
		Transport: TransportWebSocket,
	})

	if response.StopReason != StopReasonError {
		t.Fatalf("expected websocket-only request to fail, got %+v", response)
	}
	if websocketAttempts != 1 {
		t.Fatalf("expected one websocket attempt, got %d", websocketAttempts)
	}
	if sseAttempts != 0 {
		t.Fatalf("expected no sse fallback for websocket transport, got %d", sseAttempts)
	}
}

func TestCompleteSimpleOpenAICodexSSERetriesTransientHTTPError(t *testing.T) {
	previousRetryCount := openAICodexRetryCount
	previousRetryDelay := openAICodexBaseRetryDelay
	openAICodexRetryCount = 2
	openAICodexBaseRetryDelay = time.Millisecond
	defer func() {
		openAICodexRetryCount = previousRetryCount
		openAICodexBaseRetryDelay = previousRetryDelay
	}()

	token := makeOpenAICodexToken("acc_test")
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit hit"}}`))
			return
		}

		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_retry_success",
					"content": []map[string]any{
						{"type": "output_text", "text": "retried successfully"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_retry_success",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  4,
						"output_tokens": 2,
						"total_tokens":  6,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
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
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey:    token,
		Transport: TransportSSE,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected retry path to succeed, got %+v", response)
	}
	if attempts != 2 {
		t.Fatalf("expected one retry before success, got %d attempts", attempts)
	}
}

func TestCompleteSimpleOpenAICodexSSEFinalHTTPErrorUsesParsedMessage(t *testing.T) {
	previousRetryCount := openAICodexRetryCount
	previousRetryDelay := openAICodexBaseRetryDelay
	openAICodexRetryCount = 1
	openAICodexBaseRetryDelay = time.Millisecond
	defer func() {
		openAICodexRetryCount = previousRetryCount
		openAICodexBaseRetryDelay = previousRetryDelay
	}()

	token := makeOpenAICodexToken("acc_test")
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"final parsed error"}}`))
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey:    token,
		Transport: TransportSSE,
	})

	if response.StopReason != StopReasonError {
		t.Fatalf("expected final http error, got %+v", response)
	}
	if response.ErrorMessage != "final parsed error" {
		t.Fatalf("expected parsed error message, got %q", response.ErrorMessage)
	}
	if attempts != 1 {
		t.Fatalf("expected non-retryable error to stop after first attempt, got %d", attempts)
	}
}

func TestCompleteSimpleOpenAICodexAutoTransportDoesNotFallbackAfterWebSocketStarts(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	var websocketAttempts, sseAttempts int

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			websocketAttempts++
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Fatalf("expected websocket upgrade: %v", err)
			}
			defer conn.Close()
			var payload map[string]any
			if err := conn.ReadJSON(&payload); err != nil {
				t.Fatalf("expected websocket request payload: %v", err)
			}
			if got := payload["type"]; got != "response.create" {
				t.Fatalf("expected websocket response.create payload, got %#v", got)
			}
			_ = conn.WriteJSON(map[string]any{
				"type":    "error",
				"message": "websocket broke after start",
			})
			return
		}

		sseAttempts++
		http.Error(w, "sse should not be used", http.StatusInternalServerError)
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hello"}},
	}, SimpleStreamOptions{
		APIKey:    token,
		Transport: TransportAuto,
	})

	if response.StopReason != StopReasonError {
		t.Fatalf("expected websocket-started error to stop request, got %+v", response)
	}
	if response.ErrorMessage != "websocket broke after start" {
		t.Fatalf("expected websocket error to surface, got %q", response.ErrorMessage)
	}
	if websocketAttempts != 1 {
		t.Fatalf("expected one websocket attempt, got %d", websocketAttempts)
	}
	if sseAttempts != 0 {
		t.Fatalf("expected no sse fallback after websocket start, got %d", sseAttempts)
	}
}

func TestCompleteSimpleOpenAICodexWebSocketReusesSessionConnection(t *testing.T) {
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
		mu           sync.Mutex
		upgradeCount int
		requestCount int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected websocket", http.StatusBadRequest)
			return
		}

		mu.Lock()
		upgradeCount++
		mu.Unlock()

		conn, err := upgrader.Upgrade(w, r, nil)
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

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	first := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "one"}},
	}, SimpleStreamOptions{
		APIKey:    token,
		Transport: TransportWebSocket,
		SessionID: sessionID,
	})
	second := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "two"}},
	}, SimpleStreamOptions{
		APIKey:    token,
		Transport: TransportWebSocket,
		SessionID: sessionID,
	})

	if first.StopReason != StopReasonStop || second.StopReason != StopReasonStop {
		t.Fatalf("expected websocket session requests to succeed, got first=%+v second=%+v", first, second)
	}

	mu.Lock()
	defer mu.Unlock()
	if upgradeCount != 1 {
		t.Fatalf("expected one websocket upgrade for reused session, got %d", upgradeCount)
	}
	if requestCount != 2 {
		t.Fatalf("expected two response.create requests on reused websocket, got %d", requestCount)
	}
}

func TestCompleteSimpleOpenAICodexWebSocketSessionExpiresAfterIdleTTL(t *testing.T) {
	clearOpenAICodexWebSocketSessionCache()
	defer clearOpenAICodexWebSocketSessionCache()

	previousTTL := getOpenAICodexWebSocketCacheTTL()
	setOpenAICodexWebSocketCacheTTL(20 * time.Millisecond)
	defer func() {
		setOpenAICodexWebSocketCacheTTL(previousTTL)
	}()

	token := makeOpenAICodexToken("acc_test")
	sessionID := "ws-session-expiry"
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	var (
		mu           sync.Mutex
		upgradeCount int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected websocket", http.StatusBadRequest)
			return
		}

		mu.Lock()
		upgradeCount++
		mu.Unlock()

		conn, err := upgrader.Upgrade(w, r, nil)
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

				_ = conn.WriteJSON(map[string]any{
					"type": "response.output_item.done",
					"item": map[string]any{
						"type": "message",
						"id":   "msg_expiry",
						"content": []map[string]any{
							{"type": "output_text", "text": "reply"},
						},
					},
				})
				_ = conn.WriteJSON(map[string]any{
					"type": "response.completed",
					"response": map[string]any{
						"id":     "resp_expiry",
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

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	first := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "one"}},
	}, SimpleStreamOptions{
		APIKey:    token,
		Transport: TransportWebSocket,
		SessionID: sessionID,
	})
	if first.StopReason != StopReasonStop {
		t.Fatalf("expected first websocket session request to succeed, got %+v", first)
	}

	time.Sleep(60 * time.Millisecond)

	second := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "two"}},
	}, SimpleStreamOptions{
		APIKey:    token,
		Transport: TransportWebSocket,
		SessionID: sessionID,
	})
	if second.StopReason != StopReasonStop {
		t.Fatalf("expected second websocket session request to succeed, got %+v", second)
	}

	mu.Lock()
	defer mu.Unlock()
	if upgradeCount != 2 {
		t.Fatalf("expected websocket session to reconnect after idle expiry, got %d upgrades", upgradeCount)
	}
}

func TestCompleteSimpleOpenAICodexPassesToolChoiceFromOptions(t *testing.T) {
	var requestBody openAIResponsesRequest
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
					"id":     "resp_toolchoice",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  5,
						"output_tokens": 1,
						"total_tokens":  6,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
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

	_ = Complete(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
		Tools: []Tool{{
			Name:        "noop",
			Description: "Do nothing",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}},
	}, ProviderStreamOptions{
		APIKey:     token,
		ToolChoice: "required",
	})

	if requestBody.ToolChoice != "required" {
		t.Fatalf("expected tool_choice 'required' from options, got %q", requestBody.ToolChoice)
	}
}

func TestCompleteSimpleOpenAICodexDefaultsToolChoiceToAuto(t *testing.T) {
	var requestBody openAIResponsesRequest
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
					"id":     "resp_default_auto",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  5,
						"output_tokens": 1,
						"total_tokens":  6,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
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

	_ = CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if requestBody.ToolChoice != "auto" {
		t.Fatalf("expected default tool_choice 'auto', got %q", requestBody.ToolChoice)
	}
}

func TestCompleteSimpleOpenAICodexPassesPreviousResponseID(t *testing.T) {
	var requestBody openAIResponsesRequest
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
					"id":     "resp_prev_id",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  5,
						"output_tokens": 1,
						"total_tokens":  6,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
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

	_ = CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey:             token,
		PreviousResponseID: "prev_resp_123",
	})

	if requestBody.PreviousResponseID != "prev_resp_123" {
		t.Fatalf("expected previous_response_id 'prev_resp_123', got %q", requestBody.PreviousResponseID)
	}
}

func TestCompleteSimpleOpenAICodexOmitsEmptyPreviousResponseID(t *testing.T) {
	var requestBody openAIResponsesRequest
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
					"id":     "resp_omit_prev",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  5,
						"output_tokens": 1,
						"total_tokens":  6,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
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

	_ = CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if requestBody.PreviousResponseID != "" {
		t.Fatalf("expected empty previous_response_id, got %q", requestBody.PreviousResponseID)
	}
}
