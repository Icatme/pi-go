package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type anthropicRequest struct {
	Model       string               `json:"model"`
	System      []anthropicTextBlock `json:"system,omitempty"`
	Messages    []anthropicMessage   `json:"messages"`
	Tools       []anthropicTool      `json:"tools,omitempty"`
	MaxTokens   int                  `json:"max_tokens"`
	Temperature *float64             `json:"temperature,omitempty"`
	Thinking    any                  `json:"thinking,omitempty"`
	ToolChoice  any                  `json:"tool_choice,omitempty"`
	Stream      bool                 `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicTextBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	Signature    string                 `json:"signature,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
}

type anthropicImageBlock struct {
	Type         string                 `json:"type"`
	Source       anthropicImageSource   `json:"source"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicRedactedThinkingBlock struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type anthropicToolUseBlock struct {
	Type  string         `json:"type"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type anthropicToolResultBlock struct {
	Type         string                 `json:"type"`
	ToolUseID    string                 `json:"tool_use_id"`
	Content      any                    `json:"content"`
	IsError      bool                   `json:"is_error,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type anthropicResponse struct {
	ID         string                   `json:"id"`
	Content    []anthropicResponseBlock `json:"content"`
	StopReason string                   `json:"stop_reason"`
	Usage      anthropicUsage           `json:"usage"`
}

type anthropicResponseBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

type anthropicErrorEnvelope struct {
	Error anthropicErrorBody `json:"error"`
}

type anthropicErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type anthropicStreamEnvelope struct {
	Type         string                  `json:"type"`
	Index        int                     `json:"index"`
	Message      *anthropicResponse      `json:"message,omitempty"`
	ContentBlock *anthropicResponseBlock `json:"content_block,omitempty"`
	Delta        *anthropicStreamDelta   `json:"delta,omitempty"`
	Usage        *anthropicStreamUsage   `json:"usage,omitempty"`
	Error        *anthropicErrorBody     `json:"error,omitempty"`
}

type anthropicStreamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Signature   string `json:"signature,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type anthropicStreamUsage struct {
	InputTokens         *int `json:"input_tokens,omitempty"`
	OutputTokens        *int `json:"output_tokens,omitempty"`
	CacheReadTokens     *int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationTokens *int `json:"cache_creation_input_tokens,omitempty"`
}

type anthropicStreamingBlockState struct {
	ContentIndex int
	Kind         string
	PartialJSON  string
}

func streamAnthropicMessages(model Model, ctx Context, options CompleteOptions) *AssistantMessageEventStream {
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
			apiKey = ResolveAPIKey(model.Provider, options.Auth)
		}
		if apiKey == "" {
			response.StopReason = StopReasonError
			response.ErrorMessage = "missing api key"
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		cacheControl := resolveAnthropicCacheControl(model.BaseURL, options.CacheRetention)
		requestBody := anthropicRequest{
			Model:     model.ID,
			Messages:  convertAnthropicMessagesWithCache(ctx.Messages, model, cacheControl),
			Tools:     convertAnthropicTools(ctx.Tools),
			MaxTokens: defaultMaxTokens(model, options.MaxTokens),
			Thinking:  buildAnthropicThinkingOptions(model, options),
			Stream:    true,
		}
		if ctx.SystemPrompt != "" {
			requestBody.System = []anthropicTextBlock{{
				Type:         "text",
				Text:         ctx.SystemPrompt,
				CacheControl: cacheControl,
			}}
		}
		if options.Temperature != nil && requestBody.Thinking == nil {
			requestBody.Temperature = options.Temperature
		}
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
			strings.TrimRight(model.BaseURL, "/")+"/v1/messages",
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
		request.Header.Set("anthropic-version", "2023-06-01")
		request.Header.Set("x-api-key", apiKey)
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
			response.ErrorMessage = parseAnthropicError(body, httpResponse.Status)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		stream.push(AssistantMessageEvent{
			Type:    AssistantMessageEventStart,
			Partial: response,
		})

		terminalSeen := false
		states := map[int]*anthropicStreamingBlockState{}
		if err := readSSEStream(httpResponse.Body, func(_ string, data string) (bool, error) {
			done, err := processAnthropicStreamEvent(data, model, &response, stream, states)
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
			response.Usage.Cost = CalculateCost(model, response.Usage)
			stream.push(AssistantMessageEvent{
				Type:    AssistantMessageEventDone,
				Reason:  response.StopReason,
				Message: response,
			})
			stream.finish(response)
		}
	}()

	return stream
}

func processAnthropicStreamEvent(
	data string,
	model Model,
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
	states map[int]*anthropicStreamingBlockState,
) (bool, error) {
	if strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "[DONE]" {
		return false, nil
	}

	var event anthropicStreamEnvelope
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return false, err
	}

	switch event.Type {
	case "message_start":
		if event.Message != nil {
			response.ResponseID = event.Message.ID
			applyAnthropicUsage(response, model, anthropicUsage{
				InputTokens:         event.Message.Usage.InputTokens,
				OutputTokens:        event.Message.Usage.OutputTokens,
				CacheReadTokens:     event.Message.Usage.CacheReadTokens,
				CacheCreationTokens: event.Message.Usage.CacheCreationTokens,
			})
		}
	case "content_block_start":
		if event.ContentBlock == nil {
			return false, nil
		}
		contentIndex := len(response.Content)
		switch event.ContentBlock.Type {
		case "text":
			response.Content = append(response.Content, TextContent{})
			states[event.Index] = &anthropicStreamingBlockState{ContentIndex: contentIndex, Kind: "text"}
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextStart, ContentIndex: contentIndex, Partial: *response})
		case "thinking":
			response.Content = append(response.Content, ThinkingContent{})
			states[event.Index] = &anthropicStreamingBlockState{ContentIndex: contentIndex, Kind: "thinking"}
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingStart, ContentIndex: contentIndex, Partial: *response})
		case "redacted_thinking":
			block := ThinkingContent{
				Thinking:          "[Reasoning redacted]",
				ThinkingSignature: event.ContentBlock.Data,
				Redacted:          true,
			}
			response.Content = append(response.Content, block)
			states[event.Index] = &anthropicStreamingBlockState{ContentIndex: contentIndex, Kind: "thinking"}
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingStart, ContentIndex: contentIndex, Partial: *response})
		case "tool_use":
			var input map[string]any
			if len(event.ContentBlock.Input) > 0 {
				input = parseStreamingJSONObject(string(event.ContentBlock.Input))
			}
			response.Content = append(response.Content, ToolCall{
				ID:        event.ContentBlock.ID,
				Name:      event.ContentBlock.Name,
				Arguments: input,
			})
			states[event.Index] = &anthropicStreamingBlockState{ContentIndex: contentIndex, Kind: "tool", PartialJSON: string(event.ContentBlock.Input)}
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallStart, ContentIndex: contentIndex, Partial: *response})
		}
	case "content_block_delta":
		state := states[event.Index]
		if state == nil || event.Delta == nil {
			return false, nil
		}
		switch event.Delta.Type {
		case "text_delta":
			if block, ok := response.Content[state.ContentIndex].(TextContent); ok {
				block.Text += event.Delta.Text
				response.Content[state.ContentIndex] = block
				stream.push(AssistantMessageEvent{
					Type:         AssistantMessageEventTextDelta,
					ContentIndex: state.ContentIndex,
					Delta:        event.Delta.Text,
					Partial:      *response,
				})
			}
		case "thinking_delta":
			if block, ok := response.Content[state.ContentIndex].(ThinkingContent); ok {
				block.Thinking += event.Delta.Thinking
				response.Content[state.ContentIndex] = block
				stream.push(AssistantMessageEvent{
					Type:         AssistantMessageEventThinkingDelta,
					ContentIndex: state.ContentIndex,
					Delta:        event.Delta.Thinking,
					Partial:      *response,
				})
			}
		case "input_json_delta":
			state.PartialJSON += event.Delta.PartialJSON
			if block, ok := response.Content[state.ContentIndex].(ToolCall); ok {
				block.Arguments = parseStreamingJSONObject(state.PartialJSON)
				response.Content[state.ContentIndex] = block
				stream.push(AssistantMessageEvent{
					Type:         AssistantMessageEventToolCallDelta,
					ContentIndex: state.ContentIndex,
					Delta:        event.Delta.PartialJSON,
					Partial:      *response,
				})
			}
		case "signature_delta":
			if block, ok := response.Content[state.ContentIndex].(ThinkingContent); ok {
				block.ThinkingSignature += event.Delta.Signature
				response.Content[state.ContentIndex] = block
			}
		}
	case "content_block_stop":
		state := states[event.Index]
		if state == nil {
			return false, nil
		}
		switch block := response.Content[state.ContentIndex].(type) {
		case TextContent:
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventTextEnd,
				ContentIndex: state.ContentIndex,
				Content:      block.Text,
				Partial:      *response,
			})
		case ThinkingContent:
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventThinkingEnd,
				ContentIndex: state.ContentIndex,
				Content:      block.Thinking,
				Partial:      *response,
			})
		case ToolCall:
			if state.PartialJSON != "" {
				block.Arguments = parseStreamingJSONObject(state.PartialJSON)
				response.Content[state.ContentIndex] = block
			}
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventToolCallEnd,
				ContentIndex: state.ContentIndex,
				ToolCall:     block,
				Partial:      *response,
			})
		}
	case "message_delta":
		if event.Delta != nil && strings.TrimSpace(event.Delta.StopReason) != "" {
			response.StopReason = mapAnthropicStopReason(event.Delta.StopReason, response.Content)
		}
		if event.Usage != nil {
			usage := anthropicUsage{}
			if event.Usage.InputTokens != nil {
				usage.InputTokens = *event.Usage.InputTokens
			} else {
				usage.InputTokens = response.Usage.Input
			}
			if event.Usage.OutputTokens != nil {
				usage.OutputTokens = *event.Usage.OutputTokens
			} else {
				usage.OutputTokens = response.Usage.Output
			}
			if event.Usage.CacheReadTokens != nil {
				usage.CacheReadTokens = *event.Usage.CacheReadTokens
			} else {
				usage.CacheReadTokens = response.Usage.CacheRead
			}
			if event.Usage.CacheCreationTokens != nil {
				usage.CacheCreationTokens = *event.Usage.CacheCreationTokens
			} else {
				usage.CacheCreationTokens = response.Usage.CacheWrite
			}
			applyAnthropicUsage(response, model, usage)
		}
	case "message_stop":
		response.Usage.Cost = CalculateCost(model, response.Usage)
		stream.push(AssistantMessageEvent{
			Type:    AssistantMessageEventDone,
			Reason:  response.StopReason,
			Message: *response,
		})
		stream.finish(*response)
		return true, nil
	case "error":
		response.StopReason = StopReasonError
		if event.Error != nil && strings.TrimSpace(event.Error.Message) != "" {
			response.ErrorMessage = event.Error.Message
		} else {
			response.ErrorMessage = "anthropic stream error"
		}
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

func applyAnthropicUsage(response *AssistantMessage, model Model, usage anthropicUsage) {
	response.Usage = Usage{
		Input:       usage.InputTokens,
		Output:      usage.OutputTokens,
		CacheRead:   usage.CacheReadTokens,
		CacheWrite:  usage.CacheCreationTokens,
		TotalTokens: usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheCreationTokens,
	}
	response.Usage.Cost = CalculateCost(model, response.Usage)
}

func defaultMaxTokens(model Model, override int) int {
	if override > 0 {
		return override
	}
	if model.MaxTokens > 0 {
		return model.MaxTokens / 3
	}
	return 4096
}

func convertAnthropicMessages(messages []Message, model Model) []anthropicMessage {
	return convertAnthropicMessagesWithCache(messages, model, nil)
}

func resolveAnthropicCacheControl(baseURL string, retention CacheRetention) *anthropicCacheControl {
	switch retention {
	case CacheRetentionNone:
		return nil
	case CacheRetentionLong:
		cacheControl := &anthropicCacheControl{Type: "ephemeral"}
		if strings.Contains(baseURL, "api.anthropic.com") {
			cacheControl.TTL = "1h"
		}
		return cacheControl
	case CacheRetentionShort:
		return &anthropicCacheControl{Type: "ephemeral"}
	default:
		return nil
	}
}

func buildAnthropicThinkingOptions(model Model, options CompleteOptions) any {
	if options.Reasoning == "" {
		return nil
	}

	budget := options.ThinkingBudgetTokens
	if budget <= 0 {
		switch options.Reasoning {
		case ThinkingLevelMinimal:
			budget = 1024
		case ThinkingLevelLow:
			budget = 2048
		case ThinkingLevelMedium:
			budget = 4096
		case ThinkingLevelHigh:
			budget = 8192
		case ThinkingLevelXHigh:
			if SupportsXHigh(model) {
				budget = 16384
			} else {
				budget = 8192
			}
		default:
			budget = 4096
		}
	}

	maxBudget := defaultMaxTokens(model, options.MaxTokens)
	if maxBudget > 0 && budget > maxBudget {
		budget = maxBudget
	}
	if budget <= 0 {
		return nil
	}

	return map[string]any{
		"type":          "enabled",
		"budget_tokens": budget,
	}
}

func convertAnthropicMessagesWithCache(messages []Message, model Model, cacheControl *anthropicCacheControl) []anthropicMessage {
	transformed := TransformMessages(messages, model, func(id string, _ Model, _ AssistantMessage) string {
		return NormalizeSimpleToolCallID(id)
	})
	params := make([]anthropicMessage, 0, len(transformed))

	for i := 0; i < len(transformed); i++ {
		switch typed := transformed[i].(type) {
		case UserMessage:
			content := convertUserContent(typed.Content, model)
			if content == nil {
				continue
			}
			params = append(params, anthropicMessage{
				Role:    "user",
				Content: content,
			})
		case AssistantMessage:
			content := convertAssistantContent(typed.Content)
			if len(content) == 0 {
				continue
			}
			params = append(params, anthropicMessage{
				Role:    "assistant",
				Content: content,
			})
		case ToolResultMessage:
			toolResults := []anthropicToolResultBlock{{
				Type:      "tool_result",
				ToolUseID: typed.ToolCallID,
				Content:   convertToolResultContent(typed.Content, model),
				IsError:   typed.IsError,
			}}
			for i+1 < len(transformed) {
				next, ok := transformed[i+1].(ToolResultMessage)
				if !ok {
					break
				}
				toolResults = append(toolResults, anthropicToolResultBlock{
					Type:      "tool_result",
					ToolUseID: next.ToolCallID,
					Content:   convertToolResultContent(next.Content, model),
					IsError:   next.IsError,
				})
				i++
			}
			params = append(params, anthropicMessage{
				Role:    "user",
				Content: toolResults,
			})
		}
	}

	applyAnthropicMessageCacheControl(params, cacheControl)
	return params
}

func applyAnthropicMessageCacheControl(messages []anthropicMessage, cacheControl *anthropicCacheControl) {
	if cacheControl == nil {
		return
	}

	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "user" {
			continue
		}
		messages[index].Content = applyAnthropicContentCacheControl(messages[index].Content, cacheControl)
		return
	}
}

func applyAnthropicContentCacheControl(content any, cacheControl *anthropicCacheControl) any {
	if cacheControl == nil {
		return content
	}

	switch typed := content.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return content
		}
		return []any{anthropicTextBlock{
			Type:         "text",
			Text:         typed,
			CacheControl: cacheControl,
		}}
	case []any:
		if len(typed) == 0 {
			return content
		}
		lastIndex := len(typed) - 1
		switch block := typed[lastIndex].(type) {
		case anthropicTextBlock:
			block.CacheControl = cacheControl
			typed[lastIndex] = block
		case anthropicImageBlock:
			block.CacheControl = cacheControl
			typed[lastIndex] = block
		case anthropicToolResultBlock:
			block.CacheControl = cacheControl
			typed[lastIndex] = block
		}
		return typed
	case []anthropicToolResultBlock:
		if len(typed) == 0 {
			return content
		}
		typed[len(typed)-1].CacheControl = cacheControl
		blocks := make([]any, 0, len(typed))
		for _, block := range typed {
			blocks = append(blocks, block)
		}
		return blocks
	default:
		return content
	}
}

func convertUserContent(value any, model Model) any {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return typed
	case []ContentBlock:
		blocks := convertTextAndImageBlocks(typed, model)
		if len(blocks) == 0 {
			return nil
		}
		return blocks
	default:
		return nil
	}
}

func convertAssistantContent(blocks []ContentBlock) []any {
	result := make([]any, 0, len(blocks))
	for _, block := range blocks {
		switch typed := block.(type) {
		case TextContent:
			if strings.TrimSpace(typed.Text) == "" {
				continue
			}
			result = append(result, anthropicTextBlock{
				Type: "text",
				Text: typed.Text,
			})
		case ThinkingContent:
			if typed.Redacted {
				result = append(result, anthropicRedactedThinkingBlock{
					Type: "redacted_thinking",
					Data: typed.ThinkingSignature,
				})
				continue
			}
			if strings.TrimSpace(typed.Thinking) == "" {
				continue
			}
			if strings.TrimSpace(typed.ThinkingSignature) == "" {
				result = append(result, anthropicTextBlock{
					Type: "text",
					Text: typed.Thinking,
				})
				continue
			}
			result = append(result, anthropicThinkingBlock{
				Type:      "thinking",
				Thinking:  typed.Thinking,
				Signature: typed.ThinkingSignature,
			})
		case ToolCall:
			result = append(result, anthropicToolUseBlock{
				Type:  "tool_use",
				ID:    typed.ID,
				Name:  typed.Name,
				Input: typed.Arguments,
			})
		}
	}
	return result
}

func convertToolResultContent(blocks []ContentBlock, model Model) any {
	converted := convertTextAndImageBlocks(blocks, model)
	if len(converted) == 0 {
		return "No result provided"
	}
	if len(converted) == 1 {
		if textBlock, ok := converted[0].(anthropicTextBlock); ok {
			return textBlock.Text
		}
	}
	return converted
}

func convertTextAndImageBlocks(blocks []ContentBlock, model Model) []any {
	result := make([]any, 0, len(blocks))
	for _, block := range blocks {
		switch typed := block.(type) {
		case TextContent:
			if strings.TrimSpace(typed.Text) == "" {
				continue
			}
			result = append(result, anthropicTextBlock{
				Type: "text",
				Text: typed.Text,
			})
		case ImageContent:
			if !modelSupportsInput(model, InputImage) {
				continue
			}
			result = append(result, anthropicImageBlock{
				Type: "image",
				Source: anthropicImageSource{
					Type:      "base64",
					MediaType: typed.MIMEType,
					Data:      typed.Data,
				},
			})
		}
	}

	if len(result) == 0 {
		return nil
	}

	hasText := false
	for _, block := range result {
		if _, ok := block.(anthropicTextBlock); ok {
			hasText = true
			break
		}
	}
	if !hasText {
		result = append([]any{anthropicTextBlock{Type: "text", Text: "(see attached image)"}}, result...)
	}
	return result
}

func modelSupportsInput(model Model, inputType InputType) bool {
	for _, candidate := range model.Input {
		if candidate == inputType {
			return true
		}
	}
	return false
}

func convertAnthropicTools(tools []Tool) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}

	result := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		toolSchema := tool.Parameters
		if toolSchema == nil {
			toolSchema = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		result = append(result, anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: toolSchema,
		})
	}
	return result
}

func parseAnthropicError(body []byte, fallback string) string {
	var envelope anthropicErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && strings.TrimSpace(envelope.Error.Message) != "" {
		return envelope.Error.Message
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fallback
	}
	return text
}

func parseAnthropicResponseContent(blocks []anthropicResponseBlock) []ContentBlock {
	result := make([]ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			result = append(result, TextContent{Text: block.Text})
		case "thinking":
			result = append(result, ThinkingContent{
				Thinking:          block.Thinking,
				ThinkingSignature: block.Signature,
			})
		case "redacted_thinking":
			result = append(result, ThinkingContent{
				Thinking:          "[Reasoning redacted]",
				ThinkingSignature: block.Data,
				Redacted:          true,
			})
		case "tool_use":
			var input map[string]any
			if len(block.Input) > 0 {
				_ = json.Unmarshal(block.Input, &input)
			}
			result = append(result, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: input,
			})
		}
	}
	return result
}

func mapAnthropicStopReason(reason string, content []ContentBlock) StopReason {
	switch reason {
	case "end_turn", "stop_sequence", "pause_turn", "":
		for _, block := range content {
			if _, ok := block.(ToolCall); ok {
				return StopReasonToolUse
			}
		}
		return StopReasonStop
	case "max_tokens":
		return StopReasonLength
	case "tool_use":
		return StopReasonToolUse
	case "refusal", "sensitive":
		return StopReasonError
	default:
		return StopReasonError
	}
}
