package pigo

import (
	"context"
	"errors"
	"time"
)

var errKiroToolEvents = errors.New("kiro: unsupported buffered tool events")

// projectKiroCompletedToolEvents replays an already completed, validated response
// as host text/tool lifecycle events. Cancellation or any projection/budget error
// returns nil events; no callbacks or queue pushes occur inside this function.
// The caller must recheck cancellation before publishing returned events.
// This is NOT incremental network streaming or a registered API.
// Inputs must not be mutated concurrently. Every returned snapshot is detached.
// maxEventBytes bounds conservative cumulative payload accounting, not heap/RSS;
// it is separate from the host stream queue's delivery and pending-event limits.
func projectKiroCompletedToolEvents(ctx context.Context, response kiroOfflineResponse, model Model, timestamp time.Time, allowedNames []string, maxBytes, maxTools, maxEventBytes int) ([]AssistantMessageEvent, error) {
	if ctx == nil || ctx.Err() != nil || maxEventBytes < 1 || maxEventBytes > assistantMessageEventByteBudget {
		return nil, errKiroToolEvents
	}
	message, err := projectKiroCompletedTools(response, model, timestamp, allowedNames, maxBytes, maxTools)
	if err != nil {
		return nil, errKiroToolEvents
	}
	// The message projector bounds blocks to 4096, so at most 12290 events
	// can be produced. Snapshot amplification is additionally budgeted below.
	var events []AssistantMessageEvent
	remaining := maxEventBytes
	add := func(event AssistantMessageEvent) bool {
		cost := assistantEventBytes(event)
		if ctx.Err() != nil || cost > remaining {
			return false
		}
		remaining -= cost
		events = append(events, cloneAssistantMessageEvent(event))
		return true
	}
	partial := AssistantMessage{API: message.API, Provider: message.Provider, Model: message.Model, Timestamp: message.Timestamp}
	if !add(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: partial}) {
		return nil, errKiroToolEvents
	}
	for index, block := range message.Content {

		switch value := block.(type) {
		case TextContent:
			partial.Content = append(partial.Content, TextContent{})
			if !add(AssistantMessageEvent{Type: AssistantMessageEventTextStart, ContentIndex: index, Partial: partial}) {
				return nil, errKiroToolEvents
			}
			partial.Content[index] = value
			if !add(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, ContentIndex: index, Delta: value.Text, Partial: partial}) ||
				!add(AssistantMessageEvent{Type: AssistantMessageEventTextEnd, ContentIndex: index, Content: value.Text, Partial: partial}) {
				return nil, errKiroToolEvents
			}
		case ToolCall:
			partial.Content = append(partial.Content, ToolCall{ID: value.ID, Name: value.Name})
			if !add(AssistantMessageEvent{Type: AssistantMessageEventToolCallStart, ContentIndex: index, Partial: partial}) {
				return nil, errKiroToolEvents
			}
			partial.Content[index] = value
			if !add(AssistantMessageEvent{Type: AssistantMessageEventToolCallDelta, ContentIndex: index, Delta: value.RawArguments, Partial: partial}) ||
				!add(AssistantMessageEvent{Type: AssistantMessageEventToolCallEnd, ContentIndex: index, ToolCall: value, Partial: partial}) {
				return nil, errKiroToolEvents
			}
		default:
			return nil, errKiroToolEvents
		}
	}
	// Final usage and completion evidence appear only on the terminal message.
	if !add(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: message.StopReason, Message: message}) {
		return nil, errKiroToolEvents
	}
	if ctx.Err() != nil {
		return nil, errKiroToolEvents
	}
	return events, nil
}
