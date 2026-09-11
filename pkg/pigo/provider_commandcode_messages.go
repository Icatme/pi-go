package pigo

import (
	"fmt"
	"strings"
)

func commandCodeReasoningEffort(model Model, level ThinkingLevel) string {
	if !model.Reasoning || level == "" || level == "off" {
		return ""
	}
	return model.ThinkingLevelMap[ModelThinkingLevel(level)]
}

func commandCodeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func commandCodeImage(image ImageContent) (map[string]any, error) {
	if strings.TrimSpace(image.Data) == "" || strings.TrimSpace(image.MIMEType) == "" {
		return nil, fmt.Errorf("invalid Command Code image: expected base64 data and MIME type")
	}
	return map[string]any{"type": "image", "image": "data:" + image.MIMEType + ";base64," + image.Data}, nil
}

// Direct image prompts fail for text models. Historical tool images may be
// omitted when the caller switches models, while their text remains in history.
func commandCodeImageContext(model Model, ctx Context) (Context, error) {
	allowImages := false
	for _, input := range model.Input {
		allowImages = allowImages || input == InputImage
	}
	out := ctx
	out.Messages = make([]Message, 0, len(ctx.Messages))
	for _, message := range ctx.Messages {
		switch m := message.(type) {
		case UserMessage:
			if blocks, ok := m.Content.([]ContentBlock); ok {
				for _, block := range blocks {
					if image, ok := block.(ImageContent); ok {
						if !allowImages {
							return Context{}, fmt.Errorf("selected Command Code model does not support image content in user messages")
						}
						if _, err := commandCodeImage(image); err != nil {
							return Context{}, err
						}
					}
				}
			}
		case ToolResultMessage:
			m.Content = cloneBlocks(m.Content)
			blocks := make([]ContentBlock, 0, len(m.Content))
			omitted := false
			for _, block := range m.Content {
				if image, ok := block.(ImageContent); ok {
					if !allowImages {
						omitted = true
						continue
					}
					if _, err := commandCodeImage(image); err != nil {
						return Context{}, err
					}
				}
				blocks = append(blocks, block)
			}
			if omitted && len(blocks) == 0 {
				blocks = append(blocks, TextContent{Text: "[Image omitted: model does not support images]"})
			}
			m.Content = blocks
			message = m
		}
		out.Messages = append(out.Messages, message)
	}
	return out, nil
}

func commandCodeMessages(model Model, messages []Message) ([]any, error) {
	ctx, err := commandCodeImageContext(model, Context{Messages: messages})
	if err != nil {
		return nil, err
	}
	calls, results := map[string]bool{}, map[string]bool{}
	for _, message := range ctx.Messages {
		switch m := message.(type) {
		case AssistantMessage:
			for _, block := range m.Content {
				if call, ok := block.(ToolCall); ok && call.ID != "" {
					calls[call.ID] = true
				}
			}
		case ToolResultMessage:
			if m.ToolCallID != "" {
				results[m.ToolCallID] = true
			}
		}
	}
	out := make([]any, 0, len(ctx.Messages))
	for _, message := range ctx.Messages {
		switch m := message.(type) {
		case UserMessage:
			content := cloneAny(m.Content)
			if blocks, ok := m.Content.([]ContentBlock); ok {
				parts := make([]any, 0, len(blocks))
				for _, block := range blocks {
					switch b := block.(type) {
					case TextContent:
						parts = append(parts, map[string]any{"type": "text", "text": b.Text})
					case ImageContent:
						image, err := commandCodeImage(b)
						if err != nil {
							return nil, err
						}
						parts = append(parts, image)
					}
				}
				content = parts
			}
			out = append(out, map[string]any{"role": "user", "content": content})
		case AssistantMessage:
			parts, missing := []any{}, []any{}
			for _, block := range m.Content {
				switch b := block.(type) {
				case TextContent:
					parts = append(parts, map[string]any{"type": "text", "text": b.Text})
				case ToolCall:
					if b.ID == "" {
						continue
					}
					arguments := cloneMap(b.Arguments)
					if arguments == nil {
						arguments = map[string]any{}
					}
					parts = append(parts, map[string]any{"type": "tool-call", "toolCallId": b.ID, "toolName": b.Name, "input": arguments})
					if !results[b.ID] {
						missing = append(missing, commandCodeToolResult(b.ID, b.Name, "No result — the tool call did not complete (interrupted or lost).", true))
					}
				}
			}
			if len(parts) > 0 {
				out = append(out, map[string]any{"role": "assistant", "content": parts})
			}
			if len(missing) > 0 {
				out = append(out, map[string]any{"role": "tool", "content": missing})
			}
		case ToolResultMessage:
			if m.ToolCallID == "" || !calls[m.ToolCallID] {
				continue
			}
			out = append(out, map[string]any{"role": "tool", "content": []any{commandCodeToolResult(m.ToolCallID, m.ToolName, commandCodeTextContent(m.Content), m.IsError)}})
			images := []any{}
			for _, block := range m.Content {
				if b, ok := block.(ImageContent); ok {
					image, err := commandCodeImage(b)
					if err != nil {
						return nil, err
					}
					images = append(images, image)
				}
			}
			if len(images) > 0 {
				out = append(out, map[string]any{"role": "user", "content": images})
			}
		}
	}
	return out, nil
}

func commandCodeToolResult(id, name, value string, isError bool) map[string]any {
	kind := "text"
	if isError {
		kind = "error-text"
	}
	return map[string]any{"type": "tool-result", "toolCallId": id, "toolName": name, "output": map[string]any{"type": kind, "value": value}}
}
