package pigo

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/Icatme/pi-go/internal/jsontext"
)

// ============================================================================
// Shared request/response types for OpenAI Responses API
// Used by both openai-codex and openai-responses providers
// ============================================================================

type openAIResponsesRequest struct {
	Model                string                             `json:"model"`
	Store                bool                               `json:"store"`
	Stream               bool                               `json:"stream"`
	Instructions         string                             `json:"instructions,omitempty"`
	Input                []map[string]any                   `json:"input,omitempty"`
	Tools                []map[string]any                   `json:"tools,omitempty"`
	ToolChoice           string                             `json:"tool_choice,omitempty"`
	ParallelToolCalls    *bool                              `json:"parallel_tool_calls,omitempty"`
	Temperature          *float64                           `json:"temperature,omitempty"`
	TopP                 *float64                           `json:"top_p,omitempty"`
	Reasoning            *openAIResponsesReasoningOptions   `json:"reasoning,omitempty"`
	ServiceTier          string                             `json:"service_tier,omitempty"`
	Text                 *openAIResponsesTextOptions        `json:"text,omitempty"`
	Include              []string                           `json:"include,omitempty"`
	PromptCacheKey       string                             `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string                             `json:"prompt_cache_retention,omitempty"`
	PromptCacheOptions   *openAIResponsesPromptCacheOptions `json:"prompt_cache_options,omitempty"`
	MaxOutputTokens      int                                `json:"max_output_tokens,omitempty"`
	Metadata             map[string]any                     `json:"metadata,omitempty"`
	PreviousResponseID   string                             `json:"previous_response_id,omitempty"`
	Truncation           string                             `json:"truncation,omitempty"`
}

type openAIResponsesPromptCacheOptions struct {
	Mode string `json:"mode,omitempty"`
	TTL  string `json:"ttl,omitempty"`
}

type openAIResponsesReasoningOptions struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type openAIResponsesTextOptions struct {
	Verbosity string                     `json:"verbosity,omitempty"`
	Format    *openAIResponsesTextFormat `json:"format,omitempty"`
}

type openAIResponsesTextFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Strict bool            `json:"strict,omitempty"`
}

type openAIResponsesResponse struct {
	ID          string                        `json:"id"`
	Status      string                        `json:"status"`
	ServiceTier string                        `json:"service_tier,omitempty"`
	Output      []openAIResponsesResponseItem `json:"output"`
	Usage       *openAIResponsesUsage         `json:"usage,omitempty"`
	Error       *openAIResponsesResponseError `json:"error,omitempty"`
}

type openAIResponsesResponseItem struct {
	Type             string                               `json:"type"`
	ID               string                               `json:"id,omitempty"`
	Status           string                               `json:"status,omitempty"`
	CallID           string                               `json:"call_id,omitempty"`
	Name             string                               `json:"name,omitempty"`
	Namespace        string                               `json:"namespace,omitempty"`
	Arguments        string                               `json:"arguments,omitempty"`
	Input            *string                              `json:"input,omitempty"`
	Summary          []openAIResponsesReasoningSummary    `json:"summary,omitempty"`
	Content          []openAIResponsesResponseContentPart `json:"content,omitempty"`
	EncryptedContent string                               `json:"encrypted_content,omitempty"`
}

type openAIResponsesReasoningSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAIResponsesResponseContentPart struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

type openAIResponsesUsage struct {
	InputTokens  int                              `json:"input_tokens"`
	OutputTokens int                              `json:"output_tokens"`
	TotalTokens  int                              `json:"total_tokens"`
	InputDetails openAIResponsesInputTokenDetails `json:"input_tokens_details"`
}

type openAIResponsesInputTokenDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

type openAIResponsesResponseError struct {
	Code     string `json:"code"`
	Type     string `json:"type"`
	Message  string `json:"message"`
	PlanType string `json:"plan_type"`
	ResetsAt int64  `json:"resets_at"`
}

// ============================================================================
// Shared streaming state
// ============================================================================

type openAIResponsesStreamingState struct {
	CurrentTextIndex       int
	CurrentThinkingIndex   int
	CurrentTextItemKey     string
	CurrentThinkingItemKey string
	FinalizedItemKeys      map[string]bool
	ToolCalls              map[string]*openAIResponsesToolCallState
	ToolCallOutputKeys     map[int]string
	CustomTools            map[string]string
	CustomInputBytes       int
	CustomCalls            int
	CustomOutputSeen       bool
}

type openAIResponsesToolCallState struct {
	ContentIndex  int
	ItemID        string
	CallID        string
	JSON          string
	Completed     bool
	Name          string
	InputProperty string
	Input         string
	InputStarted  bool
	InputClosed   bool
}

// ============================================================================
// Shared message conversion
// ============================================================================

func convertOpenAIResponsesMessages(custom map[string]string, model Model, ctx Context, includeSystemPrompt bool) []map[string]any {
	transcript := ResolveTranscript(NormalizeContext(ctx), supportsTranscriptSystemMessages(model))
	transformed := TransformMessages(transcript.Messages, model, NormalizeOpenAIResponsesToolCallID)
	input := make([]map[string]any, 0, len(transformed)+1)
	customCallIDs := make(map[string]bool)

	for index, message := range transformed {
		switch typed := message.(type) {
		case SystemMessage:
			if index == 0 && !includeSystemPrompt {
				continue
			}
			text := RenderSystemMessageUpdate(typed)
			if index == 0 {
				text = GetSystemMessageText(typed)
			}
			if text != "" {
				role := "system"
				if model.Reasoning {
					role = "developer"
				}
				input = append(input, map[string]any{"role": role, "content": text})
			}
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
					prefix := "fc_"
					if property := custom[block.Name]; property != "" {
						functionCall["type"] = "custom_tool_call"
						functionCall["input"] = block.Arguments[property]
						delete(functionCall, "arguments")
						customCallIDs[callID] = true
						prefix = "ctc_"
					}
					if strings.HasPrefix(itemID, prefix) && typed.Provider == model.Provider && typed.API == model.API && typed.Model == model.ID {
						functionCall["id"] = itemID
					}
					input = append(input, functionCall)
				}
			}
		case ToolResultMessage:
			callID, _ := splitToolCallID(typed.ToolCallID)
			kind := "function_call_output"
			if customCallIDs[callID] {
				kind = "custom_tool_call_output"
			}
			input = append(input, map[string]any{
				"type":    kind,
				"call_id": callID,
				"output":  buildOpenAIResponsesToolResultOutput(typed.Content, model),
			})
		}
	}

	return input
}

func convertOpenAIResponsesTools(custom map[string]string, tools []Tool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}

	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if custom[tool.Name] != "" {
			config := tool.ConstrainedSampling
			format := map[string]any{"type": config.Type}
			if config.Type == "grammar" {
				format["syntax"], format["definition"] = config.Syntax, config.Definition
			}
			result = append(result, map[string]any{"type": "custom", "name": tool.Name, "description": tool.Description, "format": format})
			continue
		}
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

func buildOpenAIResponsesToolResultOutput(blocks []ContentBlock, model Model) any {
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

// ============================================================================
// Shared stream event processing
// ============================================================================

func processOpenAIResponsesStreamEvent(
	data string,
	model Model,
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
	state *openAIResponsesStreamingState,
	requestServiceTier string,
) (bool, error) {
	return processOpenAIResponsesStreamEventWithProvider(data, model, response, stream, state, requestServiceTier, "")
}

func processOpenAIResponsesStreamEventWithProvider(
	data string,
	model Model,
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
	state *openAIResponsesStreamingState,
	requestServiceTier string,
	providerName string,
) (bool, error) {
	if strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "[DONE]" {
		return false, nil
	}
	if len(state.CustomTools) > 0 {
		// Source text must not be silently changed by encoding/json's Unicode
		// replacement behavior. Transport readers already bound each frame.
		if err := jsontext.ValidateUnicode([]byte(data)); err != nil {
			return false, err
		}
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return false, err
	}
	markOpenAIResponsesCustomOutput(state, event)
	if state.FinalizedItemKeys == nil {
		state.FinalizedItemKeys = map[string]bool{}
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
		var item openAIResponsesResponseItem
		if err := json.Unmarshal(itemBytes, &item); err != nil {
			return false, err
		}
		if item.Type == "custom_tool_call" {
			return false, processOpenAIResponsesCustomItem(response, stream, state, event, item, false)
		}
		if item.Type == "function_call" {
			if err := validateOpenAIResponsesFunctionItem(state, item); err != nil {
				return false, err
			}
		}
		startOpenAIResponsesStreamItem(response, stream, state, item)
		if item.Type == "function_call" {
			if err := registerOpenAIResponsesToolOutputIndex(state, event, item); err != nil {
				return false, err
			}
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if state.CurrentThinkingIndex < 0 {
			startOpenAIResponsesThinkingBlock(response, stream, state)
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
	case "response.reasoning_summary_part.added":
		if state.CurrentThinkingIndex < 0 {
			startOpenAIResponsesThinkingBlock(response, stream, state)
		}
	case "response.reasoning_summary_part.done":
		if state.CurrentThinkingIndex >= 0 {
			block, _ := response.Content[state.CurrentThinkingIndex].(ThinkingContent)
			block.Thinking += "\n\n"
			response.Content[state.CurrentThinkingIndex] = block
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventThinkingDelta,
				ContentIndex: state.CurrentThinkingIndex,
				Delta:        "\n\n",
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
				startOpenAIResponsesTextBlock(response, stream, state)
			}
		}
	case "response.output_text.delta", "response.refusal.delta":
		if state.CurrentTextIndex < 0 {
			startOpenAIResponsesTextBlock(response, stream, state)
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
	case "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		return false, processOpenAIResponsesCustomInput(response, stream, state, event, eventType)
	case "response.function_call_arguments.delta":
		call, err := resolveOpenAIResponsesToolCall(state, event)
		if err != nil {
			return false, err
		}
		if call != nil && call.InputProperty != "" {
			return false, fmt.Errorf("OpenAI Responses function delta targets custom input")
		}
		if call == nil || call.Completed {
			return false, nil
		}
		delta, _ := event["delta"].(string)
		call.JSON += delta
		block, _ := response.Content[call.ContentIndex].(ToolCall)
		block.Arguments = parseStreamingJSONObject(call.JSON)
		response.Content[call.ContentIndex] = block
		stream.push(AssistantMessageEvent{
			Type:         AssistantMessageEventToolCallDelta,
			ContentIndex: call.ContentIndex,
			Delta:        delta,
			Partial:      *response,
		})
	case "response.function_call_arguments.done":
		call, err := resolveOpenAIResponsesToolCall(state, event)
		if err != nil {
			return false, err
		}
		if call != nil && call.InputProperty != "" {
			return false, fmt.Errorf("OpenAI Responses function done targets custom input")
		}
		if call == nil || call.Completed {
			return false, nil
		}
		arguments, _ := event["arguments"].(string)
		if arguments != "" {
			previousJSON := call.JSON
			call.JSON = arguments
			block, _ := response.Content[call.ContentIndex].(ToolCall)
			block.Arguments = parseStreamingJSONObject(call.JSON)
			response.Content[call.ContentIndex] = block
			if strings.HasPrefix(arguments, previousJSON) {
				delta := arguments[len(previousJSON):]
				if delta != "" {
					stream.push(AssistantMessageEvent{
						Type:         AssistantMessageEventToolCallDelta,
						ContentIndex: call.ContentIndex,
						Delta:        delta,
						Partial:      *response,
					})
				}
			}
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
		var item openAIResponsesResponseItem
		if err := json.Unmarshal(itemBytes, &item); err != nil {
			return false, err
		}
		if item.Type == "custom_tool_call" {
			return false, processOpenAIResponsesCustomItem(response, stream, state, event, item, true)
		}
		if item.Type == "function_call" {
			if err := registerOpenAIResponsesToolOutputIndex(state, event, item); err != nil {
				return false, err
			}
		}
		if err := finalizeOpenAIResponsesStreamItem(response, stream, state, item); err != nil {
			return false, err
		}
	case "response.completed", "response.done", "response.incomplete":
		responseMap, ok := event["response"].(map[string]any)
		if !ok {
			return false, fmt.Errorf("missing terminal response")
		}
		responseBytes, err := json.Marshal(responseMap)
		if err != nil {
			return false, err
		}
		var terminal openAIResponsesResponse
		if err := json.Unmarshal(responseBytes, &terminal); err != nil {
			return false, err
		}
		outputErr := emitOpenAIResponsesTerminalOutputIfNeeded(response, stream, state, terminal)
		applyOpenAIResponsesTerminal(model, response, terminal, requestServiceTier)
		if outputErr != nil {
			return true, outputErr
		}
		if response.StopReason == StopReasonToolUse {
			for _, call := range state.ToolCalls {
				if !call.Completed {
					block := response.Content[call.ContentIndex].(ToolCall)
					return true, fmt.Errorf("OpenAI Responses stream completed with an unfinished tool call: %s (%s)", block.Name, block.ID)
				}
			}
		}
		return true, nil
	case "response.failed":
		responseMap, ok := event["response"].(map[string]any)
		if !ok {
			return false, fmt.Errorf("response failed")
		}
		responseBytes, err := json.Marshal(responseMap)
		if err != nil {
			return false, err
		}
		var terminal openAIResponsesResponse
		if err := json.Unmarshal(responseBytes, &terminal); err != nil {
			return false, err
		}
		if terminal.ID != "" {
			response.ResponseID = terminal.ID
		}
		if terminal.Usage != nil {
			applyOpenAIResponsesUsage(model, response, *terminal.Usage, terminal.ServiceTier, requestServiceTier)
		}
		if terminal.Error != nil && strings.TrimSpace(terminal.Error.Message) != "" {
			response.StopReason = StopReasonError
			response.ErrorMessage = terminal.Error.Message
		} else {
			response.StopReason = StopReasonError
			if providerName == "codex" {
				response.ErrorMessage = "codex response failed"
			} else {
				response.ErrorMessage = "response failed"
			}
		}
		return true, errors.New(response.ErrorMessage)
	case "error":
		if message, ok := event["message"].(string); ok && strings.TrimSpace(message) != "" {
			response.ErrorMessage = message
		} else {
			response.ErrorMessage = "error"
		}
		response.StopReason = StopReasonError
		return true, errors.New(response.ErrorMessage)
	}

	return false, nil
}

// ============================================================================
// Shared stream item lifecycle
// ============================================================================

func registerOpenAIResponsesToolOutputIndex(state *openAIResponsesStreamingState, event map[string]any, item openAIResponsesResponseItem) error {
	index, ok := event["output_index"].(float64)
	if !ok {
		return nil
	}
	if index < 0 || index > math.MaxInt32 || index != math.Trunc(index) {
		return fmt.Errorf("OpenAI Responses invalid tool output_index: %v", index)
	}
	key := openAIResponsesItemKey(item)
	if previous, exists := state.ToolCallOutputKeys[int(index)]; exists && previous != key {
		return fmt.Errorf("OpenAI Responses conflicting tool call identity at output_index %d", int(index))
	}
	if state.ToolCallOutputKeys == nil {
		state.ToolCallOutputKeys = map[int]string{}
	}
	state.ToolCallOutputKeys[int(index)] = key
	return nil
}

func resolveOpenAIResponsesToolCall(state *openAIResponsesStreamingState, event map[string]any) (*openAIResponsesToolCallState, error) {
	itemID, _ := event["item_id"].(string)
	callID, _ := event["call_id"].(string)
	var call *openAIResponsesToolCallState
	if itemID != "" || callID != "" {
		for _, candidate := range state.ToolCalls {
			if (itemID == "" || candidate.ItemID == itemID) && (callID == "" || candidate.CallID == callID) {
				if call != nil {
					return nil, fmt.Errorf("OpenAI Responses ambiguous tool call identity")
				}
				call = candidate
			}
		}
		if call == nil {
			return nil, fmt.Errorf("OpenAI Responses unknown tool call identity: item_id=%q call_id=%q", itemID, callID)
		}
	}
	if index, ok := event["output_index"].(float64); ok {
		if index < 0 || index > math.MaxInt32 || index != math.Trunc(index) {
			return nil, fmt.Errorf("OpenAI Responses invalid tool output_index: %v", index)
		}
		indexedCall := state.ToolCalls[state.ToolCallOutputKeys[int(index)]]
		if call != nil && indexedCall != nil && call != indexedCall {
			return nil, fmt.Errorf("OpenAI Responses conflicting tool call identity at output_index %d", int(index))
		}
		if call != nil {
			return call, nil
		}
		if indexedCall == nil {
			return nil, fmt.Errorf("OpenAI Responses unknown tool output_index %d", int(index))
		}
		return indexedCall, nil
	}
	if call != nil {
		return call, nil
	}
	// Identity-free events are safe only while exactly one call is open.
	// Never associate an ambiguous delta with whichever item was added last.
	for _, candidate := range state.ToolCalls {
		if !candidate.Completed {
			if call != nil {
				return nil, fmt.Errorf("OpenAI Responses ambiguous tool call event without item_id, call_id, or output_index")
			}
			call = candidate
		}
	}
	return call, nil
}

func emitOpenAIResponsesTerminalOutputIfNeeded(
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
	state *openAIResponsesStreamingState,
	terminal openAIResponsesResponse,
) error {
	if len(terminal.Output) == 0 {
		return nil
	}

	for _, item := range terminal.Output {
		if item.Type == "custom_tool_call" {
			if err := processOpenAIResponsesCustomItem(response, stream, state, nil, item, true); err != nil {
				return err
			}
			continue
		}
		itemKey := openAIResponsesItemKey(item)
		if itemKey != "" && state != nil && state.FinalizedItemKeys[itemKey] {
			continue
		}

		switch item.Type {
		case "message":
			if state != nil && state.CurrentTextIndex >= 0 && state.CurrentTextItemKey == itemKey {
				if err := finalizeOpenAIResponsesStreamItem(response, stream, state, item); err != nil {
					return err
				}
				continue
			}
		case "reasoning":
			if state != nil && state.CurrentThinkingIndex >= 0 && state.CurrentThinkingItemKey == itemKey {
				if err := finalizeOpenAIResponsesStreamItem(response, stream, state, item); err != nil {
					return err
				}
				continue
			}
		case "function_call":
			if err := finalizeOpenAIResponsesStreamItem(response, stream, state, item); err != nil {
				return err
			}
			continue
		}

		emitOpenAIResponsesItemLifecycle(response, stream, item)
		if itemKey != "" && state != nil {
			state.FinalizedItemKeys[itemKey] = true
		}
	}
	return nil
}

func openAIResponsesItemKey(item openAIResponsesResponseItem) string {
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
	case "custom_tool_call":
		return "custom_tool_call|" + item.CallID + "|" + item.ID
	default:
		return ""
	}
}

func emitOpenAIResponsesItemLifecycle(response *AssistantMessage, stream *AssistantMessageEventStream, item openAIResponsesResponseItem) {
	for _, block := range parseOpenAIResponsesResponseOutput([]openAIResponsesResponseItem{item}) {
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

func startOpenAIResponsesStreamItem(
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
	state *openAIResponsesStreamingState,
	item openAIResponsesResponseItem,
) {
	itemKey := openAIResponsesItemKey(item)
	switch item.Type {
	case "message":
		startOpenAIResponsesTextBlock(response, stream, state)
		state.CurrentTextItemKey = itemKey
	case "reasoning":
		startOpenAIResponsesThinkingBlock(response, stream, state)
		state.CurrentThinkingItemKey = itemKey
	case "function_call":
		if state.ToolCalls == nil {
			state.ToolCalls = map[string]*openAIResponsesToolCallState{}
		}
		if _, exists := state.ToolCalls[itemKey]; exists {
			return
		}
		contentIndex := len(response.Content)
		response.Content = append(response.Content, ToolCall{
			ID:        combineOpenAIResponsesToolCallID(item.CallID, item.ID),
			Name:      item.Name,
			Arguments: parseStreamingJSONObject(item.Arguments),
		})
		state.ToolCalls[itemKey] = &openAIResponsesToolCallState{
			ContentIndex: contentIndex, ItemID: item.ID, CallID: item.CallID, JSON: item.Arguments,
		}
		stream.push(AssistantMessageEvent{
			Type:         AssistantMessageEventToolCallStart,
			ContentIndex: contentIndex,
			Partial:      *response,
		})
	}
}

func startOpenAIResponsesTextBlock(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAIResponsesStreamingState) {
	contentIndex := len(response.Content)
	response.Content = append(response.Content, TextContent{})
	state.CurrentTextIndex = contentIndex
	stream.push(AssistantMessageEvent{
		Type:         AssistantMessageEventTextStart,
		ContentIndex: contentIndex,
		Partial:      *response,
	})
}

func startOpenAIResponsesThinkingBlock(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAIResponsesStreamingState) {
	contentIndex := len(response.Content)
	response.Content = append(response.Content, ThinkingContent{})
	state.CurrentThinkingIndex = contentIndex
	stream.push(AssistantMessageEvent{
		Type:         AssistantMessageEventThinkingStart,
		ContentIndex: contentIndex,
		Partial:      *response,
	})
}

func encodeOpenAIResponsesTextSignatureV1(id string, phase string) string {
	sig := TextSignatureV1{V: 1, ID: id}
	if phase != "" {
		sig.Phase = phase
	}
	bytes, _ := json.Marshal(sig)
	return string(bytes)
}

func finalizeOpenAIResponsesStreamItem(
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
	state *openAIResponsesStreamingState,
	item openAIResponsesResponseItem,
) error {
	if item.Type == "function_call" {
		if err := validateOpenAIResponsesFunctionItem(state, item); err != nil {
			return err
		}
	}
	itemKey := openAIResponsesItemKey(item)
	switch item.Type {
	case "message":
		if state.CurrentTextIndex < 0 {
			emitOpenAIResponsesItemLifecycle(response, stream, item)
			if itemKey != "" {
				state.FinalizedItemKeys[itemKey] = true
			}
			return nil
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
		if strings.TrimSpace(item.ID) != "" {
			block.TextSignature = encodeOpenAIResponsesTextSignatureV1(item.ID, "")
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
			emitOpenAIResponsesItemLifecycle(response, stream, item)
			if itemKey != "" {
				state.FinalizedItemKeys[itemKey] = true
			}
			return nil
		}
		block, _ := response.Content[state.CurrentThinkingIndex].(ThinkingContent)
		if text := openAIResponsesReasoningText(item); text != "" {
			block.Thinking = text
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
		call := state.ToolCalls[itemKey]
		newCall := call == nil
		if call == nil {
			startOpenAIResponsesStreamItem(response, stream, state, item)
			call = state.ToolCalls[itemKey]
		}
		if call.Completed {
			return nil
		}
		if item.Status != "" && item.Status != "completed" {
			return fmt.Errorf("OpenAI Responses unfinished tool call %s (%s): status %s", item.Name, item.CallID, item.Status)
		}
		argumentsJSON := item.Arguments
		if strings.TrimSpace(argumentsJSON) == "" {
			argumentsJSON = call.JSON
		}
		if strings.TrimSpace(argumentsJSON) == "" {
			argumentsJSON = "{}"
		}
		var arguments map[string]any
		if err := json.Unmarshal([]byte(argumentsJSON), &arguments); err != nil {
			return fmt.Errorf("OpenAI Responses tool call %s (%s) has invalid arguments: %w", item.Name, item.CallID, err)
		}
		if arguments == nil {
			return fmt.Errorf("OpenAI Responses tool call %s (%s) has invalid arguments: expected a JSON object", item.Name, item.CallID)
		}
		if strings.TrimSpace(item.CallID) == "" || strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("OpenAI Responses tool call is missing call_id or name")
		}
		block, _ := response.Content[call.ContentIndex].(ToolCall)
		block.Arguments = arguments
		block.ID = combineOpenAIResponsesToolCallID(item.CallID, item.ID)
		block.Name = item.Name
		response.Content[call.ContentIndex] = block
		call.Completed = true
		call.JSON = ""
		if newCall {
			stream.push(AssistantMessageEvent{
				Type:         AssistantMessageEventToolCallDelta,
				ContentIndex: call.ContentIndex,
				Delta:        argumentsJSON,
				Partial:      *response,
			})
		}
		stream.push(AssistantMessageEvent{
			Type:         AssistantMessageEventToolCallEnd,
			ContentIndex: call.ContentIndex,
			ToolCall:     block,
			Partial:      *response,
		})
	}
	if itemKey != "" {
		if state.FinalizedItemKeys == nil {
			state.FinalizedItemKeys = map[string]bool{}
		}
		state.FinalizedItemKeys[itemKey] = true
	}
	return nil
}

func combineOpenAIResponsesToolCallID(callID string, itemID string) string {
	if itemID == "" {
		return callID
	}
	return callID + "|" + itemID
}

// ============================================================================
// Shared terminal response handling
// ============================================================================

func applyOpenAIResponsesTerminal(model Model, response *AssistantMessage, terminal openAIResponsesResponse, requestServiceTier string) {
	if terminal.ID != "" {
		response.ResponseID = terminal.ID
	}
	if len(response.Content) == 0 && len(terminal.Output) > 0 {
		response.Content = append(response.Content, parseOpenAIResponsesResponseOutput(terminal.Output)...)
	}
	response.StopReason = mapOpenAIResponsesStopReason(terminal.Status, response.Content)
	if terminal.Usage != nil {
		applyOpenAIResponsesUsage(model, response, *terminal.Usage, terminal.ServiceTier, requestServiceTier)
	}
}

func applyOpenAIResponsesUsage(model Model, response *AssistantMessage, usage openAIResponsesUsage, responseServiceTier string, requestServiceTier string) {
	inputTokens := usage.InputTokens - usage.InputDetails.CachedTokens - usage.InputDetails.CacheWriteTokens
	if inputTokens < 0 {
		inputTokens = 0
	}
	response.Usage = Usage{
		Input:       inputTokens,
		Output:      usage.OutputTokens,
		CacheRead:   usage.InputDetails.CachedTokens,
		CacheWrite:  usage.InputDetails.CacheWriteTokens,
		TotalTokens: usage.TotalTokens,
	}
	response.UsageReported = true
	response.Usage.Cost = calculateProviderUsageCost(model, response.Usage)
	if model.Provider != "opencode-go" {
		applyOpenAIResponsesServiceTierPricing(model, &response.Usage, resolveOpenAIResponsesServiceTier(responseServiceTier, requestServiceTier))
	}
}

func parseOpenAIResponsesResponseOutput(items []openAIResponsesResponseItem) []ContentBlock {
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
			thinking := openAIResponsesReasoningText(item)
			if thinking == "" && strings.TrimSpace(item.EncryptedContent) == "" {
				continue
			}
			signatureBytes, err := json.Marshal(item)
			if err != nil {
				signatureBytes = nil
			}
			blocks = append(blocks, ThinkingContent{
				Thinking:          thinking,
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

func openAIResponsesReasoningText(item openAIResponsesResponseItem) string {
	parts := make([]string, 0, len(item.Summary)+len(item.Content))
	for _, summary := range item.Summary {
		if strings.TrimSpace(summary.Text) != "" {
			parts = append(parts, summary.Text)
		}
	}
	if len(parts) == 0 {
		for _, content := range item.Content {
			if content.Type == "reasoning_text" && strings.TrimSpace(content.Text) != "" {
				parts = append(parts, content.Text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func mapOpenAIResponsesStopReason(status string, content []ContentBlock) StopReason {
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

// ============================================================================
// Shared error parsing
// ============================================================================

func parseOpenAIResponsesError(body []byte, fallback string) string {
	return parseOpenAIResponsesErrorWithProvider(body, fallback, "")
}

func parseOpenAIResponsesErrorWithProvider(body []byte, fallback string, providerName string) string {
	var payload openAIResponsesResponse
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error != nil {
		if friendly := buildOpenAIResponsesFriendlyErrorMessage(payload.Error, fallback, providerName); friendly != "" {
			return friendly
		}
		if strings.TrimSpace(payload.Error.Message) != "" {
			return payload.Error.Message
		}
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fallback
	}
	return text
}

func buildOpenAIResponsesFriendlyErrorMessage(err *openAIResponsesResponseError, fallback string, providerName string) string {
	if err == nil {
		return ""
	}

	code := strings.TrimSpace(err.Code)
	if code == "" {
		code = strings.TrimSpace(err.Type)
	}
	if !regexp.MustCompile(`(?i)usage_limit_reached|usage_not_included|rate_limit_exceeded`).MatchString(code) &&
		!strings.HasPrefix(strings.TrimSpace(fallback), "429") {
		return ""
	}

	plan := ""
	if strings.TrimSpace(err.PlanType) != "" {
		plan = " (" + strings.ToLower(strings.TrimSpace(err.PlanType)) + " plan)"
	}
	when := ""
	if err.ResetsAt > 0 {
		minutes := int(math.Round(float64(err.ResetsAt-time.Now().Unix()) / 60.0))
		if minutes < 0 {
			minutes = 0
		}
		when = fmt.Sprintf(" Try again in ~%d min.", minutes)
	}

	product := "your usage limit"
	if providerName == "codex" {
		product = "your ChatGPT usage limit"
	}

	return strings.TrimSpace(fmt.Sprintf("You have hit %s%s.%s", product, plan, when))
}

// ============================================================================
// Shared service tier helpers
// ============================================================================

func getOpenAIResponsesServiceTierCostMultiplier(model Model, serviceTier string) float64 {
	switch serviceTier {
	case "flex":
		return 0.5
	case "priority", "fast":
		if model.ID == "gpt-5.5" {
			return 2.5
		}
		return 2
	default:
		return 1
	}
}

func applyOpenAIResponsesServiceTierPricing(model Model, usage *Usage, serviceTier string) {
	multiplier := getOpenAIResponsesServiceTierCostMultiplier(model, serviceTier)
	if multiplier == 1 || usage == nil {
		return
	}
	usage.Cost.Input *= multiplier
	usage.Cost.Output *= multiplier
	usage.Cost.CacheRead *= multiplier
	usage.Cost.CacheWrite *= multiplier
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
}

func resolveOpenAIResponsesServiceTier(responseServiceTier string, requestServiceTier string) string {
	if responseServiceTier == "default" && (requestServiceTier == "flex" || requestServiceTier == "priority") {
		return requestServiceTier
	}
	if strings.TrimSpace(responseServiceTier) != "" {
		return responseServiceTier
	}
	return requestServiceTier
}

// ============================================================================
// Shared reasoning effort clamping
// ============================================================================

func clampOpenAIResponsesReasoningEffort(model Model, level ThinkingLevel) string {
	effort := strings.TrimSpace(string(level))
	if effort == "" {
		return ""
	}
	if len(model.ThinkingLevelMap) > 0 {
		clamped := ClampThinkingLevel(model, ModelThinkingLevel(effort))
		if mapped, ok := model.ThinkingLevelMap[clamped]; ok {
			return strings.TrimSpace(mapped)
		}
		return string(clamped)
	}

	id := model.ID
	if strings.Contains(id, "/") {
		parts := strings.Split(id, "/")
		id = parts[len(parts)-1]
	}
	if effort == string(ThinkingLevelMax) {
		if mapped := strings.TrimSpace(model.ThinkingLevelMap[ModelThinkingLevelMax]); mapped != "" {
			return mapped
		}
		if SupportsXHigh(model) {
			effort = string(ThinkingLevelXHigh)
		} else {
			effort = string(ThinkingLevelHigh)
		}
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

// ============================================================================
// Shared retry helpers
// ============================================================================

func shouldRetryOpenAIResponsesRequest(status int, message string) bool {
	if isOpenAINonRetryableLimitError("", "", message) {
		return false
	}
	if status == 429 || status == 500 || status == 502 || status == 503 || status == 504 || status == 520 {
		return true
	}

	lower := strings.ToLower(message)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "ratelimit") ||
		strings.Contains(lower, "overloaded") ||
		strings.Contains(lower, "model is at capacity") ||
		strings.Contains(lower, "currently experiencing high demand") ||
		strings.Contains(lower, "service unavailable") ||
		strings.Contains(lower, "upstream connect") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "exceeded request buffer limit while retrying upstream")
}

func shouldRetryOpenAIProviderResponse(status int, body []byte, message string) bool {
	var envelope struct {
		Error struct {
			Code    any    `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		return shouldRetryOpenAIProviderError(
			status,
			openAIProviderErrorCode(envelope.Error.Code),
			envelope.Error.Type,
			envelope.Error.Message,
			message,
		)
	}
	return shouldRetryOpenAIResponsesRequest(status, message)
}

func shouldRetryOpenAIProviderError(status int, code string, errorType string, providerMessage string, fallback string) bool {
	identifier := strings.ToLower(strings.TrimSpace(code + " " + errorType))
	if strings.Contains(identifier, "rate_limit_exceeded") || strings.Contains(identifier, "rate limit exceeded") {
		return true
	}
	if isOpenAINonRetryableLimitError(code, errorType, providerMessage) {
		return false
	}
	if strings.TrimSpace(code) != "" || strings.TrimSpace(errorType) != "" || strings.TrimSpace(providerMessage) != "" {
		return shouldRetryOpenAIResponsesRequest(status, providerMessage)
	}
	return shouldRetryOpenAIResponsesRequest(status, fallback)
}

func openAIProviderErrorCode(code any) string {
	if code == nil {
		return ""
	}
	return fmt.Sprint(code)
}

func isOpenAINonRetryableLimitError(code string, errorType string, message string) bool {
	lower := strings.ToLower(strings.Join([]string{code, errorType, message}, " "))
	patterns := []string{
		"gousagelimiterror",
		"freeusagelimiterror",
		"usage_limit",
		"usage limit",
		"usage_not_included",
		"insufficient_quota",
		"quota exceeded",
		"quota_exceeded",
		"billing",
		"payment_required",
		"payment required",
		"available balance",
		"credit balance",
		"monthly limit",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}
