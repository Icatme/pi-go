package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type googleRequest struct {
	Model    string          `json:"model"`
	Contents []googleContent `json:"contents"`
	Config   *googleConfig   `json:"config,omitempty"`
}

type googleContent struct {
	Role  string       `json:"role"`
	Parts []googlePart `json:"parts"`
}

type googlePart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *googleInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *googleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *googleFunctionResponse `json:"functionResponse,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
}

type googleInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type googleFunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type googleFunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type googleConfig struct {
	SystemInstruction *googleContent        `json:"systemInstruction,omitempty"`
	Tools             []googleTool          `json:"tools,omitempty"`
	ToolConfig        *googleToolConfig     `json:"toolConfig,omitempty"`
	ThinkingConfig    *googleThinkingConfig `json:"thinkingConfig,omitempty"`
	Temperature       *float64              `json:"temperature,omitempty"`
	MaxOutputTokens   int                   `json:"maxOutputTokens,omitempty"`
}

type googleTool struct {
	FunctionDeclarations []googleFunctionDeclaration `json:"functionDeclarations"`
}

type googleFunctionDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type googleToolConfig struct {
	FunctionCallingConfig googleFunctionCallingConfig `json:"functionCallingConfig"`
}

type googleFunctionCallingConfig struct {
	Mode string `json:"mode"`
}

type googleThinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	ThinkingBudget  int    `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
}

type googleStreamChunk struct {
	ResponseID    string               `json:"responseId,omitempty"`
	Candidates    []googleCandidate    `json:"candidates,omitempty"`
	UsageMetadata *googleUsageMetadata `json:"usageMetadata,omitempty"`
	Error         *googleErrorPayload  `json:"error,omitempty"`
}

type googleCandidate struct {
	Content      *googleContent `json:"content,omitempty"`
	FinishReason string         `json:"finishReason,omitempty"`
}

type googleUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int `json:"candidatesTokenCount,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
	TotalTokenCount         int `json:"totalTokenCount,omitempty"`
}

type googleErrorPayload struct {
	Message string `json:"message,omitempty"`
	Code    int    `json:"code,omitempty"`
}

type googleStreamState struct {
	CurrentBlock      any
	CurrentBlockIndex int
	ToolCallCounter   int
}

func streamGoogle(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
	options = resolveGoogleProviderOptions(model, options).toProviderStreamOptions(model)
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
		payload := any(buildGoogleRequest(model, ctx, options))
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

		if err := streamGoogleSSE(model, requestContext, httpClient, options, bodyBytes, apiKey, &response, stream); err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}
	}()

	return stream
}

func streamSimpleGoogle(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
	return streamGoogle(model, ctx, BuildProviderStreamOptions(model, options))
}

func buildGoogleRequest(model Model, ctx Context, options ProviderStreamOptions) googleRequest {
	resolvedOptions := resolveGoogleProviderOptions(model, options)
	request := googleRequest{
		Model:    model.ID,
		Contents: convertGoogleMessages(model, ctx),
	}

	config := &googleConfig{}
	if strings.TrimSpace(ctx.SystemPrompt) != "" {
		config.SystemInstruction = &googleContent{
			Role:  "user",
			Parts: []googlePart{{Text: strings.TrimSpace(ctx.SystemPrompt)}},
		}
	}
	if len(ctx.Tools) > 0 {
		config.Tools = convertGoogleTools(ctx.Tools)
	}
	if resolvedOptions.ToolChoice != "" {
		config.ToolConfig = &googleToolConfig{
			FunctionCallingConfig: googleFunctionCallingConfig{
				Mode: mapGoogleToolChoice(resolvedOptions.ToolChoice),
			},
		}
	}
	if resolvedOptions.Temperature != nil {
		config.Temperature = resolvedOptions.Temperature
	}
	if resolvedOptions.MaxTokens > 0 {
		config.MaxOutputTokens = resolvedOptions.MaxTokens
	}
	if thinking := resolvedOptions.Thinking; thinking != nil {
		config.ThinkingConfig = buildGoogleThinkingConfig(model, thinking)
	}

	if config.SystemInstruction != nil || len(config.Tools) > 0 || config.ToolConfig != nil ||
		config.ThinkingConfig != nil || config.Temperature != nil || config.MaxOutputTokens > 0 {
		request.Config = config
	}

	return request
}

func buildGoogleThinkingConfig(model Model, thinking *GoogleThinkingConfig) *googleThinkingConfig {
	if thinking == nil {
		return nil
	}
	if !thinking.Enabled {
		return buildGoogleDisabledThinkingConfig(model)
	}
	config := &googleThinkingConfig{IncludeThoughts: true}
	if thinking.Level != "" {
		config.ThinkingLevel = thinking.Level
	} else if thinking.BudgetTokens != 0 || !usesGoogleThinkingLevel(model) {
		config.ThinkingBudget = thinking.BudgetTokens
	}
	return config
}

func buildGoogleDisabledThinkingConfig(model Model) *googleThinkingConfig {
	if usesGoogleThinkingLevel(model) {
		level := "LOW"
		if isGemini3FlashModel(model.ID) || isGemma4Model(model.ID) {
			level = "MINIMAL"
		}
		return &googleThinkingConfig{IncludeThoughts: false, ThinkingLevel: level}
	}
	return &googleThinkingConfig{IncludeThoughts: false, ThinkingBudget: 0}
}

func mapGoogleToolChoice(choice string) string {
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "none":
		return "NONE"
	case "any", "required":
		return "ANY"
	case "auto", "":
		return "AUTO"
	default:
		return "AUTO"
	}
}

func convertGoogleMessages(model Model, ctx Context) []googleContent {
	transformed := TransformMessages(ctx.Messages, model, func(id string, _ Model, _ AssistantMessage) string {
		return normalizeGoogleToolCallID(id)
	})

	contents := make([]googleContent, 0, len(transformed))
	for _, message := range transformed {
		switch typed := message.(type) {
		case UserMessage:
			parts := convertGoogleUserContent(typed.Content)
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, googleContent{Role: "user", Parts: parts})
		case AssistantMessage:
			parts := convertGoogleAssistantContent(model, typed)
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, googleContent{Role: "model", Parts: parts})
		case ToolResultMessage:
			parts := convertGoogleToolResultContent(model, typed)
			if len(parts) == 0 {
				continue
			}
			if len(contents) > 0 && contents[len(contents)-1].Role == "user" {
				last := &contents[len(contents)-1]
				last.Parts = append(last.Parts, parts...)
			} else {
				contents = append(contents, googleContent{Role: "user", Parts: parts})
			}
		}
	}
	return contents
}

func convertGoogleUserContent(content any) []googlePart {
	switch typed := content.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []googlePart{{Text: typed}}
	case []ContentBlock:
		parts := make([]googlePart, 0, len(typed))
		for _, block := range typed {
			switch b := block.(type) {
			case TextContent:
				if strings.TrimSpace(b.Text) != "" {
					parts = append(parts, googlePart{Text: b.Text})
				}
			case ImageContent:
				parts = append(parts, googlePart{InlineData: &googleInlineData{MIMEType: b.MIMEType, Data: b.Data}})
			}
		}
		return parts
	default:
		return nil
	}
}

func convertGoogleAssistantContent(model Model, message AssistantMessage) []googlePart {
	isSameModel := message.Provider == model.Provider && message.Model == model.ID
	parts := make([]googlePart, 0, len(message.Content))

	for _, block := range message.Content {
		switch typed := block.(type) {
		case TextContent:
			if strings.TrimSpace(typed.Text) == "" {
				continue
			}
			part := googlePart{Text: typed.Text}
			if sig := resolveThoughtSignature(isSameModel, typed.TextSignature); sig != "" {
				// Thought signatures on text parts are not directly representable in our
				// simple part model; keep the text only.
				_ = sig
			}
			parts = append(parts, part)
		case ThinkingContent:
			if strings.TrimSpace(typed.Thinking) == "" {
				continue
			}
			if isSameModel {
				parts = append(parts, googlePart{
					Thought: true,
					Text:    typed.Thinking,
				})
			} else {
				parts = append(parts, googlePart{Text: typed.Thinking})
			}
		case ToolCall:
			parts = append(parts, googlePart{
				FunctionCall: &googleFunctionCall{
					ID:   normalizeGoogleToolCallID(typed.ID),
					Name: typed.Name,
					Args: cloneMap(typed.Arguments),
				},
			})
		}
	}
	return parts
}

func convertGoogleToolResultContent(model Model, message ToolResultMessage) []googlePart {
	blocks := message.Content
	textParts := make([]string, 0)
	hasImages := false
	var imageParts []googlePart

	for _, block := range blocks {
		switch typed := block.(type) {
		case TextContent:
			if strings.TrimSpace(typed.Text) != "" {
				textParts = append(textParts, typed.Text)
			}
		case ImageContent:
			hasImages = true
			if modelSupportsInput(model, InputImage) {
				imageParts = append(imageParts, googlePart{InlineData: &googleInlineData{MIMEType: typed.MIMEType, Data: typed.Data}})
			}
		}
	}

	text := strings.Join(textParts, "\n")
	if text == "" {
		if hasImages {
			text = "(see attached image)"
		} else {
			text = "No result provided"
		}
	}

	response := map[string]any{"output": text}
	if message.IsError {
		response = map[string]any{"error": text}
	}

	parts := []googlePart{{
		FunctionResponse: &googleFunctionResponse{
			ID:       normalizeGoogleToolCallID(message.ToolCallID),
			Name:     message.ToolName,
			Response: response,
		},
	}}
	if len(imageParts) > 0 {
		parts = append(parts, imageParts...)
	}
	return parts
}

func convertGoogleTools(tools []Tool) []googleTool {
	if len(tools) == 0 {
		return nil
	}
	declarations := make([]googleFunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		declarations = append(declarations, googleFunctionDeclaration{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  parameters,
		})
	}
	return []googleTool{{FunctionDeclarations: declarations}}
}

func normalizeGoogleToolCallID(id string) string {
	return strings.ReplaceAll(id, " ", "_")
}

func resolveThoughtSignature(isSameModel bool, signature string) string {
	if !isSameModel || signature == "" {
		return ""
	}
	return signature
}

func streamGoogleSSE(model Model, ctx context.Context, client *http.Client, options ProviderStreamOptions, bodyBytes []byte, apiKey string, response *AssistantMessage, stream *AssistantMessageEventStream) error {
	url := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/") + "/models/" + model.ID + ":streamGenerateContent?alt=sse"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	request.Header.Set("x-goog-api-key", apiKey)
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
		options.OnResponse(ProviderResponse{Status: httpResponse.StatusCode, Headers: cloneGoogleHTTPHeaders(httpResponse.Header)}, model)
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResponse.Body)
		return fmt.Errorf("google upstream returned status %d: %s", httpResponse.StatusCode, parseGoogleErrorMessage(body))
	}

	state := &googleStreamState{}
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: *response})
	if err := readSSEStream(httpResponse.Body, func(_ string, data string) (bool, error) {
		return processGoogleStreamEvent(data, model, response, stream, state)
	}); err != nil {
		return err
	}
	finalizeGoogleResponse(model, response, stream, state)
	return nil
}

func processGoogleStreamEvent(data string, model Model, response *AssistantMessage, stream *AssistantMessageEventStream, state *googleStreamState) (bool, error) {
	if strings.TrimSpace(data) == "" {
		return false, nil
	}

	var chunk googleStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return false, err
	}
	if chunk.Error != nil && strings.TrimSpace(chunk.Error.Message) != "" {
		response.StopReason = StopReasonError
		response.ErrorMessage = chunk.Error.Message
		return true, nil
	}
	if strings.TrimSpace(chunk.ResponseID) != "" {
		response.ResponseID = chunk.ResponseID
	}
	if chunk.UsageMetadata != nil {
		applyGoogleUsage(response, model, *chunk.UsageMetadata)
	}

	if len(chunk.Candidates) == 0 || chunk.Candidates[0].Content == nil {
		return false, nil
	}

	candidate := chunk.Candidates[0]
	for _, part := range candidate.Content.Parts {
		if part.FunctionCall != nil {
			if state.CurrentBlock != nil {
				finishGoogleCurrentBlock(response, stream, state)
			}
			toolCall := ToolCall{
				ID:        googleToolCallID(part.FunctionCall.ID, part.FunctionCall.Name, state),
				Name:      part.FunctionCall.Name,
				Arguments: cloneMap(part.FunctionCall.Args),
			}
			contentIndex := len(response.Content)
			response.Content = append(response.Content, toolCall)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallStart, ContentIndex: contentIndex, Partial: cloneAssistantMessage(*response)})
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallDelta, ContentIndex: contentIndex, Delta: mustJSON(toolCall.Arguments), ToolCall: toolCall, Partial: cloneAssistantMessage(*response)})
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallEnd, ContentIndex: contentIndex, ToolCall: toolCall, Partial: cloneAssistantMessage(*response)})
			continue
		}

		isThinking := part.Thought
		if part.Text == "" {
			continue
		}

		if state.CurrentBlock == nil || !googleSameBlockType(state.CurrentBlock, isThinking) {
			finishGoogleCurrentBlock(response, stream, state)
			contentIndex := len(response.Content)
			if isThinking {
				response.Content = append(response.Content, ThinkingContent{Thinking: ""})
				state.CurrentBlock = &ThinkingContent{Thinking: ""}
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingStart, ContentIndex: contentIndex, Partial: cloneAssistantMessage(*response)})
			} else {
				response.Content = append(response.Content, TextContent{Text: ""})
				state.CurrentBlock = &TextContent{Text: ""}
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextStart, ContentIndex: contentIndex, Partial: cloneAssistantMessage(*response)})
			}
			state.CurrentBlockIndex = contentIndex
		}

		if isThinking {
			thinking := state.CurrentBlock.(*ThinkingContent)
			thinking.Thinking += part.Text
			response.Content[state.CurrentBlockIndex] = *thinking
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingDelta, ContentIndex: state.CurrentBlockIndex, Delta: part.Text, Partial: cloneAssistantMessage(*response)})
		} else {
			text := state.CurrentBlock.(*TextContent)
			text.Text += part.Text
			response.Content[state.CurrentBlockIndex] = *text
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, ContentIndex: state.CurrentBlockIndex, Delta: part.Text, Partial: cloneAssistantMessage(*response)})
		}
	}

	if candidate.FinishReason != "" {
		response.StopReason = mapGoogleStopReason(candidate.FinishReason, response.Content)
	}

	return false, nil
}

func googleSameBlockType(block any, isThinking bool) bool {
	switch block.(type) {
	case *TextContent:
		return !isThinking
	case *ThinkingContent:
		return isThinking
	default:
		return false
	}
}

func finishGoogleCurrentBlock(response *AssistantMessage, stream *AssistantMessageEventStream, state *googleStreamState) {
	if state.CurrentBlock == nil {
		return
	}
	switch block := state.CurrentBlock.(type) {
	case *TextContent:
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextEnd, ContentIndex: state.CurrentBlockIndex, Content: block.Text, Partial: cloneAssistantMessage(*response)})
	case *ThinkingContent:
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingEnd, ContentIndex: state.CurrentBlockIndex, Content: block.Thinking, Partial: cloneAssistantMessage(*response)})
	}
	state.CurrentBlock = nil
}

func finalizeGoogleResponse(model Model, response *AssistantMessage, stream *AssistantMessageEventStream, state *googleStreamState) {
	finishGoogleCurrentBlock(response, stream, state)
	if response.StopReason == "" {
		response.StopReason = StopReasonStop
	}
	response.Usage.Cost = CalculateCost(model, response.Usage)
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: response.StopReason, Message: *response})
	stream.finish(*response)
}

func googleToolCallID(id, name string, state *googleStreamState) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	state.ToolCallCounter++
	return fmt.Sprintf("%s_%d_%d", name, time.Now().UnixMilli(), state.ToolCallCounter)
}

func applyGoogleUsage(response *AssistantMessage, model Model, usage googleUsageMetadata) {
	response.Usage = Usage{
		Input:       usage.PromptTokenCount - usage.CachedContentTokenCount,
		Output:      usage.CandidatesTokenCount + usage.ThoughtsTokenCount,
		CacheRead:   usage.CachedContentTokenCount,
		CacheWrite:  0,
		TotalTokens: usage.TotalTokenCount,
	}
	response.Usage.Cost = CalculateCost(model, response.Usage)
}

func mapGoogleStopReason(reason string, content []ContentBlock) StopReason {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "STOP":
		for _, block := range content {
			if _, ok := block.(ToolCall); ok {
				return StopReasonToolUse
			}
		}
		return StopReasonStop
	case "MAX_TOKENS":
		return StopReasonLength
	default:
		return StopReasonError
	}
}

func parseGoogleErrorMessage(body []byte) string {
	var payload googleErrorPayload
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Message) != "" {
		return payload.Message
	}
	return strings.TrimSpace(string(body))
}

func cloneGoogleHTTPHeaders(headers http.Header) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		result[key] = strings.Join(values, ",")
	}
	return result
}
