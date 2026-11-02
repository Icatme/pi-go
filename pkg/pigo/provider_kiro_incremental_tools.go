package pigo

import (
	"context"
	"strings"
)

// completeTools closes provisional text and emits tool lifecycle events only for
// an already completed response, after the caller has validated HTTP EOF/length/
// Close. It is pure: no queue/transport/tools. The entire final batch is budgeted
// and cloned before return; failure returns no tool events. A caller must check
// cancellation again before publishing, and the Agent still owns tool permission.
// Only text-before-tools is supported, matching the completed-tool projector.
func (m *kiroIncrementalTextMapper) completeTools(ctx context.Context, response kiroOfflineResponse, allowedNames []string, maxBytes, maxTools int) ([]AssistantMessageEvent, error) {
	if m == nil || m.failed || m.terminal {
		return nil, errKiroIncrementalEvents
	}
	if ctx == nil || ctx.Err() != nil {
		return m.reject()
	}
	model := Model{API: m.identity.API, Provider: m.identity.Provider, ID: m.identity.Model}
	message, err := projectKiroCompletedTools(response, model, m.identity.Timestamp, allowedNames, maxBytes, maxTools)
	if err != nil {
		return m.reject()
	}
	var text strings.Builder
	var calls []ToolCall
	textBlocks := 0
	for _, block := range message.Content {
		switch value := block.(type) {
		case TextContent:
			textBlocks++
			text.WriteString(value.Text)
		case ToolCall:
			calls = append(calls, value)
		default:
			return m.reject()
		}
	}
	// Reserve the combined text/tool delta count even when no consumer has
	// drained any previews. Do not rely on the queue dropping earlier text.
	if m.fragments+len(calls) > assistantMessageEventDeltaBuffer || text.String() != m.text || (textBlocks > 0) != (m.fragments > 0) {
		return m.reject()
	}
	var events []AssistantMessageEvent
	remaining := m.remainingBytes
	add := func(event AssistantMessageEvent) bool {
		cost := assistantEventBytes(event)
		if ctx.Err() != nil || cost > remaining {
			return false
		}
		remaining -= cost
		events = append(events, cloneAssistantMessageEvent(event))
		return true
	}
	partial := m.identity
	if m.fragments == 0 {
		if !add(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: partial}) {
			return m.reject()
		}
	} else {
		partial.Content = []ContentBlock{TextContent{Text: m.text}}
		if !add(AssistantMessageEvent{Type: AssistantMessageEventTextEnd, ContentIndex: 0, Content: m.text, Partial: partial}) {
			return m.reject()
		}
	}
	for _, call := range calls {
		if ctx.Err() != nil {
			return m.reject()
		}
		index := len(partial.Content)
		partial.Content = append(partial.Content, ToolCall{ID: call.ID, Name: call.Name})
		// Snapshot immediately; subsequent slice element assignments must not add
		// arguments to the earlier start event retained in the pending final batch.
		if !add(AssistantMessageEvent{Type: AssistantMessageEventToolCallStart, ContentIndex: index, Partial: partial}) {
			return m.reject()
		}
		partial.Content[index] = call
		if !add(AssistantMessageEvent{Type: AssistantMessageEventToolCallDelta, ContentIndex: index, Delta: call.RawArguments, Partial: partial}) || !add(AssistantMessageEvent{Type: AssistantMessageEventToolCallEnd, ContentIndex: index, ToolCall: call, Partial: partial}) {
			return m.reject()
		}
	}
	// Preserve final usage/diagnostics but share the one live text block layout.
	message.Content = partial.Content
	if !add(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: message.StopReason, Message: message}) {
		return m.reject()
	}
	if ctx.Err() != nil {
		return m.reject()
	}
	m.remainingBytes = remaining
	m.terminal = true
	m.text = ""
	return events, nil
}
