package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type deepSeekChatRequest struct {
	Model          string                   `json:"model"`
	Messages       []deepSeekChatMessage    `json:"messages"`
	Stream         bool                     `json:"stream"`
	Thinking       *deepSeekThinkingOptions `json:"thinking,omitempty"`
	MaxTokens      int                      `json:"max_tokens,omitempty"`
	Temperature    *float64                 `json:"temperature,omitempty"`
	Tools          []map[string]any         `json:"tools,omitempty"`
	ToolChoice     any                      `json:"tool_choice,omitempty"`
	ResponseFormat *deepSeekResponseFormat  `json:"response_format,omitempty"`
}

type deepSeekResponseFormat struct {
	Type string `json:"type"`
}

type deepSeekThinkingOptions struct {
	Type            string `json:"type"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type deepSeekChatMessage struct {
	Role       string                 `json:"role"`
	Content    any                    `json:"content"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	ToolCalls  []deepSeekChatToolCall `json:"tool_calls,omitempty"`
}

type deepSeekChatToolCall struct {
	ID       string                   `json:"id,omitempty"`
	Type     string                   `json:"type"`
	Function deepSeekChatToolFunction `json:"function"`
}

type deepSeekChatToolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type deepSeekChatChunk struct {
	ID      string                `json:"id"`
	Model   string                `json:"model"`
	Choices []deepSeekChatChoice  `json:"choices"`
	Usage   *deepSeekChatUsage    `json:"usage,omitempty"`
	Error   *deepSeekErrorPayload `json:"error,omitempty"`
}

type deepSeekChatChoice struct {
	Index        int                `json:"index"`
	Delta        deepSeekChatDelta  `json:"delta"`
	Message      *deepSeekChatDelta `json:"message,omitempty"`
	FinishReason string             `json:"finish_reason"`
}

type deepSeekChatDelta struct {
	Role             string                  `json:"role,omitempty"`
	Content          string                  `json:"content,omitempty"`
	ReasoningContent string                  `json:"reasoning_content,omitempty"`
	ToolCalls        []deepSeekDeltaToolCall `json:"tool_calls,omitempty"`
}

type deepSeekDeltaToolCall struct {
	Index    int                      `json:"index"`
	ID       string                   `json:"id,omitempty"`
	Type     string                   `json:"type,omitempty"`
	Function deepSeekChatToolFunction `json:"function,omitempty"`
}

type deepSeekChatUsage struct {
	PromptTokens            int                             `json:"prompt_tokens"`
	CompletionTokens        int                             `json:"completion_tokens"`
	TotalTokens             int                             `json:"total_tokens"`
	PromptCacheHitTokens    int                             `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens   int                             `json:"prompt_cache_miss_tokens"`
	CompletionTokensDetails *deepSeekCompletionTokenDetails `json:"completion_tokens_details,omitempty"`
}

type deepSeekCompletionTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type deepSeekErrorEnvelope struct {
	Error deepSeekErrorPayload `json:"error"`
}

type deepSeekErrorPayload struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type deepSeekToolCallState struct {
	ID        string
	Name      string
	Arguments string
	Index     int
	Started   bool
}

func streamDeepSeekChatCompletions(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
	options = resolveDeepSeekProviderOptions(model, options).toProviderStreamOptions(model)
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
		payload := any(buildDeepSeekChatRequest(model, ctx, options))
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

		requestContext, cancel := providerRequestContext(options.RequestContext, options.TimeoutMs)
		defer cancel()
		requestContext = stream.startRequest(requestContext, payload)
		options.RequestContext = requestContext

		apiKey := options.APIKey
		if apiKey == "" {
			apiKey = ResolveAPIKey(model.Provider, options.Auth)
		}
		if apiKey == "" {
			apiKey = GetEnvAPIKey(model.Provider)
		}
		if apiKey == "" {
			response.StopReason = StopReasonError
			response.ErrorMessage = "missing api key"
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		httpClient := options.HTTPClient
		if httpClient == nil {
			httpClient = http.DefaultClient
		}
		if err := streamDeepSeekChatSSE(model, requestContext, httpClient, options, bodyBytes, apiKey, &response, stream); err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}
	}()

	return stream
}

func streamSimpleDeepSeekChatCompletions(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
	return streamDeepSeekChatCompletions(model, ctx, BuildProviderStreamOptions(model, options))
}

func buildDeepSeekChatRequest(model Model, ctx Context, options ProviderStreamOptions) deepSeekChatRequest {
	resolvedOptions := resolveDeepSeekProviderOptions(model, options)
	requestBody := deepSeekChatRequest{
		Model:    model.ID,
		Messages: convertDeepSeekChatMessages(model, ctx),
		Stream:   true,
		Tools:    convertDeepSeekChatTools(ctx.Tools),
	}
	if resolvedOptions.MaxTokens > 0 {
		requestBody.MaxTokens = resolvedOptions.MaxTokens
	}
	if resolvedOptions.Temperature != nil {
		requestBody.Temperature = resolvedOptions.Temperature
	}
	if toolChoice := strings.TrimSpace(resolvedOptions.ToolChoice); toolChoice != "" {
		requestBody.ToolChoice = toolChoice
	}
	if thinking := resolveDeepSeekThinking(model, resolvedOptions.Reasoning); thinking != nil {
		requestBody.Thinking = thinking
	}
	if resolvedOptions.ResponseFormat != nil {
		requestBody.ResponseFormat = &deepSeekResponseFormat{Type: string(resolvedOptions.ResponseFormat.Type)}
	}
	return requestBody
}

func resolveDeepSeekThinking(model Model, level ThinkingLevel) *deepSeekThinkingOptions {
	if !model.Reasoning {
		return nil
	}
	switch strings.TrimSpace(string(level)) {
	case "", string(ThinkingLevelXHigh):
		return &deepSeekThinkingOptions{Type: "enabled", ReasoningEffort: "max"}
	case string(ThinkingLevelHigh), string(ThinkingLevelMedium), string(ThinkingLevelLow), string(ThinkingLevelMinimal):
		return &deepSeekThinkingOptions{Type: "enabled", ReasoningEffort: "high"}
	case "off", "disabled":
		return &deepSeekThinkingOptions{Type: "disabled"}
	default:
		if mapped := strings.TrimSpace(model.ThinkingLevelMap[ModelThinkingLevel(level)]); mapped != "" {
			return &deepSeekThinkingOptions{Type: "enabled", ReasoningEffort: mapped}
		}
		return &deepSeekThinkingOptions{Type: "enabled", ReasoningEffort: "high"}
	}
}

func convertDeepSeekChatMessages(model Model, ctx Context) []deepSeekChatMessage {
	transformed := TransformMessages(ctx.Messages, model, nil)
	messages := make([]deepSeekChatMessage, 0, len(transformed)+1)
	if strings.TrimSpace(ctx.SystemPrompt) != "" {
		messages = append(messages, deepSeekChatMessage{Role: "system", Content: strings.TrimSpace(ctx.SystemPrompt)})
	}
	for _, message := range transformed {
		switch typed := message.(type) {
		case UserMessage:
			if content := deepSeekTextFromContent(typed.Content); strings.TrimSpace(content) != "" {
				messages = append(messages, deepSeekChatMessage{Role: "user", Content: content})
			}
		case AssistantMessage:
			content, toolCalls := convertDeepSeekAssistantContent(typed.Content)
			if strings.TrimSpace(content) != "" || len(toolCalls) > 0 {
				messages = append(messages, deepSeekChatMessage{Role: "assistant", Content: content, ToolCalls: toolCalls})
			}
		case ToolResultMessage:
			messages = append(messages, deepSeekChatMessage{
				Role:       "tool",
				Content:    deepSeekTextFromContent(typed.Content),
				ToolCallID: typed.ToolCallID,
			})
		}
	}
	return messages
}

func deepSeekTextFromContent(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []ContentBlock:
		parts := make([]string, 0, len(typed))
		for _, block := range typed {
			switch block := block.(type) {
			case TextContent:
				if strings.TrimSpace(block.Text) != "" {
					parts = append(parts, block.Text)
				}
			case ImageContent:
				parts = append(parts, "(see attached image)")
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprint(content)
	}
}

func convertDeepSeekAssistantContent(blocks []ContentBlock) (string, []deepSeekChatToolCall) {
	parts := make([]string, 0, len(blocks))
	toolCalls := make([]deepSeekChatToolCall, 0)
	for _, block := range blocks {
		switch typed := block.(type) {
		case TextContent:
			if strings.TrimSpace(typed.Text) != "" {
				parts = append(parts, typed.Text)
			}
		case ThinkingContent:
			if strings.TrimSpace(typed.Thinking) != "" {
				parts = append(parts, typed.Thinking)
			}
		case ToolCall:
			toolCalls = append(toolCalls, deepSeekChatToolCall{
				ID:   typed.ID,
				Type: "function",
				Function: deepSeekChatToolFunction{
					Name:      typed.Name,
					Arguments: mustJSON(typed.Arguments),
				},
			})
		}
	}
	return strings.Join(parts, "\n"), toolCalls
}

func convertDeepSeekChatTools(tools []Tool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  parameters,
			},
		})
	}
	return result
}

func streamDeepSeekChatSSE(model Model, ctx context.Context, client *http.Client, options ProviderStreamOptions, bodyBytes []byte, apiKey string, response *AssistantMessage, stream *AssistantMessageEventStream) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveDeepSeekChatURL(model.BaseURL), bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	for key, value := range options.Headers {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		request.Header.Set(key, value)
	}

	httpResponse, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() {
		_ = httpResponse.Body.Close()
	}()

	if options.OnResponse != nil {
		options.OnResponse(ProviderResponse{Status: httpResponse.StatusCode, Headers: cloneDeepSeekHTTPHeaders(httpResponse.Header)}, model)
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResponse.Body)
		return fmt.Errorf("deepseek upstream returned status %d: %s", httpResponse.StatusCode, parseDeepSeekErrorMessage(body))
	}

	state := &deepSeekChatState{
		ToolCalls: map[int]*deepSeekToolCallState{},
	}
	if err := readSSEStream(httpResponse.Body, func(_ string, data string) (bool, error) {
		return processDeepSeekChatStreamEvent(data, model, response, stream, state)
	}); err != nil {
		return err
	}
	finalizeDeepSeekChatResponse(response, stream, state)
	return nil
}

type deepSeekChatState struct {
	TextIndex       int
	TextStarted     bool
	Text            strings.Builder
	ThinkingIndex   int
	ThinkingStarted bool
	Thinking        strings.Builder
	ToolCalls       map[int]*deepSeekToolCallState
}

func processDeepSeekChatStreamEvent(data string, model Model, response *AssistantMessage, stream *AssistantMessageEventStream, state *deepSeekChatState) (bool, error) {
	if strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "[DONE]" {
		return false, nil
	}
	var chunk deepSeekChatChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return false, err
	}
	if chunk.Error != nil && strings.TrimSpace(chunk.Error.Message) != "" {
		response.StopReason = StopReasonError
		response.ErrorMessage = chunk.Error.Message
		return true, nil
	}
	if strings.TrimSpace(chunk.ID) != "" {
		response.ResponseID = chunk.ID
	}
	if strings.TrimSpace(chunk.Model) != "" {
		response.ResponseModel = chunk.Model
	}
	if chunk.Usage != nil {
		applyDeepSeekUsage(response, model, *chunk.Usage)
	}
	for _, choice := range chunk.Choices {
		delta := choice.Delta
		if choice.Message != nil {
			delta = *choice.Message
		}
		if delta.ReasoningContent != "" {
			if !state.ThinkingStarted {
				state.ThinkingStarted = true
				state.ThinkingIndex = len(response.Content)
				response.Content = append(response.Content, ThinkingContent{})
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingStart, ContentIndex: state.ThinkingIndex, Partial: *response})
			}
			state.Thinking.WriteString(delta.ReasoningContent)
			response.Content[state.ThinkingIndex] = ThinkingContent{Thinking: state.Thinking.String()}
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingDelta, ContentIndex: state.ThinkingIndex, Delta: delta.ReasoningContent, Partial: *response})
		}
		if delta.Content != "" {
			if !state.TextStarted {
				state.TextStarted = true
				state.TextIndex = len(response.Content)
				response.Content = append(response.Content, TextContent{})
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextStart, ContentIndex: state.TextIndex, Partial: *response})
			}
			state.Text.WriteString(delta.Content)
			response.Content[state.TextIndex] = TextContent{Text: state.Text.String()}
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, ContentIndex: state.TextIndex, Delta: delta.Content, Partial: *response})
		}
		for _, toolDelta := range delta.ToolCalls {
			toolState := deepSeekToolCallStateFor(state.ToolCalls, toolDelta)
			if toolDelta.ID != "" {
				toolState.ID = toolDelta.ID
			}
			if toolDelta.Function.Name != "" {
				toolState.Name = toolDelta.Function.Name
			}
			if toolDelta.Function.Arguments != "" {
				toolState.Arguments += toolDelta.Function.Arguments
			}
			toolCall := deepSeekToolCallFromState(toolState)
			if !toolState.Started {
				toolState.Started = true
				toolState.Index = len(response.Content)
				response.Content = append(response.Content, toolCall)
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallStart, ContentIndex: toolState.Index, ToolCall: toolCall, Partial: *response})
			} else {
				response.Content[toolState.Index] = toolCall
			}
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallDelta, ContentIndex: toolState.Index, Delta: toolDelta.Function.Arguments, ToolCall: toolCall, Partial: *response})
		}
		if strings.TrimSpace(choice.FinishReason) != "" {
			response.StopReason = mapDeepSeekFinishReason(choice.FinishReason, response.Content)
		}
	}
	return false, nil
}

func deepSeekToolCallStateFor(states map[int]*deepSeekToolCallState, delta deepSeekDeltaToolCall) *deepSeekToolCallState {
	state, ok := states[delta.Index]
	if ok {
		return state
	}
	state = &deepSeekToolCallState{Index: -1}
	states[delta.Index] = state
	return state
}

func deepSeekToolCallFromState(state *deepSeekToolCallState) ToolCall {
	var args map[string]any
	if strings.TrimSpace(state.Arguments) != "" {
		_ = json.Unmarshal([]byte(state.Arguments), &args)
	}
	if args == nil {
		args = map[string]any{}
	}
	return ToolCall{ID: state.ID, Name: state.Name, Arguments: args}
}

func finalizeDeepSeekChatResponse(response *AssistantMessage, stream *AssistantMessageEventStream, state *deepSeekChatState) {
	if state.ThinkingStarted {
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingEnd, ContentIndex: state.ThinkingIndex, Content: state.Thinking.String(), Partial: *response})
	}
	if state.TextStarted {
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextEnd, ContentIndex: state.TextIndex, Content: state.Text.String(), Partial: *response})
	}
	for _, toolState := range state.ToolCalls {
		toolCall := deepSeekToolCallFromState(toolState)
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallEnd, ContentIndex: toolState.Index, ToolCall: toolCall, Partial: *response})
	}
	if response.StopReason == "" {
		response.StopReason = StopReasonStop
	}
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: response.StopReason, Message: *response})
	stream.finish(*response)
}

func applyDeepSeekUsage(response *AssistantMessage, model Model, usage deepSeekChatUsage) {
	response.Usage = Usage{
		Input:       usage.PromptTokens,
		Output:      usage.CompletionTokens,
		CacheRead:   usage.PromptCacheHitTokens,
		TotalTokens: usage.TotalTokens,
		Cost:        model.Cost,
	}
}

func mapDeepSeekFinishReason(reason string, content []ContentBlock) StopReason {
	switch strings.TrimSpace(reason) {
	case "", "stop":
		for _, block := range content {
			if _, ok := block.(ToolCall); ok {
				return StopReasonToolUse
			}
		}
		return StopReasonStop
	case "length":
		return StopReasonLength
	case "tool_calls":
		return StopReasonToolUse
	default:
		return StopReasonError
	}
}

func parseDeepSeekErrorMessage(body []byte) string {
	var envelope deepSeekErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && strings.TrimSpace(envelope.Error.Message) != "" {
		return envelope.Error.Message
	}
	return strings.TrimSpace(string(body))
}

func cloneDeepSeekHTTPHeaders(headers http.Header) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		result[key] = strings.Join(values, ",")
	}
	return result
}

func resolveDeepSeekChatURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		trimmed = "https://api.deepseek.com"
	}
	if strings.HasSuffix(trimmed, "/chat/completions") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed + "/chat/completions"
	}
	if parsed, err := url.Parse(trimmed); err == nil && (parsed.Path == "" || parsed.Path == "/") {
		return trimmed + "/chat/completions"
	}
	return trimmed + "/chat/completions"
}
