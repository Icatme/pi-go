package pigo

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

var errKiroToolProjection = errors.New("kiro: unsupported tool conversation projection")

type kiroToolWireCall struct {
	Name  string          `json:"name"`
	ID    string          `json:"toolUseId"`
	Input json.RawMessage `json:"input"`
}
type kiroToolWireText struct {
	Text string `json:"text"`
}
type kiroToolWireResult struct {
	Content []kiroToolWireText `json:"content"`
	Status  string             `json:"status"`
	ID      string             `json:"toolUseId"`
}
type kiroToolWireSpec struct {
	Specification struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			JSON json.RawMessage `json:"json"`
		} `json:"inputSchema"`
	} `json:"toolSpecification"`
}
type kiroToolWireContext struct {
	Tools   []kiroToolWireSpec   `json:"tools,omitempty"`
	Results []kiroToolWireResult `json:"toolResults,omitempty"`
}
type kiroToolWireUser struct {
	Content string               `json:"content"`
	ModelID string               `json:"modelId"`
	Context *kiroToolWireContext `json:"userInputMessageContext,omitempty"`
}
type kiroToolWireAssistant struct {
	Content string             `json:"content"`
	Calls   []kiroToolWireCall `json:"toolUses,omitempty"`
}
type kiroToolWireEntry struct {
	User      *kiroToolWireUser      `json:"userInputMessage,omitempty"`
	Assistant *kiroToolWireAssistant `json:"assistantResponseMessage,omitempty"`
}
type kiroToolProjection struct {
	History []kiroToolWireEntry
	Current kiroToolWireUser
}

// projectKiroToolConversation builds only inert conversation DTOs, not a full
// sendable request. Origin/profile/auth/transport are intentionally absent.
// Schemas must be explicitly supplied as raw JSON; Tool.Parameters is never
// marshaled. RawArguments must be present; a nonnil Arguments map must pass
// bounded dual-evidence equality rather than silently overriding edits or
// precision loss. Original raw JSON is replayed and copied.
func projectKiroToolConversation(ctx Context, modelID string, schemas map[string]json.RawMessage, maxMessages, maxCalls, maxBytes int) (kiroToolProjection, error) {
	bad := kiroToolProjection{}
	if ctx.SystemPrompt != "" || modelID == "" || len(modelID) > 256 || !utf8.ValidString(modelID) || maxBytes < 2 || len(ctx.Messages) == 0 || len(ctx.Tools) == 0 || len(schemas) != len(ctx.Tools) {
		return bad, errKiroToolProjection
	}
	groups, err := inspectKiroToolHistoryGroups(ctx, maxMessages, maxCalls)
	if err != nil {
		return bad, errKiroToolProjection
	}
	byIndex := make(map[int]kiroToolHistoryGroup)
	for _, group := range groups {
		byIndex[group.AssistantIndex] = group
	}
	remaining := maxBytes
	consume := func(value string) bool {
		if len(value) > remaining || !utf8.ValidString(value) {
			return false
		}
		remaining -= len(value)
		return true
	}
	consumeRaw := func(value []byte) bool {
		if len(value) > remaining || !utf8.Valid(value) {
			return false
		}
		remaining -= len(value)
		return true
	}
	var specs []kiroToolWireSpec
	for _, tool := range ctx.Tools {
		raw, found := schemas[tool.Name]
		if !found || tool.OutputSchema != nil || tool.ConstrainedSampling != nil || !consume(tool.Name) || !consume(tool.Description) || !consumeRaw(raw) {
			return bad, errKiroToolProjection
		}
		if _, err := decodeKiroJSONObject(raw, maxBytes); err != nil {
			return bad, errKiroToolProjection
		}
		var spec kiroToolWireSpec
		spec.Specification.Name, spec.Specification.Description = tool.Name, tool.Description
		spec.Specification.InputSchema.JSON = append(json.RawMessage(nil), raw...)
		specs = append(specs, spec)
	}
	var entries []kiroToolWireEntry
	blocksLeft := 4096
	for i := 0; i < len(ctx.Messages); i++ {
		message := ctx.Messages[i]
		switch m := message.(type) {
		case *UserMessage:
			message = *m // nil pointers rejected by structural pass.
		case *AssistantMessage:
			message = *m
		}
		switch m := message.(type) {
		case UserMessage:
			var text string
			switch content := m.Content.(type) {
			case string:
				text = content
				if !consume(text) {
					return bad, errKiroToolProjection
				}
			case []ContentBlock:
				var valid bool
				text, valid = kiroProjectionText(content, consume, &blocksLeft)
				if !valid {
					return bad, errKiroToolProjection
				}
			default:
				return bad, errKiroToolProjection
			}
			if strings.TrimSpace(text) == "" {
				return bad, errKiroToolProjection
			}
			entries = append(entries, kiroToolWireEntry{User: &kiroToolWireUser{Content: text, ModelID: modelID}})
		case AssistantMessage:
			if m.ErrorMessage != "" || (m.StopReason != StopReasonStop && m.StopReason != StopReasonToolUse) || len(m.Content) > blocksLeft {
				return bad, errKiroToolProjection
			}
			blocksLeft -= len(m.Content)
			var assistant kiroToolWireAssistant
			var text strings.Builder
			for _, block := range m.Content {
				switch p := block.(type) {
				case *TextContent:
					if p == nil {
						return bad, errKiroToolProjection
					}
					block = *p
				case *ToolCall:
					if p == nil {
						return bad, errKiroToolProjection
					}
					block = *p
				}
				switch b := block.(type) {
				case TextContent:
					if len(assistant.Calls) != 0 || b.TextSignature != "" || !consume(b.Text) {
						return bad, errKiroToolProjection
					}
					text.WriteString(b.Text)
				case ToolCall:
					if b.RawArguments == "" || !consume(b.ID) || !consume(b.Name) || !consume(b.RawArguments) {
						return bad, errKiroToolProjection
					}
					if !kiroToolArgumentEvidenceMatches(b.RawArguments, b.Arguments, maxBytes) {
						return bad, errKiroToolProjection
					}
					assistant.Calls = append(assistant.Calls, kiroToolWireCall{Name: b.Name, ID: b.ID, Input: json.RawMessage(b.RawArguments)})
				default:
					return bad, errKiroToolProjection
				}
			}
			assistant.Content = text.String()
			if len(assistant.Calls) == 0 && strings.TrimSpace(assistant.Content) == "" {
				return bad, errKiroToolProjection
			}
			entries = append(entries, kiroToolWireEntry{Assistant: &assistant})
			if group, hasTools := byIndex[i]; hasTools {
				carrier := kiroToolWireUser{Content: "", ModelID: modelID, Context: &kiroToolWireContext{}}
				for _, index := range group.ResultIndices {
					result, ok := ctx.Messages[index].(ToolResultMessage)
					if !ok {
						result = *ctx.Messages[index].(*ToolResultMessage)
					}
					if result.Details != nil {
						return bad, errKiroToolProjection
					}
					if len(result.Content) > blocksLeft {
						return bad, errKiroToolProjection
					}
					blocksLeft -= len(result.Content)
					var content strings.Builder
					for _, block := range result.Content {
						if p, ok := block.(*TextContent); ok {
							if p == nil {
								return bad, errKiroToolProjection
							}
							block = *p
						}
						part, ok := block.(TextContent)
						if !ok || part.TextSignature != "" || !consume(part.Text) {
							return bad, errKiroToolProjection
						}
						content.WriteString(part.Text)
					}
					status := "success"
					if result.IsError {
						status = "error"
					}
					carrier.Context.Results = append(carrier.Context.Results, kiroToolWireResult{Content: []kiroToolWireText{{Text: content.String()}}, Status: status, ID: result.ToolCallID})
				}
				entries = append(entries, kiroToolWireEntry{User: &carrier})
				i += len(group.ResultIndices)
			}
		default:
			return bad, errKiroToolProjection
		}
	}
	// No merging adjacent user turns or synthesizing alternation. A complete
	// tool-result group naturally forms one user carrier with genuinely empty text.
	if len(entries)%2 == 0 {
		return bad, errKiroToolProjection
	}
	for i, entry := range entries {
		if (i%2 == 0 && entry.User == nil) || (i%2 == 1 && entry.Assistant == nil) {
			return bad, errKiroToolProjection
		}
	}
	last := entries[len(entries)-1].User
	if last.Context == nil {
		last.Context = &kiroToolWireContext{}
	}
	last.Context.Tools = specs
	return kiroToolProjection{History: entries[:len(entries)-1], Current: *last}, nil
}
