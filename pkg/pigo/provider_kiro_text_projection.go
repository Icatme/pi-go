package pigo

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var errKiroTextProjection = errors.New("kiro: unsupported text conversation projection")

const maxKiroTextProjectionBlocks = 4096

type kiroTextUserInput struct {
	Content string `json:"content"`
	ModelID string `json:"modelId"`
}

type kiroTextAssistantResponse struct {
	Content string `json:"content"`
}

type kiroTextHistoryEntry struct {
	User      *kiroTextUserInput         `json:"userInputMessage,omitempty"`
	Assistant *kiroTextAssistantResponse `json:"assistantResponseMessage,omitempty"`
}

// This is deliberately NOT a sendable request: origin identity, profile/auth,
// conversation ID, endpoint and transport are absent. SystemPromptLowered marks
// an explicit semantic downgrade from system instruction to first-user text.
type kiroTextProjection struct {
	History             []kiroTextHistoryEntry
	Current             kiroTextUserInput
	SystemPromptLowered bool
}

// projectKiroTextConversation preserves a strict text-only alternating transcript
// ending in a user turn. It never repairs history, inserts padding, drops tool
// results, flattens reasoning, or calls a custom JSON marshaler/validator.
// Text blocks concatenate without separators; their original boundaries are
// not represented in the projection, while message chronology is preserved.
// maxTextBytes limits aggregate decoded text, not serialized/heap bytes.
func projectKiroTextConversation(ctx Context, modelID string, allowSystemLowering bool, maxMessages, maxTextBytes int) (kiroTextProjection, error) {
	if modelID == "" || !utf8.ValidString(modelID) || len(modelID) > 256 || maxMessages < 1 || maxTextBytes < 1 || len(ctx.Messages) == 0 || len(ctx.Messages) > maxMessages || len(ctx.Messages)%2 == 0 || len(ctx.Tools) != 0 || len(ctx.HostedTools) != 0 {
		return kiroTextProjection{}, errKiroTextProjection
	}
	if ctx.SystemPrompt != "" && (!allowSystemLowering || !utf8.ValidString(ctx.SystemPrompt)) {
		return kiroTextProjection{}, errKiroTextProjection
	}
	used := 0
	remainingBlocks := maxKiroTextProjectionBlocks
	consume := func(text string) bool {
		if !utf8.ValidString(text) || len(text) > maxTextBytes-used {
			return false
		}
		used += len(text)
		return true
	}
	var projection kiroTextProjection
	for index, message := range ctx.Messages {
		// Existing Message types permit both value and pointer use. Normalize
		// pointers without changing or cloning any caller-owned data.
		switch m := message.(type) {
		case *UserMessage:
			if m == nil {
				return kiroTextProjection{}, errKiroTextProjection
			}
			message = *m
		case *AssistantMessage:
			if m == nil {
				return kiroTextProjection{}, errKiroTextProjection
			}
			message = *m
		}
		if index%2 == 0 {
			user, ok := message.(UserMessage)
			if !ok {
				return kiroTextProjection{}, errKiroTextProjection
			}
			var text string
			switch content := user.Content.(type) {
			case string:
				text = content
				if !consume(text) {
					return kiroTextProjection{}, errKiroTextProjection
				}
			case []ContentBlock:
				var valid bool
				text, valid = kiroProjectionText(content, consume, &remainingBlocks)
				if !valid {
					return kiroTextProjection{}, errKiroTextProjection
				}
			default:
				return kiroTextProjection{}, errKiroTextProjection
			}
			if strings.TrimSpace(text) == "" {
				return kiroTextProjection{}, errKiroTextProjection
			}
			if index == 0 && ctx.SystemPrompt != "" {
				if !consume(ctx.SystemPrompt) || !consume("\n\n") {
					return kiroTextProjection{}, errKiroTextProjection
				}
				text = ctx.SystemPrompt + "\n\n" + text
				projection.SystemPromptLowered = true
			}
			entry := kiroTextUserInput{Content: text, ModelID: modelID}
			if index == len(ctx.Messages)-1 {
				projection.Current = entry
			} else {
				projection.History = append(projection.History, kiroTextHistoryEntry{User: &entry})
			}
		} else {
			assistant, ok := message.(AssistantMessage)
			if !ok || len(assistant.HostedToolExecutions) != 0 || assistant.ErrorMessage != "" || assistant.StopReason != StopReasonStop {
				return kiroTextProjection{}, errKiroTextProjection
			}
			text, valid := kiroProjectionText(assistant.Content, consume, &remainingBlocks)
			if !valid || strings.TrimSpace(text) == "" {
				return kiroTextProjection{}, errKiroTextProjection
			}
			projection.History = append(projection.History, kiroTextHistoryEntry{Assistant: &kiroTextAssistantResponse{Content: text}})
		}
	}
	return projection, nil
}

func kiroProjectionText(blocks []ContentBlock, consume func(string) bool, remainingBlocks *int) (string, bool) {
	if len(blocks) > *remainingBlocks {
		return "", false
	}
	*remainingBlocks -= len(blocks)
	var text strings.Builder
	for _, block := range blocks {
		if pointer, ok := block.(*TextContent); ok {
			if pointer == nil {
				return "", false
			}
			block = *pointer
		}
		part, ok := block.(TextContent)
		if !ok || part.TextSignature != "" || !consume(part.Text) {
			return "", false
		}
		text.WriteString(part.Text)
	}
	return text.String(), true
}
