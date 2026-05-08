package pigo

import "time"

const (
	nonVisionUserImagePlaceholder = "(image omitted: model does not support images)"
	nonVisionToolImagePlaceholder = "(tool image omitted: model does not support images)"
)

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

func downgradeUnsupportedImages(messages []Message, model Model) []Message {
	if modelSupportsImages(model) {
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

func TransformMessages(
	messages []Message,
	model Model,
	normalizeToolCallID func(string, Model, AssistantMessage) string,
) []Message {
	toolCallIDMap := map[string]string{}
	imageAwareMessages := downgradeUnsupportedImages(messages, model)
	transformed := make([]Message, 0, len(imageAwareMessages))

	for _, message := range imageAwareMessages {
		switch typed := message.(type) {
		case UserMessage:
			transformed = append(transformed, typed.clone())
		case ToolResultMessage:
			toolCallID := typed.ToolCallID
			if normalizedID, ok := toolCallIDMap[typed.ToolCallID]; ok {
				toolCallID = normalizedID
			}
			transformed = append(transformed, ToolResultMessage{
				ToolCallID: toolCallID,
				ToolName:   typed.ToolName,
				Content:    cloneBlocks(typed.Content),
				IsError:    typed.IsError,
				Timestamp:  typed.Timestamp,
			})
		case AssistantMessage:
			isSameModel := typed.Provider == model.Provider && typed.API == model.API && typed.Model == model.ID
			nextContent := make([]ContentBlock, 0, len(typed.Content))
			for _, block := range typed.Content {
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
					nextToolCall := ToolCall{
						ID:               content.ID,
						Name:             content.Name,
						Arguments:        cloneMap(content.Arguments),
						ThoughtSignature: content.ThoughtSignature,
					}
					if !isSameModel {
						nextToolCall.ThoughtSignature = ""
					}
					if !isSameModel && normalizeToolCallID != nil {
						normalizedID := normalizeToolCallID(content.ID, model, typed)
						if normalizedID != content.ID {
							toolCallIDMap[content.ID] = normalizedID
							nextToolCall.ID = normalizedID
						}
					}
					nextContent = append(nextContent, nextToolCall)
				case ImageContent:
					nextContent = append(nextContent, content)
				}
			}
			transformed = append(transformed, AssistantMessage{
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

	result := make([]Message, 0, len(transformed))
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

	for _, message := range transformed {
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
