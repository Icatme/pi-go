package pigo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type openAICodexRequest struct {
	Model             string                       `json:"model"`
	Store             bool                         `json:"store"`
	Stream            bool                         `json:"stream"`
	Instructions      string                       `json:"instructions,omitempty"`
	Input             []map[string]any             `json:"input,omitempty"`
	Tools             []map[string]any             `json:"tools,omitempty"`
	ToolChoice        string                       `json:"tool_choice,omitempty"`
	ParallelToolCalls bool                         `json:"parallel_tool_calls,omitempty"`
	Temperature       *float64                     `json:"temperature,omitempty"`
	Reasoning         *openAICodexReasoningOptions `json:"reasoning,omitempty"`
	Text              *openAICodexTextOptions      `json:"text,omitempty"`
	Include           []string                     `json:"include,omitempty"`
	PromptCacheKey    string                       `json:"prompt_cache_key,omitempty"`
}

type openAICodexReasoningOptions struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type openAICodexTextOptions struct {
	Verbosity string `json:"verbosity,omitempty"`
}

type openAICodexResponse struct {
	ID     string                    `json:"id"`
	Status string                    `json:"status"`
	Output []openAICodexResponseItem `json:"output"`
	Usage  openAICodexUsage          `json:"usage"`
	Error  *openAICodexResponseError `json:"error,omitempty"`
}

type openAICodexResponseItem struct {
	Type      string                           `json:"type"`
	ID        string                           `json:"id,omitempty"`
	CallID    string                           `json:"call_id,omitempty"`
	Name      string                           `json:"name,omitempty"`
	Arguments string                           `json:"arguments,omitempty"`
	Summary   []openAICodexReasoningSummary    `json:"summary,omitempty"`
	Content   []openAICodexResponseContentPart `json:"content,omitempty"`
}

type openAICodexReasoningSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAICodexResponseContentPart struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

type openAICodexUsage struct {
	InputTokens  int                          `json:"input_tokens"`
	OutputTokens int                          `json:"output_tokens"`
	TotalTokens  int                          `json:"total_tokens"`
	InputDetails openAICodexInputTokenDetails `json:"input_tokens_details"`
}

type openAICodexInputTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type openAICodexResponseError struct {
	Code    string `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

type openAICodexStreamingState struct {
	CurrentTextIndex       int
	CurrentThinkingIndex   int
	CurrentToolIndex       int
	CurrentToolJSON        string
	CurrentTextItemKey     string
	CurrentThinkingItemKey string
	CurrentToolItemKey     string
	FinalizedItemKeys      map[string]bool
}

func streamOpenAICodex(model Model, ctx Context, options CompleteOptions) *AssistantMessageEventStream {
	stream := newAssistantMessageEventStream()

	response := AssistantMessage{
		API:        model.API,
		Provider:   model.Provider,
		Model:      model.ID,
		StopReason: StopReasonStop,
		Timestamp:  time.Now().UTC(),
	}

	go func() {
		apiKey := options.APIKey
		if apiKey == "" {
			resolved, err := ResolveAuthorization(model.Provider, options.Auth, options.HTTPClient, options.RequestContext)
			if err != nil {
				response.StopReason = StopReasonError
				response.ErrorMessage = err.Error()
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
				stream.finish(response)
				return
			}
			apiKey = resolved
		}
		if apiKey == "" {
			response.StopReason = StopReasonError
			response.ErrorMessage = "missing api key"
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		accountID, err := extractOpenAICodexAccountID(apiKey)
		if err != nil {
			response.StopReason = StopReasonError
			response.ErrorMessage = err.Error()
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		requestBody := buildOpenAICodexRequest(model, ctx, options)
		payload := any(requestBody)
		if options.OnPayload != nil {
			if next := options.OnPayload(payload, model); next != nil {
				payload = next
			}
		}

		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			response.StopReason = StopReasonError
			response.ErrorMessage = err.Error()
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		requestContext := options.RequestContext
		if requestContext == nil {
			requestContext = context.Background()
		}

		request, err := http.NewRequestWithContext(
			requestContext,
			http.MethodPost,
			resolveOpenAICodexURL(model.BaseURL),
			bytes.NewReader(bodyBytes),
		)
		if err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		request.Header.Set("content-type", "application/json")
		request.Header.Set("accept", "text/event-stream")
		request.Header.Set("authorization", "Bearer "+apiKey)
		request.Header.Set("chatgpt-account-id", accountID)
		request.Header.Set("originator", "pi")
		request.Header.Set("openai-beta", "responses=experimental")
		if options.SessionID != "" {
			request.Header.Set("conversation_id", options.SessionID)
			request.Header.Set("session_id", options.SessionID)
		}
		for key, value := range options.Headers {
			request.Header.Set(key, value)
		}

		httpClient := options.HTTPClient
		if httpClient == nil {
			httpClient = http.DefaultClient
		}

		httpResponse, err := httpClient.Do(request)
		if err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}
		defer httpResponse.Body.Close()

		if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
			body, _ := io.ReadAll(httpResponse.Body)
			response.StopReason = StopReasonError
			response.ErrorMessage = parseOpenAICodexError(body, httpResponse.Status)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		stream.push(AssistantMessageEvent{
			Type:    AssistantMessageEventStart,
			Partial: response,
		})

		state := openAICodexStreamingState{
			CurrentTextIndex:     -1,
			CurrentThinkingIndex: -1,
			CurrentToolIndex:     -1,
			FinalizedItemKeys:    map[string]bool{},
		}
		terminalSeen := false
		if err := readSSEStream(httpResponse.Body, func(_ string, data string) (bool, error) {
			done, err := processOpenAICodexStreamEvent(data, model, &response, stream, &state)
			if done {
				terminalSeen = true
				return true, nil
			}
			return false, err
		}); err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		if !terminalSeen {
			response.StopReason = StopReasonError
			response.ErrorMessage = "missing terminal sse event"
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
		}
	}()

	return stream
}

func buildOpenAICodexRequest(model Model, ctx Context, options CompleteOptions) openAICodexRequest {
	options = NormalizeCompleteOptions(model, options)

	requestBody := openAICodexRequest{
		Model:             model.ID,
		Store:             false,
		Stream:            true,
		Instructions:      ctx.SystemPrompt,
		Input:             convertOpenAICodexMessages(model, ctx),
		Tools:             convertOpenAICodexTools(ctx.Tools),
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Include:           []string{"reasoning.encrypted_content"},
		Text: &openAICodexTextOptions{
			Verbosity: defaultTextVerbosity(options.TextVerbosity),
		},
	}

	if options.Temperature != nil {
		requestBody.Temperature = options.Temperature
	}
	if options.SessionID != "" {
		requestBody.PromptCacheKey = options.SessionID
	}

	if effort := string(options.Reasoning); effort != "" {
		requestBody.Reasoning = &openAICodexReasoningOptions{
			Effort:  effort,
			Summary: options.ReasoningSummary,
		}
	}

	return requestBody
}

func resolveOpenAICodexURL(baseURL string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	if trimmed == "" {
		trimmed = "https://chatgpt.com/backend-api"
	}
	if strings.HasSuffix(trimmed, "/codex/responses") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/codex") {
		return trimmed + "/responses"
	}
	return trimmed + "/codex/responses"
}

func defaultTextVerbosity(verbosity string) string {
	if strings.TrimSpace(verbosity) == "" {
		return "medium"
	}
	return verbosity
}

func defaultReasoningSummary(summary string) string {
	if strings.TrimSpace(summary) == "" {
		return "auto"
	}
	return summary
}

func clampOpenAICodexReasoningEffort(model Model, level ThinkingLevel) string {
	effort := string(level)
	if effort == "" {
		return ""
	}

	id := model.ID
	if strings.Contains(id, "/") {
		parts := strings.Split(id, "/")
		id = parts[len(parts)-1]
	}

	if (strings.HasPrefix(id, "gpt-5.2") || strings.HasPrefix(id, "gpt-5.3") || strings.HasPrefix(id, "gpt-5.4")) && effort == string(ThinkingLevelMinimal) {
		return string(ThinkingLevelLow)
	}
	if id == "gpt-5.1" && effort == string(ThinkingLevelXHigh) {
		return string(ThinkingLevelHigh)
	}
	if id == "gpt-5.1-codex-mini" {
		if effort == string(ThinkingLevelHigh) || effort == string(ThinkingLevelXHigh) {
			return string(ThinkingLevelHigh)
		}
		return string(ThinkingLevelMedium)
	}
	if effort == string(ThinkingLevelXHigh) && !SupportsXHigh(model) {
		return string(ThinkingLevelHigh)
	}
	return effort
}

func extractOpenAICodexAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("failed to extract accountId from token")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payloadBytes, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", fmt.Errorf("failed to extract accountId from token")
		}
	}

	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return "", fmt.Errorf("failed to extract accountId from token")
	}

	authClaim, ok := payload["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("failed to extract accountId from token")
	}
	accountID, ok := authClaim["chatgpt_account_id"].(string)
	if !ok || strings.TrimSpace(accountID) == "" {
		return "", fmt.Errorf("failed to extract accountId from token")
	}
	return accountID, nil
}

func convertOpenAICodexMessages(model Model, ctx Context) []map[string]any {
	transformed := TransformMessages(ctx.Messages, model, NormalizeOpenAIResponsesToolCallID)
	input := make([]map[string]any, 0, len(transformed)+1)

	for index, message := range transformed {
		switch typed := message.(type) {
		case UserMessage:
			switch content := typed.Content.(type) {
			case string:
				if strings.TrimSpace(content) == "" {
					continue
				}
				input = append(input, map[string]any{
					"role": "user",
					"content": []map[string]any{
						{
							"type": "input_text",
							"text": content,
						},
					},
				})
			case []ContentBlock:
				contentParts := make([]map[string]any, 0, len(content))
				for _, block := range content {
					switch block := block.(type) {
					case TextContent:
						if strings.TrimSpace(block.Text) == "" {
							continue
						}
						contentParts = append(contentParts, map[string]any{
							"type": "input_text",
							"text": block.Text,
						})
					case ImageContent:
						if !modelSupportsInput(model, InputImage) {
							continue
						}
						contentParts = append(contentParts, map[string]any{
							"type":      "input_image",
							"detail":    "auto",
							"image_url": "data:" + block.MIMEType + ";base64," + block.Data,
						})
					}
				}
				if len(contentParts) == 0 {
					continue
				}
				input = append(input, map[string]any{
					"role":    "user",
					"content": contentParts,
				})
			}
		case AssistantMessage:
			for _, block := range typed.Content {
				switch block := block.(type) {
				case TextContent:
					if strings.TrimSpace(block.Text) == "" {
						continue
					}
					input = append(input, map[string]any{
						"type":   "message",
						"role":   "assistant",
						"id":     fmt.Sprintf("msg_%d", index),
						"status": "completed",
						"content": []map[string]any{
							{
								"type": "output_text",
								"text": block.Text,
							},
						},
					})
				case ThinkingContent:
					if strings.TrimSpace(block.ThinkingSignature) != "" {
						var reasoning map[string]any
						if json.Unmarshal([]byte(block.ThinkingSignature), &reasoning) == nil {
							input = append(input, reasoning)
						}
					}
				case ToolCall:
					callID, itemID := splitToolCallID(block.ID)
					functionCall := map[string]any{
						"type":      "function_call",
						"call_id":   callID,
						"name":      block.Name,
						"arguments": mustJSON(block.Arguments),
					}
					if itemID != "" && typed.Provider == model.Provider && typed.API == model.API && typed.Model == model.ID {
						functionCall["id"] = itemID
					}
					input = append(input, functionCall)
				}
			}
		case ToolResultMessage:
			callID, _ := splitToolCallID(typed.ToolCallID)
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  buildOpenAICodexToolResultOutput(typed.Content, model),
			})
		}
	}

	return input
}

func convertOpenAICodexTools(tools []Tool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}

	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		result = append(result, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  parameters,
			"strict":      false,
		})
	}
	return result
}

func splitToolCallID(id string) (string, string) {
	callID, itemID, found := strings.Cut(id, "|")
	if !found {
		return id, ""
	}
	return callID, itemID
}

func mustJSON(value any) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(bytes)
}

func buildOpenAICodexToolResultOutput(blocks []ContentBlock, model Model) any {
	var (
		content  []map[string]any
		hasImage bool
		parts    []string
	)

	for _, block := range blocks {
		switch block := block.(type) {
		case TextContent:
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			parts = append(parts, block.Text)
			content = append(content, map[string]any{
				"type": "input_text",
				"text": block.Text,
			})
		case ImageContent:
			hasImage = true
			if modelSupportsInput(model, InputImage) {
				content = append(content, map[string]any{
					"type":      "input_image",
					"detail":    "auto",
					"image_url": "data:" + block.MIMEType + ";base64," + block.Data,
				})
				continue
			}
			parts = append(parts, "(see attached image)")
		}
	}

	if hasImage && modelSupportsInput(model, InputImage) {
		hasText := false
		for _, part := range content {
			if part["type"] == "input_text" {
				hasText = true
				break
			}
		}
		if !hasText {
			content = append([]map[string]any{{
				"type": "input_text",
				"text": "(see attached image)",
			}}, content...)
		}
		return content
	}
	if len(parts) == 0 {
		return "No result provided"
	}
	return strings.Join(parts, "\n")
}

func processOpenAICodexStreamEvent(
	data string,
	model Model,
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
	state *openAICodexStreamingState,
) (bool, error) {
	if strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "[DONE]" {
		return false, nil
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return false, err
	}

	eventType, _ := event["type"].(string)
	switch eventType {
	case "response.created":
		if responseMap, ok := event["response"].(map[string]any); ok {
			if id, ok := responseMap["id"].(string); ok && strings.TrimSpace(id) != "" {
				response.ResponseID = id
			}
		}
	case "response.output_item.added":
		itemMap, ok := event["item"].(map[string]any)
		if !ok {
			return false, nil
		}
		itemBytes, err := json.Marshal(itemMap)
		if err != nil {
			return false, err
		}
		var item openAICodexResponseItem
		if err := json.Unmarshal(itemBytes, &item); err != nil {
			return false, err
		}
		startOpenAICodexStreamItem(response, stream, state, item)
	case "response.reasoning_summary_text.delta":
		if state.CurrentThinkingIndex < 0 {
			startOpenAICodexThinkingBlock(response, stream, state)
		}
		if state.CurrentThinkingIndex >= 0 {
			block, _ := response.Content[state.CurrentThinkingIndex].(ThinkingContent)
			delta, _ := event["delta"].(string)
			block.Thinking += delta
			response.Content[state.CurrentThinkingIndex] = block
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventThinkingDelta,
				ContentIndex: state.CurrentThinkingIndex,
				Delta:        delta,
				Partial:      *response,
			})
		}
	case "response.content_part.added":
		partMap, ok := event["part"].(map[string]any)
		if !ok {
			return false, nil
		}
		partType, _ := partMap["type"].(string)
		if partType == "output_text" || partType == "refusal" {
			if state.CurrentTextIndex < 0 {
				startOpenAICodexTextBlock(response, stream, state)
			}
		}
	case "response.output_text.delta", "response.refusal.delta":
		if state.CurrentTextIndex < 0 {
			startOpenAICodexTextBlock(response, stream, state)
		}
		if state.CurrentTextIndex >= 0 {
			block, _ := response.Content[state.CurrentTextIndex].(TextContent)
			delta, _ := event["delta"].(string)
			block.Text += delta
			response.Content[state.CurrentTextIndex] = block
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventTextDelta,
				ContentIndex: state.CurrentTextIndex,
				Delta:        delta,
				Partial:      *response,
			})
		}
	case "response.function_call_arguments.delta":
		if state.CurrentToolIndex < 0 {
			return false, nil
		}
		delta, _ := event["delta"].(string)
		state.CurrentToolJSON += delta
		block, _ := response.Content[state.CurrentToolIndex].(ToolCall)
		block.Arguments = parseStreamingJSONObject(state.CurrentToolJSON)
		response.Content[state.CurrentToolIndex] = block
		stream.push(AssistantMessageEvent{
			Type:         AssistantMessageEventToolCallDelta,
			ContentIndex: state.CurrentToolIndex,
			Delta:        delta,
			Partial:      *response,
		})
	case "response.function_call_arguments.done":
		if state.CurrentToolIndex < 0 {
			return false, nil
		}
		arguments, _ := event["arguments"].(string)
		if arguments != "" {
			state.CurrentToolJSON = arguments
			block, _ := response.Content[state.CurrentToolIndex].(ToolCall)
			block.Arguments = parseStreamingJSONObject(state.CurrentToolJSON)
			response.Content[state.CurrentToolIndex] = block
		}
	case "response.output_item.done":
		itemMap, ok := event["item"].(map[string]any)
		if !ok {
			return false, nil
		}
		itemBytes, err := json.Marshal(itemMap)
		if err != nil {
			return false, err
		}
		var item openAICodexResponseItem
		if err := json.Unmarshal(itemBytes, &item); err != nil {
			return false, err
		}
		finalizeOpenAICodexStreamItem(response, stream, state, item)
	case "response.completed", "response.done", "response.incomplete":
		responseMap, ok := event["response"].(map[string]any)
		if !ok {
			return false, fmt.Errorf("missing terminal response")
		}
		responseBytes, err := json.Marshal(responseMap)
		if err != nil {
			return false, err
		}
		var terminal openAICodexResponse
		if err := json.Unmarshal(responseBytes, &terminal); err != nil {
			return false, err
		}
		emitOpenAICodexTerminalOutputIfNeeded(response, stream, state, terminal)
		applyOpenAICodexTerminal(model, response, terminal)
		stream.push(AssistantMessageEvent{
			Type:    AssistantMessageEventDone,
			Reason:  response.StopReason,
			Message: *response,
		})
		stream.finish(*response)
		return true, nil
	case "response.failed":
		responseMap, ok := event["response"].(map[string]any)
		if !ok {
			return false, fmt.Errorf("codex response failed")
		}
		responseBytes, err := json.Marshal(responseMap)
		if err != nil {
			return false, err
		}
		var terminal openAICodexResponse
		if err := json.Unmarshal(responseBytes, &terminal); err != nil {
			return false, err
		}
		if terminal.ID != "" {
			response.ResponseID = terminal.ID
		}
		if terminal.Error != nil && strings.TrimSpace(terminal.Error.Message) != "" {
			response.StopReason = StopReasonError
			response.ErrorMessage = terminal.Error.Message
		} else {
			response.StopReason = StopReasonError
			response.ErrorMessage = "codex response failed"
		}
		stream.push(AssistantMessageEvent{
			Type:   AssistantMessageEventError,
			Reason: response.StopReason,
			Error:  *response,
		})
		stream.finish(*response)
		return true, nil
	case "error":
		if message, ok := event["message"].(string); ok && strings.TrimSpace(message) != "" {
			response.ErrorMessage = message
		} else {
			response.ErrorMessage = "codex error"
		}
		response.StopReason = StopReasonError
		stream.push(AssistantMessageEvent{
			Type:   AssistantMessageEventError,
			Reason: response.StopReason,
			Error:  *response,
		})
		stream.finish(*response)
		return true, nil
	}

	return false, nil
}

func emitOpenAICodexTerminalOutputIfNeeded(
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
	state *openAICodexStreamingState,
	terminal openAICodexResponse,
) {
	if len(terminal.Output) == 0 {
		return
	}

	for _, item := range terminal.Output {
		itemKey := openAICodexItemKey(item)
		if itemKey != "" && state != nil && state.FinalizedItemKeys[itemKey] {
			continue
		}

		switch item.Type {
		case "message":
			if state != nil && state.CurrentTextIndex >= 0 && state.CurrentTextItemKey == itemKey {
				finalizeOpenAICodexStreamItem(response, stream, state, item)
				continue
			}
		case "reasoning":
			if state != nil && state.CurrentThinkingIndex >= 0 && state.CurrentThinkingItemKey == itemKey {
				finalizeOpenAICodexStreamItem(response, stream, state, item)
				continue
			}
		case "function_call":
			if state != nil && state.CurrentToolIndex >= 0 && state.CurrentToolItemKey == itemKey {
				finalizeOpenAICodexStreamItem(response, stream, state, item)
				continue
			}
		}

		emitOpenAICodexItemLifecycle(response, stream, item)
		if itemKey != "" && state != nil {
			state.FinalizedItemKeys[itemKey] = true
		}
	}
}

func openAICodexItemKey(item openAICodexResponseItem) string {
	switch item.Type {
	case "message":
		if strings.TrimSpace(item.ID) != "" {
			return "message|" + item.ID
		}
		return "message|" + mustJSON(item.Content)
	case "reasoning":
		if strings.TrimSpace(item.ID) != "" {
			return "reasoning|" + item.ID
		}
		return "reasoning|" + mustJSON(item.Summary)
	case "function_call":
		return "function_call|" + item.CallID + "|" + item.ID
	default:
		return ""
	}
}

func emitOpenAICodexItemLifecycle(response *AssistantMessage, stream *AssistantMessageEventStream, item openAICodexResponseItem) {
	for _, block := range parseOpenAICodexResponseOutput([]openAICodexResponseItem{item}) {
		contentIndex := len(response.Content)
		switch typed := block.(type) {
		case TextContent:
			response.Content = append(response.Content, TextContent{})
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventTextStart,
				ContentIndex: contentIndex,
				Partial:      *response,
			})
			response.Content[contentIndex] = typed
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventTextDelta,
				ContentIndex: contentIndex,
				Delta:        typed.Text,
				Partial:      *response,
			})
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventTextEnd,
				ContentIndex: contentIndex,
				Content:      typed.Text,
				Partial:      *response,
			})
		case ThinkingContent:
			response.Content = append(response.Content, ThinkingContent{ThinkingSignature: typed.ThinkingSignature, Redacted: typed.Redacted})
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventThinkingStart,
				ContentIndex: contentIndex,
				Partial:      *response,
			})
			response.Content[contentIndex] = typed
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventThinkingDelta,
				ContentIndex: contentIndex,
				Delta:        typed.Thinking,
				Partial:      *response,
			})
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventThinkingEnd,
				ContentIndex: contentIndex,
				Content:      typed.Thinking,
				Partial:      *response,
			})
		case ToolCall:
			response.Content = append(response.Content, ToolCall{
				ID:               typed.ID,
				Name:             typed.Name,
				Arguments:        map[string]any{},
				ThoughtSignature: typed.ThoughtSignature,
			})
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventToolCallStart,
				ContentIndex: contentIndex,
				Partial:      *response,
			})
			response.Content[contentIndex] = typed
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventToolCallDelta,
				ContentIndex: contentIndex,
				Delta:        mustJSON(typed.Arguments),
				Partial:      *response,
			})
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventToolCallEnd,
				ContentIndex: contentIndex,
				ToolCall:     typed,
				Partial:      *response,
			})
		}
	}
}

func startOpenAICodexStreamItem(
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
	state *openAICodexStreamingState,
	item openAICodexResponseItem,
) {
	itemKey := openAICodexItemKey(item)
	switch item.Type {
	case "message":
		startOpenAICodexTextBlock(response, stream, state)
		state.CurrentTextItemKey = itemKey
	case "reasoning":
		startOpenAICodexThinkingBlock(response, stream, state)
		state.CurrentThinkingItemKey = itemKey
	case "function_call":
		contentIndex := len(response.Content)
		response.Content = append(response.Content, ToolCall{
			ID:        combineOpenAICodexToolCallID(item.CallID, item.ID),
			Name:      item.Name,
			Arguments: parseStreamingJSONObject(item.Arguments),
		})
		state.CurrentToolIndex = contentIndex
		state.CurrentToolJSON = item.Arguments
		state.CurrentToolItemKey = itemKey
		stream.push(AssistantMessageEvent{
			Type:         AssistantMessageEventToolCallStart,
			ContentIndex: contentIndex,
			Partial:      *response,
		})
	}
}

func startOpenAICodexTextBlock(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAICodexStreamingState) {
	contentIndex := len(response.Content)
	response.Content = append(response.Content, TextContent{})
	state.CurrentTextIndex = contentIndex
	stream.push(AssistantMessageEvent{
		Type:         AssistantMessageEventTextStart,
		ContentIndex: contentIndex,
		Partial:      *response,
	})
}

func startOpenAICodexThinkingBlock(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAICodexStreamingState) {
	contentIndex := len(response.Content)
	response.Content = append(response.Content, ThinkingContent{})
	state.CurrentThinkingIndex = contentIndex
	stream.push(AssistantMessageEvent{
		Type:         AssistantMessageEventThinkingStart,
		ContentIndex: contentIndex,
		Partial:      *response,
	})
}

func finalizeOpenAICodexStreamItem(
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
	state *openAICodexStreamingState,
	item openAICodexResponseItem,
) {
	itemKey := openAICodexItemKey(item)
	switch item.Type {
	case "message":
		if state.CurrentTextIndex < 0 {
			emitOpenAICodexItemLifecycle(response, stream, item)
			if itemKey != "" {
				state.FinalizedItemKeys[itemKey] = true
			}
			return
		}
		block, _ := response.Content[state.CurrentTextIndex].(TextContent)
		var parts []string
		for _, part := range item.Content {
			switch part.Type {
			case "output_text":
				parts = append(parts, part.Text)
			case "refusal":
				parts = append(parts, part.Refusal)
			}
		}
		if len(parts) > 0 {
			block.Text = strings.Join(parts, "")
		}
		response.Content[state.CurrentTextIndex] = block
		stream.push(AssistantMessageEvent{
			Type:         AssistantMessageEventTextEnd,
			ContentIndex: state.CurrentTextIndex,
			Content:      block.Text,
			Partial:      *response,
		})
		state.CurrentTextIndex = -1
		state.CurrentTextItemKey = ""
	case "reasoning":
		if state.CurrentThinkingIndex < 0 {
			emitOpenAICodexItemLifecycle(response, stream, item)
			if itemKey != "" {
				state.FinalizedItemKeys[itemKey] = true
			}
			return
		}
		block, _ := response.Content[state.CurrentThinkingIndex].(ThinkingContent)
		if len(item.Summary) > 0 {
			parts := make([]string, 0, len(item.Summary))
			for _, summary := range item.Summary {
				if strings.TrimSpace(summary.Text) != "" {
					parts = append(parts, summary.Text)
				}
			}
			if len(parts) > 0 {
				block.Thinking = strings.Join(parts, "\n\n")
			}
		}
		signatureBytes, err := json.Marshal(item)
		if err == nil {
			block.ThinkingSignature = string(signatureBytes)
		}
		response.Content[state.CurrentThinkingIndex] = block
		stream.push(AssistantMessageEvent{
			Type:         AssistantMessageEventThinkingEnd,
			ContentIndex: state.CurrentThinkingIndex,
			Content:      block.Thinking,
			Partial:      *response,
		})
		state.CurrentThinkingIndex = -1
		state.CurrentThinkingItemKey = ""
	case "function_call":
		if state.CurrentToolIndex < 0 {
			emitOpenAICodexItemLifecycle(response, stream, item)
			if itemKey != "" {
				state.FinalizedItemKeys[itemKey] = true
			}
			return
		}
		block, _ := response.Content[state.CurrentToolIndex].(ToolCall)
		if strings.TrimSpace(item.Arguments) != "" {
			block.Arguments = parseStreamingJSONObject(item.Arguments)
		} else {
			block.Arguments = parseStreamingJSONObject(state.CurrentToolJSON)
		}
		block.ID = combineOpenAICodexToolCallID(item.CallID, item.ID)
		block.Name = item.Name
		response.Content[state.CurrentToolIndex] = block
		stream.push(AssistantMessageEvent{
			Type:         AssistantMessageEventToolCallEnd,
			ContentIndex: state.CurrentToolIndex,
			ToolCall:     block,
			Partial:      *response,
		})
		state.CurrentToolIndex = -1
		state.CurrentToolJSON = ""
		state.CurrentToolItemKey = ""
	}
	if itemKey != "" {
		state.FinalizedItemKeys[itemKey] = true
	}
}

func combineOpenAICodexToolCallID(callID string, itemID string) string {
	if itemID == "" {
		return callID
	}
	return callID + "|" + itemID
}

func applyOpenAICodexTerminal(model Model, response *AssistantMessage, terminal openAICodexResponse) {
	if terminal.ID != "" {
		response.ResponseID = terminal.ID
	}
	if len(response.Content) == 0 && len(terminal.Output) > 0 {
		response.Content = append(response.Content, parseOpenAICodexResponseOutput(terminal.Output)...)
	}
	response.StopReason = mapOpenAICodexStopReason(terminal.Status, response.Content)
	inputTokens := terminal.Usage.InputTokens - terminal.Usage.InputDetails.CachedTokens
	if inputTokens < 0 {
		inputTokens = 0
	}
	response.Usage = Usage{
		Input:       inputTokens,
		Output:      terminal.Usage.OutputTokens,
		CacheRead:   terminal.Usage.InputDetails.CachedTokens,
		CacheWrite:  0,
		TotalTokens: terminal.Usage.TotalTokens,
	}
	response.Usage.Cost = CalculateCost(model, response.Usage)
}

func parseOpenAICodexResponseOutput(items []openAICodexResponseItem) []ContentBlock {
	blocks := make([]ContentBlock, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case "message":
			var parts []string
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					parts = append(parts, part.Text)
				case "refusal":
					parts = append(parts, part.Refusal)
				}
			}
			if len(parts) > 0 {
				blocks = append(blocks, TextContent{Text: strings.Join(parts, "")})
			}
		case "reasoning":
			if len(item.Summary) == 0 {
				continue
			}
			parts := make([]string, 0, len(item.Summary))
			for _, summary := range item.Summary {
				if strings.TrimSpace(summary.Text) != "" {
					parts = append(parts, summary.Text)
				}
			}
			if len(parts) == 0 {
				continue
			}
			signatureBytes, err := json.Marshal(item)
			if err != nil {
				signatureBytes = nil
			}
			blocks = append(blocks, ThinkingContent{
				Thinking:          strings.Join(parts, "\n\n"),
				ThinkingSignature: string(signatureBytes),
			})
		case "function_call":
			var arguments map[string]any
			if strings.TrimSpace(item.Arguments) != "" {
				_ = json.Unmarshal([]byte(item.Arguments), &arguments)
			}
			id := item.CallID
			if item.ID != "" {
				id = item.CallID + "|" + item.ID
			}
			blocks = append(blocks, ToolCall{
				ID:        id,
				Name:      item.Name,
				Arguments: arguments,
			})
		}
	}
	return blocks
}

func mapOpenAICodexStopReason(status string, content []ContentBlock) StopReason {
	switch status {
	case "completed", "queued", "in_progress", "":
		for _, block := range content {
			if _, ok := block.(ToolCall); ok {
				return StopReasonToolUse
			}
		}
		return StopReasonStop
	case "incomplete":
		return StopReasonLength
	case "failed", "cancelled":
		return StopReasonError
	default:
		return StopReasonError
	}
}

func parseOpenAICodexError(body []byte, fallback string) string {
	var payload openAICodexResponse
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error != nil && strings.TrimSpace(payload.Error.Message) != "" {
		return payload.Error.Message
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fallback
	}
	return text
}
