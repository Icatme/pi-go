package pigo

import (
	"errors"
	"unicode/utf8"
)

var errKiroHistoryGroups = errors.New("kiro: unsupported or ambiguous tool history group")

// Index-only projection: no arguments/results are serialized or executed. This
// validates adjacency/identity only, not tool schemas, authorization or content.
type kiroToolHistoryGroup struct {
	AssistantIndex int
	ResultIndices  []int
}

// inspectKiroToolHistoryGroups identifies strictly adjacent, ordered groups in
// the original transcript. It never relocates results, synthesizes partners,
// rewrites IDs, fabricates schemas or invokes a custom validator/marshaler.
func inspectKiroToolHistoryGroups(ctx Context, maxMessages, maxCalls int) ([]kiroToolHistoryGroup, error) {
	if maxMessages < 1 || maxCalls < 1 || len(ctx.Messages) > maxMessages || len(ctx.Tools) > 256 || len(ctx.HostedTools) != 0 {
		return nil, errKiroHistoryGroups
	}
	catalog := make(map[string]struct{})
	for _, tool := range ctx.Tools {
		if tool.Name == "" || len(tool.Name) > 256 || !utf8.ValidString(tool.Name) {
			return nil, errKiroHistoryGroups
		}
		if _, duplicate := catalog[tool.Name]; duplicate {
			return nil, errKiroHistoryGroups
		}
		catalog[tool.Name] = struct{}{}
	}
	seen := make(map[string]struct{})
	var groups []kiroToolHistoryGroup
	blocksLeft := 4096
	for i := 0; i < len(ctx.Messages); i++ {
		message := ctx.Messages[i]
		switch m := message.(type) {
		case *UserMessage:
			if m == nil {
				return nil, errKiroHistoryGroups
			}
			message = *m
		case *AssistantMessage:
			if m == nil {
				return nil, errKiroHistoryGroups
			}
			message = *m
		}
		if _, user := message.(UserMessage); user {
			continue
		}
		assistant, ok := message.(AssistantMessage)
		if !ok || len(assistant.HostedToolExecutions) != 0 || len(assistant.Content) > blocksLeft {
			// Includes orphan results and dynamic system/tool catalog messages.
			return nil, errKiroHistoryGroups
		}
		blocksLeft -= len(assistant.Content)
		var calls []ToolCall
		for _, block := range assistant.Content {
			if pointer, ok := block.(*ToolCall); ok {
				if pointer == nil {
					return nil, errKiroHistoryGroups
				}
				block = *pointer
			}
			call, ok := block.(ToolCall)
			if !ok {
				continue // Non-tool content is outside this structural validator.
			}
			if !kiroNativeHistoryToolID(call.ID) || call.Namespace != "" || call.ThoughtSignature != "" || len(seen) >= maxCalls {
				return nil, errKiroHistoryGroups
			}
			if _, known := catalog[call.Name]; !known {
				return nil, errKiroHistoryGroups
			}
			if _, duplicate := seen[call.ID]; duplicate {
				return nil, errKiroHistoryGroups
			}
			seen[call.ID] = struct{}{}
			calls = append(calls, call)
		}
		if len(calls) == 0 {
			if assistant.StopReason == StopReasonToolUse {
				return nil, errKiroHistoryGroups
			}
			continue
		}
		if assistant.StopReason != StopReasonToolUse || assistant.ErrorMessage != "" || len(calls) > len(ctx.Messages)-i-1 {
			return nil, errKiroHistoryGroups
		}
		group := kiroToolHistoryGroup{AssistantIndex: i}
		for offset, call := range calls {
			index := i + offset + 1
			message := ctx.Messages[index]
			if pointer, ok := message.(*ToolResultMessage); ok {
				if pointer == nil {
					return nil, errKiroHistoryGroups
				}
				message = *pointer
			}
			result, ok := message.(ToolResultMessage)
			if !ok || result.ToolCallID != call.ID || result.ToolName != call.Name {
				return nil, errKiroHistoryGroups
			}
			group.ResultIndices = append(group.ResultIndices, index)
		}
		groups = append(groups, group)
		i += len(calls)
	}
	return groups, nil
}

// The community mapper hashes non-native IDs. This strict subset rejects them
// instead; a future explicit replay mapping must prove call/result consistency.
func kiroNativeHistoryToolID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range []byte(id) {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}
