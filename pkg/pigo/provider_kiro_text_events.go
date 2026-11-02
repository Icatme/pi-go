package pigo

import (
	"errors"
	"time"
)

var errKiroTextEvents = errors.New("kiro: unsupported buffered text events")

// projectKiroCompletedTextEvents replays an already completed, validated response
// as host events. It is NOT incremental network streaming or a registered API.
// Inputs must not be mutated concurrently. Every returned snapshot is detached.
// maxEventBytes bounds conservative cumulative payload accounting, not heap/RSS;
// it is separate from the host stream queue's delivery and pending-event limits.
func projectKiroCompletedTextEvents(response kiroOfflineResponse, model Model, timestamp time.Time, maxTextBytes, maxEventBytes int) ([]AssistantMessageEvent, error) {
	if maxEventBytes < 1 || maxEventBytes > assistantMessageEventByteBudget {
		return nil, errKiroTextEvents
	}
	message, err := projectKiroCompletedText(response, model, timestamp, maxTextBytes)
	if err != nil {
		return nil, errKiroTextEvents
	}
	// The message projector bounds blocks to 4096, so at most 12290 events
	// can be produced. Snapshot amplification is additionally budgeted below.
	var events []AssistantMessageEvent
	remaining := maxEventBytes
	add := func(event AssistantMessageEvent) bool {
		cost := assistantEventBytes(event)
		if cost > remaining {
			return false
		}
		remaining -= cost
		events = append(events, cloneAssistantMessageEvent(event))
		return true
	}
	partial := AssistantMessage{API: message.API, Provider: message.Provider, Model: message.Model, Timestamp: message.Timestamp}
	if !add(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: partial}) {
		return nil, errKiroTextEvents
	}
	for index, block := range message.Content {
		text := block.(TextContent) // The completed-text projector admits only this value type.
		partial.Content = append(partial.Content, TextContent{})
		if !add(AssistantMessageEvent{Type: AssistantMessageEventTextStart, ContentIndex: index, Partial: partial}) {
			return nil, errKiroTextEvents
		}
		partial.Content[index] = text
		if !add(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, ContentIndex: index, Delta: text.Text, Partial: partial}) ||
			!add(AssistantMessageEvent{Type: AssistantMessageEventTextEnd, ContentIndex: index, Content: text.Text, Partial: partial}) {
			return nil, errKiroTextEvents
		}
	}
	// Final usage and completion evidence appear only on the terminal message.
	if !add(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: message.StopReason, Message: message}) {
		return nil, errKiroTextEvents
	}
	return events, nil
}
