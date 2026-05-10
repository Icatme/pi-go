package pigo

import "time"

const (
	nonVisionUserImagePlaceholder = "(image omitted: model does not support images)"
	nonVisionToolImagePlaceholder = "(tool image omitted: model does not support images)"
)

type MessageTransformer func(transformContext, []Message) []Message

type transformContext struct {
	model               Model
	normalizeToolCallID func(string, Model, AssistantMessage) string
}

var defaultTransformers = []MessageTransformer{
	downgradeUnsupportedImages,
	normalizeThinkingContent,
	normalizeToolCallIDs,
	fillMissingToolResults,
}

func modelSupportsImages(model Model) bool {
	for _, input := range model.Input {
		if input == InputImage {
			return true
		}
	}
	return false
}

func replaceImagesWithPlaceholder(content []ContentBlock, placeholder string) []ContentBlock {
	var result []ContentBlock
	var previousWasPlaceholder bool

	for _, block := range content {
		_, ok := block.(ImageContent)
		if ok {
			if !previousWasPlaceholder {
				result = append(result, TextContent{Text: placeholder})
			}
			previousWasPlaceholder = true
			continue
		}

		result = append(result, block)
		text, ok := block.(TextContent)
		previousWasPlaceholder = ok && text.Text == placeholder
	}

	return result
}

func downgradeUnsupportedImages(ctx transformContext, messages []Message) []Message {
	if modelSupportsImages(ctx.model) {
		return messages
	}

	result := make([]Message, 0, len(messages))
	for _, message := range messages {
		switch typed := message.(type) {
		case UserMessage:
			contentBlocks, ok := typed.Content.([]ContentBlock)
			if ok {
				result = append(result, UserMessage{
					Content:   replaceImagesWithPlaceholder(contentBlocks, nonVisionUserImagePlaceholder),
					Timestamp: typed.Timestamp,
				})
				continue
			}
			result = append(result, typed.clone())
		case ToolResultMessage:
			result = append(result, ToolResultMessage{
				ToolCallID: typed.ToolCallID,
				ToolName:   typed.ToolName,
				Content:    replaceImagesWithPlaceholder(typed.Content, nonVisionToolImagePlaceholder),
				IsError:    typed.IsError,
				Timestamp:  typed.Timestamp,
			})
		default:
			result = append(result, typed.clone())
		}
	}
	return result
}

func normalizeThinkingContent(ctx transformContext, messages []Message) []Message {
	result := make([]Message, 0, len(messages))
	for _, message := range messages {
		assistant, ok := message.(AssistantMessage)
		if !ok {
			result = append(result, message.clone())
			continue
		}

		isSameModel := assistant.Provider == ctx.model.Provider && assistant.API == ctx.model.API && assistant.Model == ctx.model.ID
		nextContent := make([]ContentBlock, 0, len(assistant.Content))
		for _, block := range assistant.Content {
			switch content := block.(type) {
			case ThinkingContent:
				if content.Redacted {
					if isSameModel {
						nextContent = append(nextContent, content)
					}
					continue
				}
				if isSameModel && content.ThinkingSignature != "" {
					nextContent = append(nextContent, content)
					continue
				}
				if content.Thinking == "" {
					continue
				}
				if isSameModel {
					nextContent = append(nextContent, content)
					continue
				}
				nextContent = append(nextContent, TextContent{Text: content.Thinking})
			case TextContent:
				nextContent = append(nextContent, content)
			case ToolCall:
				nextContent = append(nextContent, ToolCall{
					ID:               content.ID,
					Name:             content.Name,
					Arguments:        cloneMap(content.Arguments),
					ThoughtSignature: content.ThoughtSignature,
				})
			case ImageContent:
				nextContent = append(nextContent, content)
			}
		}

		result = append(result, AssistantMessage{
			Content:      nextContent,
			API:          assistant.API,
			Provider:     assistant.Provider,
			Model:        assistant.Model,
			Usage:        assistant.Usage,
			StopReason:   assistant.StopReason,
			ErrorMessage: assistant.ErrorMessage,
			Timestamp:    assistant.Timestamp,
		})
	}
	return result
}

func normalizeToolCallIDs(ctx transformContext, messages []Message) []Message {
	toolCallIDMap := map[string]string{}
	result := make([]Message, 0, len(messages))

	for _, message := range messages {
		switch typed := message.(type) {
		case UserMessage:
			result = append(result, typed.clone())
		case ToolResultMessage:
			toolCallID := typed.ToolCallID
			if normalizedID, ok := toolCallIDMap[typed.ToolCallID]; ok {
				toolCallID = normalizedID
			}
			result = append(result, ToolResultMessage{
				ToolCallID: toolCallID,
				ToolName:   typed.ToolName,
				Content:    cloneBlocks(typed.Content),
				IsError:    typed.IsError,
				Timestamp:  typed.Timestamp,
			})
		case AssistantMessage:
			isSameModel := typed.Provider == ctx.model.Provider && typed.API == ctx.model.API && typed.Model == ctx.model.ID
			nextContent := make([]ContentBlock, 0, len(typed.Content))
			for _, block := range typed.Content {
				switch content := block.(type) {
				case ToolCall:
					nextToolCall := ToolCall{
						ID:               content.ID,
						Name:             content.Name,
						Arguments:        cloneMap(content.Arguments),
						ThoughtSignature: content.ThoughtSignature,
					}
					if !isSameModel {
						nextToolCall.ThoughtSignature = ""
					}
					if !isSameModel && ctx.normalizeToolCallID != nil {
						normalizedID := ctx.normalizeToolCallID(content.ID, ctx.model, typed)
						if normalizedID != content.ID {
							toolCallIDMap[content.ID] = normalizedID
							nextToolCall.ID = normalizedID
						}
					}
					nextContent = append(nextContent, nextToolCall)
				case TextContent:
					nextContent = append(nextContent, content)
				case ThinkingContent:
					nextContent = append(nextContent, content)
				case ImageContent:
					nextContent = append(nextContent, content)
				}
			}
			result = append(result, AssistantMessage{
				Content:      nextContent,
				API:          typed.API,
				Provider:     typed.Provider,
				Model:        typed.Model,
				Usage:        typed.Usage,
				StopReason:   typed.StopReason,
				ErrorMessage: typed.ErrorMessage,
				Timestamp:    typed.Timestamp,
			})
		}
	}

	return result
}

func fillMissingToolResults(_ transformContext, messages []Message) []Message {
	result := make([]Message, 0, len(messages))
	var pendingToolCalls []ToolCall
	existingToolResults := map[string]struct{}{}

	flushPending := func() {
		if len(pendingToolCalls) == 0 {
			return
		}
		for _, call := range pendingToolCalls {
			if _, ok := existingToolResults[call.ID]; ok {
				continue
			}
			result = append(result, ToolResultMessage{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Content:    []ContentBlock{TextContent{Text: "No result provided"}},
				IsError:    true,
				Timestamp:  time.Now().UTC(),
			})
		}
		pendingToolCalls = nil
		existingToolResults = map[string]struct{}{}
	}

	for _, message := range messages {
		switch typed := message.(type) {
		case AssistantMessage:
			if len(pendingToolCalls) > 0 {
				flushPending()
			}
			if typed.StopReason == StopReasonError || typed.StopReason == StopReasonAborted {
				continue
			}
			var toolCalls []ToolCall
			for _, block := range typed.Content {
				call, ok := block.(ToolCall)
				if ok {
					toolCalls = append(toolCalls, call)
				}
			}
			if len(toolCalls) > 0 {
				pendingToolCalls = append([]ToolCall(nil), toolCalls...)
				existingToolResults = map[string]struct{}{}
			}
			result = append(result, typed.clone())
		case ToolResultMessage:
			existingToolResults[typed.ToolCallID] = struct{}{}
			result = append(result, typed.clone())
		case UserMessage:
			if len(pendingToolCalls) > 0 {
				flushPending()
			}
			result = append(result, typed.clone())
		}
	}

	return result
}

func TransformMessages(
	messages []Message,
	model Model,
	normalizeToolCallID func(string, Model, AssistantMessage) string,
) []Message {
	ctx := transformContext{
		model:               model,
		normalizeToolCallID: normalizeToolCallID,
	}

	result := cloneMessages(messages)
	for _, transformer := range defaultTransformers {
		result = transformer(ctx, result)
	}
	return result
}
