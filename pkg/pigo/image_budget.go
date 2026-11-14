package pigo

import "fmt"

// ImageBudget is a host-selected policy, not a provider capability declaration.
// Its zero value disables budgeting. No provider applies it automatically.
type ImageBudget struct {
	MaxImages int
	// PruneBefore is an exclusive message index. Only tool-result images before
	// this boundary may be omitted. The host must protect current task evidence;
	// zero permits no omission. User and assistant images are always protected.
	PruneBefore int
}

// ImageBudgetExceededError means protected images alone exceed the chosen limit.
// No partial transformed history is returned and no request has been sent.
type ImageBudgetExceededError struct{ Limit, Protected int }

func (e *ImageBudgetExceededError) Error() string {
	return fmt.Sprintf("image budget exceeded: %d protected images exceed limit %d", e.Protected, e.Limit)
}

// ApplyImageBudget returns a model-facing history copy. When enabled it omits
// the oldest eligible tool images first, preserving every message, tool-call ID,
// result metadata and non-image block. It does not compact or modify stored
// history, infer a current-input boundary, retry requests, or limit image bytes.
// Apply after host content conversion and before sending the provider request.
// Enabled policies require canonical value messages and value content blocks;
// ambiguous user content (including JSON maps) is rejected, not undercounted.
// This is copy-on-write, not a deep copy: unchanged content and metadata remain
// shared and must be treated as immutable. Disabled policies copy only the slice.
func ApplyImageBudget(messages []Message, budget ImageBudget) ([]Message, error) {
	if budget.MaxImages < 0 {
		return nil, fmt.Errorf("image budget: MaxImages must not be negative")
	}
	if budget.MaxImages == 0 {
		return append([]Message(nil), messages...), nil
	}
	if budget.PruneBefore < 0 || budget.PruneBefore > len(messages) {
		return nil, fmt.Errorf("image budget: PruneBefore outside message range")
	}
	total, eligible := 0, 0
	for i, message := range messages {
		var blocks []ContentBlock
		switch m := message.(type) {
		case UserMessage:
			switch content := m.Content.(type) {
			case nil, string:
			case []ContentBlock:
				blocks = content
			default:
				return nil, fmt.Errorf("image budget: noncanonical user content at message %d", i)
			}
		case AssistantMessage:
			blocks = m.Content
		case ToolResultMessage:
			blocks = m.Content
		case SystemMessage:
		default:
			return nil, fmt.Errorf("image budget: noncanonical message at index %d", i)
		}
		for _, block := range blocks {
			switch block.(type) {
			case TextContent, ThinkingContent, ToolCall, ImageContent:
			default:
				return nil, fmt.Errorf("image budget: noncanonical content block at message %d", i)
			}
			if _, ok := block.(ImageContent); ok {
				total++
				if _, tool := message.(ToolResultMessage); tool && i < budget.PruneBefore {
					eligible++
				}
			}
		}
	}
	if total-eligible > budget.MaxImages {
		return nil, &ImageBudgetExceededError{Limit: budget.MaxImages, Protected: total - eligible}
	}
	out := append([]Message(nil), messages...)
	remove := total - budget.MaxImages
	for i := 0; i < budget.PruneBefore && remove > 0; i++ {
		m, ok := out[i].(ToolResultMessage)
		if !ok {
			continue
		}
		m.Content = append([]ContentBlock(nil), m.Content...)
		for j, block := range m.Content {
			if remove == 0 {
				break
			}
			if _, ok := block.(ImageContent); ok {
				m.Content[j] = TextContent{Text: "(tool image omitted by host image budget)"}
				remove--
			}
		}
		out[i] = m
	}
	return out, nil
}
