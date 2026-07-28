package pigo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const mistralToolCallIDLength = 9

type mistralChatRequest struct {
	Model           string                `json:"model"`
	Messages        []mistralChatMessage  `json:"messages"`
	Stream          bool                  `json:"stream"`
	Temperature     *float64              `json:"temperature,omitempty"`
	MaxTokens       int                   `json:"max_tokens,omitempty"`
	Tools           []mistralFunctionTool `json:"tools,omitempty"`
	ToolChoice      any                   `json:"tool_choice,omitempty"`
	PromptMode      string                `json:"prompt_mode,omitempty"`
	ReasoningEffort string                `json:"reasoning_effort,omitempty"`
}

type mistralChatMessage struct {
	Role       string            `json:"role"`
	Content    any               `json:"content,omitempty"`
	ToolCalls  []mistralToolCall `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Name       string            `json:"name,omitempty"`
}

type mistralToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function mistralToolFunction `json:"function"`
}

type mistralToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type mistralFunctionTool struct {
	Type     string                 `json:"type"`
	Function mistralToolDeclaration `json:"function"`
}

type mistralToolDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type mistralChatChunk struct {
	ID      string               `json:"id"`
	Choices []mistralChatChoice  `json:"choices"`
	Usage   *mistralChatUsage    `json:"usage,omitempty"`
	Error   *mistralErrorPayload `json:"error,omitempty"`
}

type mistralChatChoice struct {
	Index        int              `json:"index"`
	Delta        mistralChatDelta `json:"delta"`
	FinishReason *string          `json:"finish_reason"`
}

type mistralChatDelta struct {
	Role             string                 `json:"role,omitempty"`
	Content          any                    `json:"content,omitempty"`
	ReasoningContent string                 `json:"reasoning_content,omitempty"`
	ToolCalls        []mistralDeltaToolCall `json:"tool_calls,omitempty"`
}

type mistralDeltaToolCall struct {
	Index    int                 `json:"index"`
	ID       string              `json:"id,omitempty"`
	Type     string              `json:"type,omitempty"`
	Function mistralToolFunction `json:"function,omitempty"`
}

type mistralChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type mistralErrorPayload struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
}

type mistralToolCallState struct {
	ID        string
	Name      string
	Arguments string
	Index     int
	Started   bool
}

type mistralStreamState struct {
	TextIndex       int
	TextStarted     bool
	Text            strings.Builder
	ThinkingIndex   int
	ThinkingStarted bool
	Thinking        strings.Builder
	ToolCalls       map[int]*mistralToolCallState
}

func streamMistral(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
	options = resolveMistralProviderOptions(model, options).toProviderStreamOptions(model)
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
		payload := any(buildMistralChatRequest(model, ctx, options))
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

		if err := streamMistralSSE(model, requestContext, httpClient, options, bodyBytes, apiKey, &response, stream); err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}
	}()

	return stream
}

func streamSimpleMistral(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
	return streamMistral(model, ctx, BuildProviderStreamOptions(model, options))
}

func buildMistralChatRequest(model Model, ctx Context, options ProviderStreamOptions) mistralChatRequest {
	resolvedOptions := resolveMistralProviderOptions(model, options)
	messages := convertMistralMessages(model, ctx)
	if strings.TrimSpace(ctx.SystemPrompt) != "" {
		messages = append([]mistralChatMessage{{Role: "system", Content: strings.TrimSpace(ctx.SystemPrompt)}}, messages...)
	}

	request := mistralChatRequest{
		Model:    model.ID,
		Messages: messages,
		Stream:   true,
	}
	if resolvedOptions.Temperature != nil {
		request.Temperature = resolvedOptions.Temperature
	}
	if resolvedOptions.MaxTokens > 0 {
		request.MaxTokens = resolvedOptions.MaxTokens
	}
	if len(ctx.Tools) > 0 {
		request.Tools = convertMistralTools(ctx.Tools)
	}
	if choice := strings.TrimSpace(resolvedOptions.ToolChoice); choice != "" {
		request.ToolChoice = mapMistralToolChoice(choice)
	}
	if resolvedOptions.PromptMode != "" {
		request.PromptMode = resolvedOptions.PromptMode
	}
	if resolvedOptions.ReasoningEffort != "" {
		request.ReasoningEffort = resolvedOptions.ReasoningEffort
	}
	return request
}

func convertMistralMessages(model Model, ctx Context) []mistralChatMessage {
	transformed := TransformMessages(ctx.Messages, model, func(id string, _ Model, _ AssistantMessage) string {
		return deriveMistralToolCallID(id, 0)
	})

	messages := make([]mistralChatMessage, 0, len(transformed))
	for _, message := range transformed {
		switch typed := message.(type) {
		case UserMessage:
			content := convertMistralUserContent(typed.Content, modelSupportsInput(model, InputImage))
			if content != nil {
				messages = append(messages, mistralChatMessage{Role: "user", Content: content})
			}
		case AssistantMessage:
			contentParts, toolCalls := convertMistralAssistantContent(typed.Content)
			msg := mistralChatMessage{Role: "assistant"}
			if len(contentParts) > 0 {
				msg.Content = contentParts
			}
			if len(toolCalls) > 0 {
				msg.ToolCalls = toolCalls
			}
			if len(contentParts) > 0 || len(toolCalls) > 0 {
				messages = append(messages, msg)
			}
		case ToolResultMessage:
			content := convertMistralToolResultContent(typed.Content, modelSupportsInput(model, InputImage), typed.IsError)
			messages = append(messages, mistralChatMessage{
				Role:       "tool",
				ToolCallID: typed.ToolCallID,
				Name:       typed.ToolName,
				Content:    content,
			})
		}
	}
	return messages
}

func convertMistralUserContent(content any, supportsImages bool) any {
	switch typed := content.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return typed
	case []ContentBlock:
		result := make([]mistralContentChunk, 0, len(typed))
		hadImages := false
		for _, block := range typed {
			switch b := block.(type) {
			case TextContent:
				if strings.TrimSpace(b.Text) != "" {
					result = append(result, mistralContentChunk{Type: "text", Text: b.Text})
				}
			case ImageContent:
				hadImages = true
				if supportsImages {
					result = append(result, mistralContentChunk{
						Type:     "image_url",
						ImageURL: fmt.Sprintf("data:%s;base64,%s", b.MIMEType, b.Data),
					})
				}
			}
		}
		if len(result) == 0 {
			if hadImages && !supportsImages {
				return "(image omitted: model does not support images)"
			}
			return nil
		}
		return result
	default:
		return nil
	}
}

type mistralContentChunk struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

func convertMistralAssistantContent(blocks []ContentBlock) ([]mistralContentChunk, []mistralToolCall) {
	contentParts := make([]mistralContentChunk, 0)
	toolCalls := make([]mistralToolCall, 0)

	for _, block := range blocks {
		switch typed := block.(type) {
		case TextContent:
			if strings.TrimSpace(typed.Text) != "" {
				contentParts = append(contentParts, mistralContentChunk{Type: "text", Text: typed.Text})
			}
		case ThinkingContent:
			if strings.TrimSpace(typed.Thinking) != "" {
				contentParts = append(contentParts, mistralContentChunk{
					Type: "thinking",
					Text: typed.Thinking,
				})
			}
		case ToolCall:
			toolCalls = append(toolCalls, mistralToolCall{
				ID:   typed.ID,
				Type: "function",
				Function: mistralToolFunction{
					Name:      typed.Name,
					Arguments: mustJSONMistral(typed.Arguments),
				},
			})
		}
	}
	return contentParts, toolCalls
}

func convertMistralToolResultContent(blocks []ContentBlock, supportsImages bool, isError bool) any {
	textParts := make([]string, 0)
	hasImages := false
	imageChunks := make([]mistralContentChunk, 0)

	for _, block := range blocks {
		switch typed := block.(type) {
		case TextContent:
			if strings.TrimSpace(typed.Text) != "" {
				textParts = append(textParts, typed.Text)
			}
		case ImageContent:
			hasImages = true
			if supportsImages {
				imageChunks = append(imageChunks, mistralContentChunk{
					Type:     "image_url",
					ImageURL: fmt.Sprintf("data:%s;base64,%s", typed.MIMEType, typed.Data),
				})
			}
		}
	}

	text := buildMistralToolResultText(strings.Join(textParts, "\n"), hasImages, supportsImages, isError)
	result := []mistralContentChunk{{Type: "text", Text: text}}
	result = append(result, imageChunks...)
	return result
}

func buildMistralToolResultText(text string, hasImages bool, supportsImages bool, isError bool) string {
	trimmed := strings.TrimSpace(text)
	errorPrefix := ""
	if isError {
		errorPrefix = "[tool error] "
	}

	if trimmed != "" {
		imageSuffix := ""
		if hasImages && !supportsImages {
			imageSuffix = "\n[tool image omitted: model does not support images]"
		}
		return errorPrefix + trimmed + imageSuffix
	}
	if hasImages {
		if supportsImages {
			if isError {
				return "[tool error] (see attached image)"
			}
			return "(see attached image)"
		}
		if isError {
			return "[tool error] (image omitted: model does not support images)"
		}
		return "(image omitted: model does not support images)"
	}
	if isError {
		return "[tool error] (no tool output)"
	}
	return "(no tool output)"
}

func convertMistralTools(tools []Tool) []mistralFunctionTool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]mistralFunctionTool, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result = append(result, mistralFunctionTool{
			Type: "function",
			Function: mistralToolDeclaration{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	return result
}

func mapMistralToolChoice(choice string) any {
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "auto":
		return "auto"
	case "none":
		return "none"
	case "any", "required":
		return "any"
	default:
		return choice
	}
}

func streamMistralSSE(model Model, ctx context.Context, client *http.Client, options ProviderStreamOptions, bodyBytes []byte, apiKey string, response *AssistantMessage, stream *AssistantMessageEventStream) error {
	url := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/") + "/v1/chat/completions"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
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
		options.OnResponse(ProviderResponse{Status: httpResponse.StatusCode, Headers: cloneMistralHTTPHeaders(httpResponse.Header)}, model)
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResponse.Body)
		return fmt.Errorf("mistral upstream returned status %d: %s", httpResponse.StatusCode, parseMistralErrorMessage(body))
	}

	state := &mistralStreamState{ToolCalls: map[int]*mistralToolCallState{}}
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: *response})
	if err := readSSEStream(httpResponse.Body, func(_ string, data string) (bool, error) {
		return processMistralChatStreamEvent(data, model, response, stream, state)
	}); err != nil {
		return err
	}
	finalizeMistralChatResponse(model, response, stream, state)
	return nil
}

func processMistralChatStreamEvent(data string, model Model, response *AssistantMessage, stream *AssistantMessageEventStream, state *mistralStreamState) (bool, error) {
	if strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "[DONE]" {
		return false, nil
	}

	var chunk mistralChatChunk
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
	if chunk.Usage != nil {
		applyMistralUsage(response, model, *chunk.Usage)
	}

	if len(chunk.Choices) == 0 {
		return false, nil
	}

	choice := chunk.Choices[0]
	if choice.FinishReason != nil {
		response.StopReason = mapMistralFinishReason(*choice.FinishReason, response.Content)
	}

	delta := choice.Delta
	if delta.ReasoningContent != "" {
		if !state.ThinkingStarted {
			state.ThinkingStarted = true
			state.ThinkingIndex = len(response.Content)
			response.Content = append(response.Content, ThinkingContent{})
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingStart, ContentIndex: state.ThinkingIndex, Partial: cloneAssistantMessage(*response)})
		}
		state.Thinking.WriteString(delta.ReasoningContent)
		response.Content[state.ThinkingIndex] = ThinkingContent{Thinking: state.Thinking.String()}
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingDelta, ContentIndex: state.ThinkingIndex, Delta: delta.ReasoningContent, Partial: cloneAssistantMessage(*response)})
	}

	if delta.Content != nil {
		switch content := delta.Content.(type) {
		case string:
			if content != "" {
				if !state.TextStarted {
					state.TextStarted = true
					state.TextIndex = len(response.Content)
					response.Content = append(response.Content, TextContent{})
					stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextStart, ContentIndex: state.TextIndex, Partial: cloneAssistantMessage(*response)})
				}
				state.Text.WriteString(content)
				response.Content[state.TextIndex] = TextContent{Text: state.Text.String()}
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, ContentIndex: state.TextIndex, Delta: content, Partial: cloneAssistantMessage(*response)})
			}
		case []any:
			for _, item := range content {
				chunkMap, ok := item.(map[string]any)
				if !ok {
					continue
				}
				text := stringValue(chunkMap["text"])
				if text != "" {
					if !state.TextStarted {
						state.TextStarted = true
						state.TextIndex = len(response.Content)
						response.Content = append(response.Content, TextContent{})
						stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextStart, ContentIndex: state.TextIndex, Partial: cloneAssistantMessage(*response)})
					}
					state.Text.WriteString(text)
					response.Content[state.TextIndex] = TextContent{Text: state.Text.String()}
					stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, ContentIndex: state.TextIndex, Delta: text, Partial: cloneAssistantMessage(*response)})
				}
			}
		}
	}

	for _, toolDelta := range delta.ToolCalls {
		toolState := mistralToolCallStateFor(state.ToolCalls, toolDelta.Index)
		if toolDelta.ID != "" {
			toolState.ID = deriveMistralToolCallID(toolDelta.ID, 0)
		}
		if toolDelta.Function.Name != "" {
			toolState.Name = toolDelta.Function.Name
		}
		if toolDelta.Function.Arguments != "" {
			toolState.Arguments += toolDelta.Function.Arguments
		}

		toolCall := mistralToolCallFromState(toolState)
		if !toolState.Started {
			toolState.Started = true
			toolState.Index = len(response.Content)
			response.Content = append(response.Content, toolCall)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallStart, ContentIndex: toolState.Index, ToolCall: toolCall, Partial: cloneAssistantMessage(*response)})
		} else {
			response.Content[toolState.Index] = toolCall
		}
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallDelta, ContentIndex: toolState.Index, Delta: toolDelta.Function.Arguments, ToolCall: toolCall, Partial: cloneAssistantMessage(*response)})
	}

	return false, nil
}

func mistralToolCallStateFor(states map[int]*mistralToolCallState, index int) *mistralToolCallState {
	state, ok := states[index]
	if ok {
		return state
	}
	state = &mistralToolCallState{Index: -1}
	states[index] = state
	return state
}

func mistralToolCallFromState(state *mistralToolCallState) ToolCall {
	var args map[string]any
	if strings.TrimSpace(state.Arguments) != "" {
		_ = json.Unmarshal([]byte(state.Arguments), &args)
	}
	if args == nil {
		args = map[string]any{}
	}
	return ToolCall{ID: state.ID, Name: state.Name, Arguments: args}
}

func finalizeMistralChatResponse(model Model, response *AssistantMessage, stream *AssistantMessageEventStream, state *mistralStreamState) {
	if state.ThinkingStarted {
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingEnd, ContentIndex: state.ThinkingIndex, Content: state.Thinking.String(), Partial: cloneAssistantMessage(*response)})
	}
	if state.TextStarted {
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextEnd, ContentIndex: state.TextIndex, Content: state.Text.String(), Partial: cloneAssistantMessage(*response)})
	}
	for _, toolState := range state.ToolCalls {
		toolCall := mistralToolCallFromState(toolState)
		response.Content[toolState.Index] = toolCall
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallEnd, ContentIndex: toolState.Index, ToolCall: toolCall, Partial: cloneAssistantMessage(*response)})
	}
	if response.StopReason == "" {
		response.StopReason = StopReasonStop
	}
	response.Usage.Cost = CalculateCost(model, response.Usage)
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: response.StopReason, Message: *response})
	stream.finish(*response)
}

func applyMistralUsage(response *AssistantMessage, model Model, usage mistralChatUsage) {
	response.Usage = Usage{
		Input:       usage.PromptTokens,
		Output:      usage.CompletionTokens,
		CacheRead:   0,
		CacheWrite:  0,
		TotalTokens: usage.TotalTokens,
	}
	response.Usage.Cost = CalculateCost(model, response.Usage)
}

func mapMistralFinishReason(reason string, content []ContentBlock) StopReason {
	switch strings.TrimSpace(reason) {
	case "", "stop":
		for _, block := range content {
			if _, ok := block.(ToolCall); ok {
				return StopReasonToolUse
			}
		}
		return StopReasonStop
	case "length", "model_length":
		return StopReasonLength
	case "tool_calls":
		return StopReasonToolUse
	case "error":
		return StopReasonError
	default:
		return StopReasonStop
	}
}

func parseMistralErrorMessage(body []byte) string {
	var envelope struct {
		Error mistralErrorPayload `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && strings.TrimSpace(envelope.Error.Message) != "" {
		return envelope.Error.Message
	}
	return strings.TrimSpace(string(body))
}

func cloneMistralHTTPHeaders(headers http.Header) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		result[key] = strings.Join(values, ",")
	}
	return result
}

func deriveMistralToolCallID(id string, attempt int) string {
	normalized := strings.ReplaceAll(id, " ", "")
	normalized = stripNonAlphanumeric(normalized)
	if attempt == 0 && len(normalized) == mistralToolCallIDLength {
		return normalized
	}
	seedBase := normalized
	if seedBase == "" {
		seedBase = id
	}
	seed := seedBase
	if attempt > 0 {
		seed = fmt.Sprintf("%s:%d", seedBase, attempt)
	}
	hash := sha256.Sum256([]byte(seed))
	hashStr := hex.EncodeToString(hash[:])
	hashStr = stripNonAlphanumeric(hashStr)
	if len(hashStr) > mistralToolCallIDLength {
		return hashStr[:mistralToolCallIDLength]
	}
	return hashStr
}

func stripNonAlphanumeric(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func stringValue(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

func mustJSONMistral(value any) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(bytes)
}
