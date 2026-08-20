package pigo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

var errOpenAICompletionsStreamMissingTerminal = errors.New("openai completions stream ended without finish_reason")

type openAICompletionsRequest struct {
	Model                string                          `json:"model"`
	Messages             []openAICompletionsMessage      `json:"messages"`
	Stream               bool                            `json:"stream"`
	StreamOptions        *openAICompletionsStreamOptions `json:"stream_options,omitempty"`
	Store                *bool                           `json:"store,omitempty"`
	MaxTokens            int                             `json:"max_tokens,omitempty"`
	MaxCompletionTokens  int                             `json:"max_completion_tokens,omitempty"`
	Temperature          *float64                        `json:"temperature,omitempty"`
	Tools                []openAICompletionsTool         `json:"tools,omitempty"`
	ToolChoice           any                             `json:"tool_choice,omitempty"`
	ResponseFormat       any                             `json:"response_format,omitempty"`
	ReasoningEffort      string                          `json:"reasoning_effort,omitempty"`
	Thinking             *openAICompletionsThinking      `json:"thinking,omitempty"`
	EnableThinking       *bool                           `json:"enable_thinking,omitempty"`
	PromptCacheKey       string                          `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string                          `json:"prompt_cache_retention,omitempty"`
}

type openAICompletionsStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAICompletionsThinking struct {
	Type string `json:"type"`
}

type openAICompletionsMessage struct {
	Role             string                      `json:"role"`
	Content          any                         `json:"content,omitempty"`
	ToolCallID       string                      `json:"tool_call_id,omitempty"`
	Name             string                      `json:"name,omitempty"`
	ToolCalls        []openAICompletionsToolCall `json:"tool_calls,omitempty"`
	ReasoningDetails []json.RawMessage           `json:"reasoning_details,omitempty"`
	ReasoningContent *string                     `json:"reasoning_content,omitempty"`
	Reasoning        string                      `json:"reasoning,omitempty"`
	ReasoningText    string                      `json:"reasoning_text,omitempty"`
}

type openAICompletionsTool struct {
	Type     string                          `json:"type"`
	Function openAICompletionsToolDefinition `json:"function"`
}

type openAICompletionsToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters"`
}

type openAICompletionsToolCall struct {
	ID       string                        `json:"id,omitempty"`
	Type     string                        `json:"type"`
	Function openAICompletionsToolFunction `json:"function"`
}

type openAICompletionsToolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAICompletionsChunk struct {
	ID      string                    `json:"id"`
	Model   string                    `json:"model"`
	Choices []openAICompletionsChoice `json:"choices"`
	Usage   *openAICompletionsUsage   `json:"usage,omitempty"`
	Error   *openAICompletionsError   `json:"error,omitempty"`
}

type openAICompletionsChoice struct {
	Delta        openAICompletionsDelta  `json:"delta"`
	Message      *openAICompletionsDelta `json:"message,omitempty"`
	FinishReason string                  `json:"finish_reason"`
	Usage        *openAICompletionsUsage `json:"usage,omitempty"`
}

type openAICompletionsDelta struct {
	Content          string                           `json:"content,omitempty"`
	ReasoningContent string                           `json:"reasoning_content,omitempty"`
	Reasoning        string                           `json:"reasoning,omitempty"`
	ReasoningText    string                           `json:"reasoning_text,omitempty"`
	ReasoningDetails json.RawMessage                  `json:"reasoning_details,omitempty"`
	ToolCalls        []openAICompletionsDeltaToolCall `json:"tool_calls,omitempty"`
}

type openAICompletionsDeltaToolCall struct {
	Index    int                           `json:"index"`
	ID       string                        `json:"id,omitempty"`
	Type     string                        `json:"type,omitempty"`
	Function openAICompletionsToolFunction `json:"function,omitempty"`
}

type openAICompletionsUsage struct {
	PromptTokens             int                                  `json:"prompt_tokens"`
	CompletionTokens         int                                  `json:"completion_tokens"`
	TotalTokens              int                                  `json:"total_tokens"`
	CachedTokens             *int                                 `json:"cached_tokens"`
	PromptCacheHitTokens     *int                                 `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens    int                                  `json:"prompt_cache_miss_tokens"`
	CacheReadInputTokens     *int                                 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int                                 `json:"cache_creation_input_tokens"`
	PromptTokensDetails      openAICompletionsPromptTokensDetails `json:"prompt_tokens_details"`
}

type openAICompletionsPromptTokensDetails struct {
	CachedTokens        *int `json:"cached_tokens"`
	CacheWriteTokens    *int `json:"cache_write_tokens"`
	CacheCreationTokens *int `json:"cache_creation_tokens"`
}

type openAICompletionsErrorEnvelope struct {
	Error openAICompletionsError `json:"error"`
}

type openAICompletionsError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

type resolvedOpenAICompletionsCompat struct {
	SupportsStore                               bool
	SupportsDeveloperRole                       bool
	SupportsReasoningEffort                     bool
	SupportsUsageInStreaming                    bool
	MaxTokensField                              string
	RequiresToolResultName                      bool
	RequiresAssistantAfterToolResult            bool
	RequiresThinkingAsText                      bool
	RequiresReasoningContentOnAssistantMessages bool
	ThinkingFormat                              string
	SupportsStrictMode                          bool
	SupportsLongCacheRetention                  bool
}

type openAICompletionsToolCallState struct {
	ID           string
	Name         string
	Arguments    string
	ContentIndex int
	Started      bool
}

type openAICompletionsStreamState struct {
	TextIndex         int
	TextStarted       bool
	Text              strings.Builder
	ThinkingIndex     int
	ThinkingStarted   bool
	Thinking          strings.Builder
	ThinkingSignature string
	ReasoningDetails  []json.RawMessage
	ToolCalls         map[int]*openAICompletionsToolCallState
	FinishSeen        bool
	DoneSeen          bool
}

func streamOpenAICompletions(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
	options = normalizeOpenAICompletionsProviderStreamOptions(model, NormalizeProviderStreamOptions(model, options))
	stream := newAssistantMessageEventStream()
	stream.setObserver(options.Observer, model)

	response := AssistantMessage{
		API:        model.API,
		Provider:   model.Provider,
		Model:      model.ID,
		StopReason: StopReasonStop,
		Timestamp:  time.Now().UTC(),
	}

	go func() {
		requestBody := buildOpenAICompletionsRequest(model, ctx, options)
		payload := any(requestBody)
		if options.OnPayload != nil {
			if next := options.OnPayload(payload, model); next != nil {
				payload = next
			}
		}

		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		requestContext, cancel := providerRequestContext(options.RequestContext, options.TimeoutMs)
		defer cancel()
		requestContext = stream.startRequest(requestContext, payload)

		apiKey := options.APIKey
		if apiKey == "" {
			resolved, resolveErr := ResolveAuthorization(model.Provider, options.Auth, options.HTTPClient, requestContext)
			if resolveErr != nil {
				applyRequestError(&response, resolveErr)
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
				stream.finish(response)
				return
			}
			apiKey = resolved
		}
		if apiKey == "" {
			apiKey = ResolveAPIKey(model.Provider, options.Auth)
		}
		if apiKey == "" {
			apiKey = GetEnvAPIKey(model.Provider)
		}
		if apiKey == "" {
			applyRequestError(&response, errors.New("missing api key"))
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: response})
		baselineResponse := cloneAssistantMessage(response)
		state := &openAICompletionsStreamState{ToolCalls: map[int]*openAICompletionsToolCallState{}}
		headers := mergeRequestHeaders(
			map[string]string{
				"Authorization": "Bearer " + apiKey,
				"Content-Type":  "application/json",
				"Accept":        "text/event-stream",
			},
			model.Headers,
			options.Headers,
		)
		client := HTTPStreamClient{
			HTTPClient:    options.HTTPClient,
			MaxRetries:    maxInt(0, options.MaxRetries),
			MaxRetryDelay: time.Duration(options.MaxRetryDelay) * time.Millisecond,
		}
		err = client.postStream(requestContext, resolveOpenAICompletionsURL(model.BaseURL), httpStreamRequest{
			Headers: headers,
			Body:    bodyBytes,
			OnEvent: func(_ string, data string) (bool, error) {
				return processOpenAICompletionsStreamEvent(data, model, &response, stream, state)
			},
			OnResponse: func(httpResponse *http.Response) {
				if options.OnResponse != nil {
					options.OnResponse(ProviderResponse{
						Status:  httpResponse.StatusCode,
						Headers: cloneOpenAICompletionsHTTPHeaders(httpResponse.Header),
					}, model)
				}
			},
			ShouldRetry: shouldRetryOpenAIResponsesRequest,
			ShouldRetryResponse: func(status int, body []byte, message string) bool {
				return shouldRetryOpenAIProviderResponse(status, body, message)
			},
			CanRetryStreamError: func() bool {
				return len(response.Content) == 0 && len(response.HostedToolExecutions) == 0
			},
			OnStreamRetry: func() {
				response = cloneAssistantMessage(baselineResponse)
				*state = openAICompletionsStreamState{ToolCalls: map[int]*openAICompletionsToolCallState{}}
			},
			ParseError: func(body []byte, status string) string {
				return fmt.Sprintf("openai completions upstream returned %s: %s", status, parseOpenAICompletionsError(body))
			},
		})
		if err == nil && !state.FinishSeen {
			err = errOpenAICompletionsStreamMissingTerminal
		}
		if err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		finalizeOpenAICompletionsResponse(&response, stream, state)
	}()

	return stream
}

func streamSimpleOpenAICompletions(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
	return streamOpenAICompletions(model, ctx, BuildProviderStreamOptions(model, options))
}

func buildOpenAICompletionsRequest(model Model, ctx Context, options ProviderStreamOptions) openAICompletionsRequest {
	resolvedOptions := resolveOpenAICompletionsProviderOptions(model, options).toProviderStreamOptions(model)
	compat := resolveOpenAICompletionsCompat(model)
	request := openAICompletionsRequest{
		Model:    model.ID,
		Messages: convertOpenAICompletionsMessages(model, ctx, compat),
		Stream:   true,
		Tools:    convertOpenAICompletionsTools(ctx.Tools),
	}
	if compat.SupportsUsageInStreaming {
		request.StreamOptions = &openAICompletionsStreamOptions{IncludeUsage: true}
	}
	if compat.SupportsStore {
		store := false
		request.Store = &store
	}
	if resolvedOptions.MaxTokens > 0 {
		if compat.MaxTokensField == "max_tokens" {
			request.MaxTokens = resolvedOptions.MaxTokens
		} else {
			request.MaxCompletionTokens = resolvedOptions.MaxTokens
		}
	}
	request.Temperature = resolvedOptions.Temperature
	request.ToolChoice = buildOpenAICompletionsToolChoice(resolvedOptions.ToolChoice)
	request.ResponseFormat = buildOpenAICompletionsResponseFormat(resolvedOptions.ResponseFormat)
	if resolvedOptions.CacheRetention == CacheRetentionLong && compat.SupportsLongCacheRetention && resolvedOptions.SessionID != "" {
		request.PromptCacheKey = resolvedOptions.SessionID
		request.PromptCacheRetention = "24h"
	}
	applyOpenAICompletionsReasoning(&request, model, resolvedOptions.Reasoning, compat)
	return request
}

func resolveOpenAICompletionsCompat(model Model) resolvedOpenAICompletionsCompat {
	resolved := resolvedOpenAICompletionsCompat{
		SupportsStore:              true,
		SupportsDeveloperRole:      true,
		SupportsReasoningEffort:    true,
		SupportsUsageInStreaming:   true,
		MaxTokensField:             "max_completion_tokens",
		ThinkingFormat:             "openai",
		SupportsStrictMode:         true,
		SupportsLongCacheRetention: true,
	}
	compat, ok := model.Compat.(*OpenAICompletionsCompat)
	if !ok || compat == nil {
		return resolved
	}
	if compat.SupportsStore != nil {
		resolved.SupportsStore = *compat.SupportsStore
	}
	if compat.SupportsDeveloperRole != nil {
		resolved.SupportsDeveloperRole = *compat.SupportsDeveloperRole
	}
	if compat.SupportsReasoningEffort != nil {
		resolved.SupportsReasoningEffort = *compat.SupportsReasoningEffort
	}
	if compat.SupportsUsageInStreaming != nil {
		resolved.SupportsUsageInStreaming = *compat.SupportsUsageInStreaming
	}
	if compat.MaxTokensField != "" {
		resolved.MaxTokensField = compat.MaxTokensField
	}
	if compat.RequiresToolResultName != nil {
		resolved.RequiresToolResultName = *compat.RequiresToolResultName
	}
	if compat.RequiresAssistantAfterToolResult != nil {
		resolved.RequiresAssistantAfterToolResult = *compat.RequiresAssistantAfterToolResult
	}
	if compat.RequiresThinkingAsText != nil {
		resolved.RequiresThinkingAsText = *compat.RequiresThinkingAsText
	}
	if compat.RequiresReasoningContentOnAssistantMessages != nil {
		resolved.RequiresReasoningContentOnAssistantMessages = *compat.RequiresReasoningContentOnAssistantMessages
	}
	if compat.ThinkingFormat != "" {
		resolved.ThinkingFormat = compat.ThinkingFormat
	}
	if compat.SupportsStrictMode != nil {
		resolved.SupportsStrictMode = *compat.SupportsStrictMode
	}
	if compat.SupportsLongCacheRetention != nil {
		resolved.SupportsLongCacheRetention = *compat.SupportsLongCacheRetention
	}
	return resolved
}

func applyOpenAICompletionsReasoning(request *openAICompletionsRequest, model Model, level ThinkingLevel, compat resolvedOpenAICompletionsCompat) {
	if request == nil || !model.Reasoning {
		return
	}
	effort := ""
	if level != "" {
		effort = strings.TrimSpace(model.ThinkingLevelMap[ModelThinkingLevel(level)])
		if effort == "" {
			effort = string(level)
		}
	} else if mapped := strings.TrimSpace(model.ThinkingLevelMap[ModelThinkingLevelOff]); mapped != "" {
		effort = mapped
	}

	switch compat.ThinkingFormat {
	case "deepseek":
		if level != "" {
			request.Thinking = &openAICompletionsThinking{Type: "enabled"}
		} else if off, mapped := model.ThinkingLevelMap[ModelThinkingLevelOff]; !mapped || off != "" {
			request.Thinking = &openAICompletionsThinking{Type: "disabled"}
		}
		if level != "" && compat.SupportsReasoningEffort {
			request.ReasoningEffort = effort
		}
	case "qwen":
		enabled := level != ""
		request.EnableThinking = &enabled
		if enabled && compat.SupportsReasoningEffort {
			request.ReasoningEffort = effort
		}
	default:
		if effort != "" && compat.SupportsReasoningEffort {
			request.ReasoningEffort = effort
		}
	}
}

func buildOpenAICompletionsToolChoice(choice string) any {
	switch normalized := strings.TrimSpace(choice); normalized {
	case "":
		return nil
	case "any":
		return "required"
	case "auto", "none", "required":
		return normalized
	default:
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": normalized,
			},
		}
	}
}

func buildOpenAICompletionsResponseFormat(format *ResponseFormat) any {
	if format == nil {
		return nil
	}
	if format.Type == ResponseFormatJSON {
		return map[string]any{"type": string(format.Type)}
	}
	return map[string]any{
		"type": string(format.Type),
		"json_schema": map[string]any{
			"name":   format.Name,
			"schema": json.RawMessage(format.JSONSchema),
			"strict": format.Strict,
		},
	}
}

func convertOpenAICompletionsMessages(model Model, ctx Context, compat resolvedOpenAICompletionsCompat) []openAICompletionsMessage {
	transformed := TransformMessages(ctx.Messages, model, func(id string, _ Model, _ AssistantMessage) string {
		return NormalizeSimpleToolCallID(id)
	})
	messages := make([]openAICompletionsMessage, 0, len(transformed)+1)
	if strings.TrimSpace(ctx.SystemPrompt) != "" {
		role := "system"
		if model.Reasoning && compat.SupportsDeveloperRole {
			role = "developer"
		}
		messages = append(messages, openAICompletionsMessage{Role: role, Content: ctx.SystemPrompt})
	}

	lastRole := ""
	for messageIndex := 0; messageIndex < len(transformed); messageIndex++ {
		message := transformed[messageIndex]
		if compat.RequiresAssistantAfterToolResult && lastRole == "tool" {
			if _, ok := message.(UserMessage); ok {
				messages = append(messages, openAICompletionsMessage{Role: "assistant", Content: "I have processed the tool results."})
			}
		}
		switch typed := message.(type) {
		case UserMessage:
			if content := openAICompletionsUserContent(typed.Content); content != nil {
				messages = append(messages, openAICompletionsMessage{Role: "user", Content: content})
				lastRole = "user"
			}
		case AssistantMessage:
			converted, ok := openAICompletionsAssistantMessage(model, typed, compat)
			if ok {
				messages = append(messages, converted)
				lastRole = "assistant"
			}
		case ToolResultMessage:
			var images []any
			for {
				textResult, toolImages := openAICompletionsToolResultContent(typed.Content)
				if textResult == "" {
					if len(toolImages) > 0 {
						textResult = "(see attached image)"
					} else {
						textResult = "(no tool output)"
					}
				}
				toolMessage := openAICompletionsMessage{
					Role:       "tool",
					Content:    textResult,
					ToolCallID: typed.ToolCallID,
				}
				if compat.RequiresToolResultName {
					toolMessage.Name = typed.ToolName
				}
				messages = append(messages, toolMessage)
				lastRole = "tool"
				if modelSupportsImages(model) {
					images = append(images, toolImages...)
				}

				if messageIndex+1 >= len(transformed) {
					break
				}
				next, ok := transformed[messageIndex+1].(ToolResultMessage)
				if !ok {
					break
				}
				messageIndex++
				typed = next
			}

			if len(images) > 0 {
				content := make([]any, 0, len(images)+1)
				content = append(content, map[string]any{"type": "text", "text": "Attached image(s) from tool result:"})
				content = append(content, images...)
				if compat.RequiresAssistantAfterToolResult {
					messages = append(messages, openAICompletionsMessage{Role: "assistant", Content: "I have processed the tool results."})
				}
				messages = append(messages, openAICompletionsMessage{Role: "user", Content: content})
				lastRole = "user"
			}
		}
	}
	return messages
}

func openAICompletionsUserContent(content any) any {
	switch typed := content.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return typed
	case []ContentBlock:
		parts := make([]any, 0, len(typed))
		for _, block := range typed {
			switch block := block.(type) {
			case TextContent:
				if block.Text != "" {
					parts = append(parts, map[string]any{"type": "text", "text": block.Text})
				}
			case ImageContent:
				parts = append(parts, openAICompletionsImagePart(block))
			}
		}
		if len(parts) == 0 {
			return nil
		}
		return parts
	default:
		text := fmt.Sprint(content)
		if text == "" {
			return nil
		}
		return text
	}
}

func openAICompletionsAssistantMessage(model Model, message AssistantMessage, compat resolvedOpenAICompletionsCompat) (openAICompletionsMessage, bool) {
	converted := openAICompletionsMessage{Role: "assistant"}
	var text strings.Builder
	var thinking strings.Builder
	thinkingSignature := ""
	for _, block := range message.Content {
		switch typed := block.(type) {
		case TextContent:
			text.WriteString(typed.Text)
		case ThinkingContent:
			if len(converted.ReasoningDetails) == 0 {
				converted.ReasoningDetails = parseOpenAICompletionsReasoningDetails(typed.ThinkingSignature)
			}
			if compat.RequiresThinkingAsText {
				if text.Len() > 0 {
					text.WriteString("\n\n")
				}
				text.WriteString(typed.Thinking)
				continue
			}
			thinking.WriteString(typed.Thinking)
			if thinkingSignature == "" {
				thinkingSignature = typed.ThinkingSignature
			}
		case ToolCall:
			converted.ToolCalls = append(converted.ToolCalls, openAICompletionsToolCall{
				ID:   typed.ID,
				Type: "function",
				Function: openAICompletionsToolFunction{
					Name:      typed.Name,
					Arguments: mustJSON(typed.Arguments),
				},
			})
		}
	}
	if text.Len() > 0 {
		converted.Content = text.String()
	}
	if thinking.Len() > 0 && len(converted.ReasoningDetails) == 0 {
		if model.Provider == "opencode-go" && thinkingSignature == "reasoning" {
			thinkingSignature = "reasoning_content"
		}
		switch thinkingSignature {
		case "reasoning":
			converted.Reasoning = thinking.String()
		case "reasoning_text":
			converted.ReasoningText = thinking.String()
		default:
			reasoningContent := thinking.String()
			converted.ReasoningContent = &reasoningContent
		}
	}
	if compat.RequiresReasoningContentOnAssistantMessages && model.Reasoning && converted.ReasoningContent == nil {
		reasoningContent := ""
		converted.ReasoningContent = &reasoningContent
	}
	return converted, converted.Content != nil || len(converted.ToolCalls) > 0 || thinking.Len() > 0
}

func openAICompletionsToolResultContent(content []ContentBlock) (string, []any) {
	textParts := make([]string, 0, len(content))
	images := make([]any, 0)
	for _, block := range content {
		switch typed := block.(type) {
		case TextContent:
			if typed.Text != "" {
				textParts = append(textParts, typed.Text)
			}
		case ImageContent:
			images = append(images, openAICompletionsImagePart(typed))
		}
	}
	return strings.Join(textParts, "\n"), images
}

func openAICompletionsImagePart(image ImageContent) map[string]any {
	return map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "data:" + image.MIMEType + ";base64," + image.Data,
		},
	}
}

func convertOpenAICompletionsTools(tools []Tool) []openAICompletionsTool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]openAICompletionsTool, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result = append(result, openAICompletionsTool{
			Type: "function",
			Function: openAICompletionsToolDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	return result
}

func processOpenAICompletionsStreamEvent(data string, model Model, response *AssistantMessage, stream *AssistantMessageEventStream, state *openAICompletionsStreamState) (bool, error) {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false, nil
	}
	if trimmed == "[DONE]" {
		state.DoneSeen = true
		return true, nil
	}

	var chunk openAICompletionsChunk
	if err := json.Unmarshal([]byte(trimmed), &chunk); err != nil {
		return false, err
	}
	if chunk.Error != nil && strings.TrimSpace(chunk.Error.Message) != "" {
		return true, errors.New(chunk.Error.Message)
	}
	if chunk.ID != "" {
		response.ResponseID = chunk.ID
	}
	if chunk.Model != "" && chunk.Model != model.ID {
		response.ResponseModel = chunk.Model
	}
	if chunk.Usage != nil {
		applyOpenAICompletionsUsage(response, model, *chunk.Usage)
	}

	for _, choice := range chunk.Choices {
		delta := choice.Delta
		messageFallback := choice.Message != nil
		if messageFallback {
			delta = *choice.Message
		}
		if choice.Usage != nil && chunk.Usage == nil {
			applyOpenAICompletionsUsage(response, model, *choice.Usage)
		}
		if reasoning, signature := openAICompletionsReasoningDelta(delta, model.Provider); reasoning != "" {
			appendOpenAICompletionsThinking(response, stream, state, reasoning, signature)
		}
		appendOpenAICompletionsReasoningDetails(response, stream, state, delta.ReasoningDetails)
		if delta.Content != "" {
			appendOpenAICompletionsText(response, stream, state, delta.Content)
		}
		for index, toolDelta := range delta.ToolCalls {
			if messageFallback {
				toolDelta.Index = index
			}
			appendOpenAICompletionsToolCall(response, stream, state, toolDelta)
		}
		if strings.TrimSpace(choice.FinishReason) != "" {
			state.FinishSeen = true
			response.StopReason = mapOpenAICompletionsFinishReason(choice.FinishReason, response.Content)
			if response.StopReason == StopReasonError {
				return true, fmt.Errorf("unsupported finish_reason %q", choice.FinishReason)
			}
		}
	}
	return false, nil
}

func openAICompletionsReasoningDelta(delta openAICompletionsDelta, provider Provider) (string, string) {
	if delta.ReasoningContent != "" {
		return delta.ReasoningContent, "reasoning_content"
	}
	if delta.Reasoning != "" {
		signature := "reasoning"
		if provider == "opencode-go" {
			signature = "reasoning_content"
		}
		return delta.Reasoning, signature
	}
	if delta.ReasoningText != "" {
		return delta.ReasoningText, "reasoning_text"
	}
	return "", ""
}

func appendOpenAICompletionsThinking(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAICompletionsStreamState, delta string, signature string) {
	if !state.ThinkingStarted {
		state.ThinkingStarted = true
		state.ThinkingIndex = len(response.Content)
		state.ThinkingSignature = signature
		response.Content = append(response.Content, ThinkingContent{ThinkingSignature: signature})
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingStart, ContentIndex: state.ThinkingIndex, Partial: *response})
	}
	state.Thinking.WriteString(delta)
	response.Content[state.ThinkingIndex] = ThinkingContent{Thinking: state.Thinking.String(), ThinkingSignature: state.ThinkingSignature}
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingDelta, ContentIndex: state.ThinkingIndex, Delta: delta, Partial: *response})
}

func appendOpenAICompletionsReasoningDetails(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAICompletionsStreamState, raw json.RawMessage) {
	var details []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &details) != nil {
		return
	}
	for _, detail := range details {
		if !isOpenAICompletionsReasoningDetail(detail) {
			continue
		}
		state.ReasoningDetails = append(state.ReasoningDetails, detail)
	}
	if len(state.ReasoningDetails) == 0 {
		return
	}
	if !state.ThinkingStarted {
		state.ThinkingStarted = true
		state.ThinkingIndex = len(response.Content)
		response.Content = append(response.Content, ThinkingContent{})
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingStart, ContentIndex: state.ThinkingIndex, Partial: *response})
	}
	signature, err := json.Marshal(state.ReasoningDetails)
	if err != nil {
		return
	}
	state.ThinkingSignature = string(signature)
	response.Content[state.ThinkingIndex] = ThinkingContent{
		Thinking:          state.Thinking.String(),
		ThinkingSignature: state.ThinkingSignature,
	}
}

func parseOpenAICompletionsReasoningDetails(signature string) []json.RawMessage {
	if signature == "" {
		return nil
	}
	var details []json.RawMessage
	if json.Unmarshal([]byte(signature), &details) != nil || len(details) == 0 {
		return nil
	}
	for _, detail := range details {
		if !isOpenAICompletionsReasoningDetail(detail) {
			return nil
		}
	}
	return details
}

func isOpenAICompletionsReasoningDetail(raw json.RawMessage) bool {
	var detail map[string]json.RawMessage
	if json.Unmarshal(raw, &detail) != nil || detail == nil ||
		!hasOptionalOpenAICompletionsStringField(detail, "id", true) ||
		!hasOptionalOpenAICompletionsStringField(detail, "format", false) ||
		!hasOptionalOpenAICompletionsNumberField(detail, "index") {
		return false
	}
	typeName, ok := requiredOpenAICompletionsStringField(detail, "type")
	if !ok {
		return false
	}
	switch typeName {
	case "reasoning.summary":
		_, ok = requiredOpenAICompletionsStringField(detail, "summary")
		return ok
	case "reasoning.encrypted":
		_, ok = requiredOpenAICompletionsStringField(detail, "data")
		return ok
	case "reasoning.text":
		if _, ok = requiredOpenAICompletionsStringField(detail, "text"); !ok {
			return false
		}
		return hasOptionalOpenAICompletionsStringField(detail, "signature", true)
	default:
		return false
	}
}

func requiredOpenAICompletionsStringField(detail map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := detail[name]
	if !ok || strings.TrimSpace(string(raw)) == "null" {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func hasOptionalOpenAICompletionsStringField(detail map[string]json.RawMessage, name string, allowNull bool) bool {
	raw, ok := detail[name]
	if !ok {
		return true
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return allowNull
	}
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func hasOptionalOpenAICompletionsNumberField(detail map[string]json.RawMessage, name string) bool {
	raw, ok := detail[name]
	if !ok {
		return true
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return false
	}
	var value float64
	return json.Unmarshal(raw, &value) == nil
}

func appendOpenAICompletionsText(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAICompletionsStreamState, delta string) {
	if !state.TextStarted {
		state.TextStarted = true
		state.TextIndex = len(response.Content)
		response.Content = append(response.Content, TextContent{})
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextStart, ContentIndex: state.TextIndex, Partial: *response})
	}
	state.Text.WriteString(delta)
	response.Content[state.TextIndex] = TextContent{Text: state.Text.String()}
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, ContentIndex: state.TextIndex, Delta: delta, Partial: *response})
}

func appendOpenAICompletionsToolCall(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAICompletionsStreamState, delta openAICompletionsDeltaToolCall) {
	toolState := state.ToolCalls[delta.Index]
	if toolState == nil {
		toolState = &openAICompletionsToolCallState{ContentIndex: -1}
		state.ToolCalls[delta.Index] = toolState
	}
	if delta.ID != "" {
		toolState.ID = delta.ID
	}
	if delta.Function.Name != "" {
		toolState.Name = delta.Function.Name
	}
	toolState.Arguments += delta.Function.Arguments
	toolCall := openAICompletionsToolCallFromState(toolState)
	if !toolState.Started {
		toolState.Started = true
		toolState.ContentIndex = len(response.Content)
		response.Content = append(response.Content, toolCall)
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallStart, ContentIndex: toolState.ContentIndex, ToolCall: toolCall, Partial: *response})
	} else {
		response.Content[toolState.ContentIndex] = toolCall
	}
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallDelta, ContentIndex: toolState.ContentIndex, Delta: delta.Function.Arguments, ToolCall: toolCall, Partial: *response})
}

func openAICompletionsToolCallFromState(state *openAICompletionsToolCallState) ToolCall {
	arguments := parseStreamingJSON(state.Arguments)
	if arguments == nil {
		arguments = map[string]any{}
	}
	return ToolCall{ID: state.ID, Name: state.Name, Arguments: arguments}
}

func finalizeOpenAICompletionsResponse(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAICompletionsStreamState) {
	if state.ThinkingStarted {
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingEnd, ContentIndex: state.ThinkingIndex, Content: state.Thinking.String(), Partial: *response})
	}
	if state.TextStarted {
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextEnd, ContentIndex: state.TextIndex, Content: state.Text.String(), Partial: *response})
	}
	indices := make([]int, 0, len(state.ToolCalls))
	for index := range state.ToolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		toolState := state.ToolCalls[index]
		toolCall := openAICompletionsToolCallFromState(toolState)
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallEnd, ContentIndex: toolState.ContentIndex, ToolCall: toolCall, Partial: *response})
	}
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: response.StopReason, Message: *response})
	stream.finish(*response)
}

func applyOpenAICompletionsUsage(response *AssistantMessage, model Model, usage openAICompletionsUsage) {
	cacheRead := firstOpenAICompletionsTokenCount(
		usage.PromptTokensDetails.CachedTokens,
		usage.PromptCacheHitTokens,
		usage.CachedTokens,
		usage.CacheReadInputTokens,
	)
	cacheWrite := firstOpenAICompletionsTokenCount(
		usage.PromptTokensDetails.CacheWriteTokens,
		usage.PromptTokensDetails.CacheCreationTokens,
		usage.CacheCreationInputTokens,
	)
	input := usage.PromptTokens - cacheRead - cacheWrite
	if usage.PromptCacheMissTokens > 0 {
		input = usage.PromptCacheMissTokens
	}
	if input < 0 {
		input = 0
	}
	total := input + usage.CompletionTokens + cacheRead + cacheWrite
	response.Usage = Usage{
		Input:       input,
		Output:      usage.CompletionTokens,
		CacheRead:   cacheRead,
		CacheWrite:  cacheWrite,
		TotalTokens: total,
	}
	response.Usage.Cost = calculateProviderUsageCost(model, response.Usage)
}

func firstOpenAICompletionsTokenCount(counts ...*int) int {
	for _, count := range counts {
		if count != nil {
			return maxInt(0, *count)
		}
	}
	return 0
}

func mapOpenAICompletionsFinishReason(reason string, content []ContentBlock) StopReason {
	switch strings.TrimSpace(reason) {
	case "stop", "end":
		for _, block := range content {
			if _, ok := block.(ToolCall); ok {
				return StopReasonToolUse
			}
		}
		return StopReasonStop
	case "length", "max_tokens", "max_output_tokens":
		return StopReasonLength
	case "tool_calls", "function_call":
		return StopReasonToolUse
	default:
		return StopReasonError
	}
}

func parseOpenAICompletionsError(body []byte) string {
	var envelope openAICompletionsErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && strings.TrimSpace(envelope.Error.Message) != "" {
		return envelope.Error.Message
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		return "empty error response"
	}
	return message
}

func cloneOpenAICompletionsHTTPHeaders(headers http.Header) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		result[key] = strings.Join(values, ",")
	}
	return result
}

func resolveOpenAICompletionsURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		trimmed = "https://api.openai.com/v1"
	}
	if strings.HasSuffix(trimmed, "/chat/completions") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed + "/chat/completions"
	}
	if parsed, err := url.Parse(trimmed); err == nil && (parsed.Path == "" || parsed.Path == "/") {
		return trimmed + "/v1/chat/completions"
	}
	return trimmed + "/chat/completions"
}
