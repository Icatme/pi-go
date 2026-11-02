package pigo

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var errKiroIncrementalEvents = errors.New("kiro: incremental text events rejected")

// Single-owner state machine, not a host queue, API registration or transport.
// Returned snapshots are detached. Preview events do not prove final success.
type kiroIncrementalTextMapper struct {
	identity       AssistantMessage
	maxTextBytes   int
	remainingBytes int
	fragments      int
	text           string
	failed         bool
	terminal       bool
}

func newKiroIncrementalTextMapper(model Model, stamp time.Time, maxTextBytes, maxEventBytes int) (*kiroIncrementalTextMapper, error) {
	if !kiroTextModelIdentityValid(model) || maxTextBytes < 1 || maxTextBytes > 1<<20 || maxEventBytes < 1 || maxEventBytes > assistantMessageEventByteBudget {
		return nil, errKiroIncrementalEvents
	}
	m := &kiroIncrementalTextMapper{identity: AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID, Timestamp: stamp}, maxTextBytes: maxTextBytes}
	reserve := assistantEventBytes(m.failureEvent(false))
	if cost := assistantEventBytes(m.failureEvent(true)); cost > reserve {
		reserve = cost
	}
	if reserve > maxEventBytes {
		return nil, errKiroIncrementalEvents
	}
	m.remainingBytes = maxEventBytes - reserve
	return m, nil
}

func (m *kiroIncrementalTextMapper) accept(text string) ([]AssistantMessageEvent, error) {
	if m == nil || m.failed || m.terminal {
		return nil, errKiroIncrementalEvents
	}
	if m.fragments >= assistantMessageEventDeltaBuffer || len(text) > m.maxTextBytes-len(m.text) || !utf8.ValidString(text) {
		return m.reject()
	}
	next := m.text + text
	var events []AssistantMessageEvent
	if m.fragments == 0 {
		events = append(events, AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: m.identity})
		partial := m.identity
		partial.Content = []ContentBlock{TextContent{}}
		events = append(events, AssistantMessageEvent{Type: AssistantMessageEventTextStart, ContentIndex: 0, Partial: partial})
	}
	partial := m.identity
	partial.Content = []ContentBlock{TextContent{Text: next}}
	events = append(events, AssistantMessageEvent{Type: AssistantMessageEventTextDelta, ContentIndex: 0, Delta: text, Partial: partial})
	committed, ok := m.commit(events)
	if !ok {
		return m.reject()
	}
	m.text = next
	m.fragments++
	return committed, nil
}

func (m *kiroIncrementalTextMapper) complete(response kiroOfflineResponse) ([]AssistantMessageEvent, error) {
	if m == nil || m.failed || m.terminal {
		return nil, errKiroIncrementalEvents
	}
	model := Model{API: m.identity.API, Provider: m.identity.Provider, ID: m.identity.Model}
	message, err := projectKiroCompletedText(response, model, m.identity.Timestamp, m.maxTextBytes)
	if err != nil || m.fragments == 0 {
		return m.reject()
	}
	var finalText strings.Builder
	for _, block := range message.Content {
		finalText.WriteString(block.(TextContent).Text)
	}
	if finalText.String() != m.text {
		return m.reject()
	}
	// Unlike the buffered projector, a continuous incremental text run is one
	// host block. Different wire fragment boundaries are allowed, not reordered text.
	message.Content = []ContentBlock{TextContent{Text: m.text}}
	partial := m.identity
	partial.Content = []ContentBlock{TextContent{Text: m.text}}
	events := []AssistantMessageEvent{
		{Type: AssistantMessageEventTextEnd, ContentIndex: 0, Content: m.text, Partial: partial},
		{Type: AssistantMessageEventDone, Reason: message.StopReason, Message: message},
	}
	committed, ok := m.commit(events)
	if !ok {
		return m.reject()
	}
	m.terminal = true
	m.text = ""
	return committed, nil
}

// fail emits at most one fixed error terminal, even after a rejected accept or
// complete. It may be called before any text. Once Done was returned it is inert.
func (m *kiroIncrementalTextMapper) fail(cancelled bool) (AssistantMessageEvent, bool) {
	if m == nil || m.terminal {
		return AssistantMessageEvent{}, false
	}
	m.terminal = true
	m.failed = true
	m.text = ""
	return cloneAssistantMessageEvent(m.failureEvent(cancelled)), true
}
func (m *kiroIncrementalTextMapper) failureEvent(cancelled bool) AssistantMessageEvent {
	message := m.identity
	message.StopReason = StopReasonError
	if cancelled {
		message.StopReason = StopReasonAborted
	}
	message.ErrorMessage = "kiro: incremental text not completed"
	return AssistantMessageEvent{Type: AssistantMessageEventError, Reason: message.StopReason, Error: message}
}
func (m *kiroIncrementalTextMapper) reject() ([]AssistantMessageEvent, error) {
	m.failed = true
	m.text = ""
	return nil, errKiroIncrementalEvents
}
func (m *kiroIncrementalTextMapper) commit(events []AssistantMessageEvent) ([]AssistantMessageEvent, bool) {
	remaining := m.remainingBytes
	for _, event := range events {
		cost := assistantEventBytes(event)
		if cost > remaining {
			return nil, false
		}
		remaining -= cost
	}
	owned := make([]AssistantMessageEvent, len(events))
	for i, event := range events {
		owned[i] = cloneAssistantMessageEvent(event)
	}
	m.remainingBytes = remaining
	return owned, true
}
