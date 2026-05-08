package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompleteSimpleOpenAICodexSendsImageOnlyToolResultInFunctionCallOutput(t *testing.T) {
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
					"id":   "msg_image_only",
					"content": []map[string]any{
						{"type": "output_text", "text": "red circle"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_image_only",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  9,
						"output_tokens": 2,
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

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Call the tool"},
			AssistantMessage{
				Content: []ContentBlock{
					ToolCall{
						ID:        "call_1|fc_1",
						Name:      "get_circle",
						Arguments: map[string]any{},
					},
				},
				API:        "openai-codex-responses",
				Provider:   "openai-codex",
				Model:      "gpt-5.4",
				StopReason: StopReasonToolUse,
			},
			ToolResultMessage{
				ToolCallID: "call_1|fc_1",
				ToolName:   "get_circle",
				Content: []ContentBlock{
					ImageContent{Data: "abcd", MIMEType: "image/png"},
				},
			},
		},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected response to succeed, got %+v", response)
	}

	functionCallOutputIndex := -1
	for i, item := range requestBody.Input {
		if item["type"] == "function_call_output" {
			functionCallOutputIndex = i
			break
		}
	}
	if functionCallOutputIndex < 0 {
		t.Fatalf("expected function_call_output item, got %#v", requestBody.Input)
	}

	output, ok := requestBody.Input[functionCallOutputIndex]["output"].([]any)
	if !ok || len(output) != 2 {
		t.Fatalf("expected placeholder text plus image output, got %#v", requestBody.Input[functionCallOutputIndex]["output"])
	}
	textPart, ok := output[0].(map[string]any)
	if !ok || textPart["type"] != "input_text" || textPart["text"] != "(see attached image)" {
		t.Fatalf("expected placeholder text part, got %#v", output[0])
	}
	imagePart, ok := output[1].(map[string]any)
	if !ok {
		t.Fatalf("expected output image part object, got %T", output[1])
	}
	if imagePart["type"] != "input_image" {
		t.Fatalf("expected input_image part, got %#v", imagePart)
	}
	if imagePart["image_url"] != "data:image/png;base64,abcd" {
		t.Fatalf("expected png data url, got %#v", imagePart["image_url"])
	}
	for _, laterItem := range requestBody.Input[functionCallOutputIndex+1:] {
		if laterItem["role"] == "user" {
			t.Fatalf("expected no later user items after function_call_output, got %#v", laterItem)
		}
	}
}

func TestCompleteSimpleOpenAICodexSendsMixedToolResultInFunctionCallOutput(t *testing.T) {
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
					"id":   "msg_text_image",
					"content": []map[string]any{
						{"type": "output_text", "text": "red circle, diameter 100"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_text_image",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  12,
						"output_tokens": 4,
						"total_tokens":  16,
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
		Messages: []Message{
			UserMessage{Content: "Call the tool"},
			AssistantMessage{
				Content: []ContentBlock{
					ToolCall{
						ID:        "call_1|fc_1",
						Name:      "get_circle_with_description",
						Arguments: map[string]any{},
					},
				},
				API:        "openai-codex-responses",
				Provider:   "openai-codex",
				Model:      "gpt-5.4",
				StopReason: StopReasonToolUse,
			},
			ToolResultMessage{
				ToolCallID: "call_1|fc_1",
				ToolName:   "get_circle_with_description",
				Content: []ContentBlock{
					TextContent{Text: "Diameter is 100 pixels."},
					ImageContent{Data: "abcd", MIMEType: "image/png"},
				},
			},
		},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	functionCallOutputIndex := -1
	for i, item := range requestBody.Input {
		if item["type"] == "function_call_output" {
			functionCallOutputIndex = i
			break
		}
	}
	if functionCallOutputIndex < 0 {
		t.Fatalf("expected function_call_output item, got %#v", requestBody.Input)
	}

	output, ok := requestBody.Input[functionCallOutputIndex]["output"].([]any)
	if !ok || len(output) != 2 {
		t.Fatalf("expected mixed tool result output, got %#v", requestBody.Input[functionCallOutputIndex]["output"])
	}
	textPart, ok := output[0].(map[string]any)
	if !ok || textPart["type"] != "input_text" || textPart["text"] != "Diameter is 100 pixels." {
		t.Fatalf("expected first output part to be text, got %#v", output[0])
	}
	imagePart, ok := output[1].(map[string]any)
	if !ok || imagePart["type"] != "input_image" {
		t.Fatalf("expected second output part to be image, got %#v", output[1])
	}
}

func TestCompleteSimpleKimiCodingSendsImageOnlyToolResultContent(t *testing.T) {
	var requestBody anthropicRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request body: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_image_only",
					"usage": map[string]any{
						"input_tokens":                12,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": "red circle",
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"output_tokens": 3,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Call the tool"},
			AssistantMessage{
				Content: []ContentBlock{
					ToolCall{
						ID:        "call_1",
						Name:      "get_circle",
						Arguments: map[string]any{},
					},
				},
				API:        "anthropic-messages",
				Provider:   "kimi-coding",
				Model:      "k2p5",
				StopReason: StopReasonToolUse,
			},
			ToolResultMessage{
				ToolCallID: "call_1",
				ToolName:   "get_circle",
				Content: []ContentBlock{
					ImageContent{Data: "abcd", MIMEType: "image/png"},
				},
			},
		},
	}, SimpleStreamOptions{
		APIKey: "kimi-test-key",
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected response to succeed, got %+v", response)
	}
	if len(requestBody.Messages) == 0 {
		t.Fatalf("expected anthropic messages payload, got %#v", requestBody)
	}

	lastMessage := requestBody.Messages[len(requestBody.Messages)-1]
	toolResults, ok := lastMessage.Content.([]any)
	if !ok || len(toolResults) != 1 {
		t.Fatalf("expected single tool result block, got %#v", lastMessage.Content)
	}
	toolResult, ok := toolResults[0].(map[string]any)
	if !ok || toolResult["type"] != "tool_result" {
		t.Fatalf("expected tool_result block, got %#v", toolResults[0])
	}
	content, ok := toolResult["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("expected placeholder text plus image content block, got %#v", toolResult["content"])
	}
	textBlock, ok := content[0].(map[string]any)
	if !ok || textBlock["type"] != "text" || textBlock["text"] != "(see attached image)" {
		t.Fatalf("expected placeholder text content block, got %#v", content[0])
	}
	imageBlock, ok := content[1].(map[string]any)
	if !ok || imageBlock["type"] != "image" {
		t.Fatalf("expected image content block, got %#v", content[1])
	}
	source, ok := imageBlock["source"].(map[string]any)
	if !ok || source["media_type"] != "image/png" || source["data"] != "abcd" {
		t.Fatalf("expected png image source, got %#v", imageBlock["source"])
	}
}

func TestCompleteSimpleKimiCodingSendsMixedToolResultContent(t *testing.T) {
	var requestBody anthropicRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request body: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_text_image",
					"usage": map[string]any{
						"input_tokens":                14,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": "red circle with diameter info",
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"output_tokens": 4,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	_ = CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Call the tool"},
			AssistantMessage{
				Content: []ContentBlock{
					ToolCall{
						ID:        "call_1",
						Name:      "get_circle_with_description",
						Arguments: map[string]any{},
					},
				},
				API:        "anthropic-messages",
				Provider:   "kimi-coding",
				Model:      "k2p5",
				StopReason: StopReasonToolUse,
			},
			ToolResultMessage{
				ToolCallID: "call_1",
				ToolName:   "get_circle_with_description",
				Content: []ContentBlock{
					TextContent{Text: "Diameter is 100 pixels."},
					ImageContent{Data: "abcd", MIMEType: "image/png"},
				},
			},
		},
	}, SimpleStreamOptions{
		APIKey: "kimi-test-key",
	})

	lastMessage := requestBody.Messages[len(requestBody.Messages)-1]
	toolResults, ok := lastMessage.Content.([]any)
	if !ok || len(toolResults) != 1 {
		t.Fatalf("expected single tool result block, got %#v", lastMessage.Content)
	}
	toolResult, ok := toolResults[0].(map[string]any)
	if !ok || toolResult["type"] != "tool_result" {
		t.Fatalf("expected tool_result block, got %#v", toolResults[0])
	}
	content, ok := toolResult["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("expected mixed text+image content, got %#v", toolResult["content"])
	}
	textBlock, ok := content[0].(map[string]any)
	if !ok || textBlock["type"] != "text" || textBlock["text"] != "Diameter is 100 pixels." {
		t.Fatalf("expected first content block to be text, got %#v", content[0])
	}
	imageBlock, ok := content[1].(map[string]any)
	if !ok || imageBlock["type"] != "image" {
		t.Fatalf("expected second content block to be image, got %#v", content[1])
	}
}
