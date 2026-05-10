package pigo

import (
	"context"
	"errors"
	"sync"
	"time"
)

const assistantMessageEventDeltaBuffer = 1024
const assistantMessageEventReservedNonDroppableSlots = 32

type AssistantMessageEventStream struct {
	events       chan AssistantMessageEvent
	result       chan AssistantMessage
	finalizeOnce sync.Once

	mu      sync.Mutex
	closed  bool
	dropped int

	observer   Observer
	model      Model
	startTime  time.Time
	requestCtx context.Context
	eventCount int64
}

func (s *AssistantMessageEventStream) setObserver(obs Observer, m Model) {
	s.observer = obs
	s.model = m
	s.startTime = time.Now()
	s.requestCtx = context.Background()
}

func newAssistantMessageEventStream() *AssistantMessageEventStream {
	return &AssistantMessageEventStream{
		events: make(chan AssistantMessageEvent, 1024),
		result: make(chan AssistantMessage, 1),
	}
}

func Stream(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
	apiModule := resolveAPIModule(model.API)
	if providerModule := resolveProviderModule(model.Provider); providerModule != nil && providerModule.NormalizeOptions != nil {
		options = providerModule.NormalizeOptions(model, options)
	}
	if apiModule != nil && apiModule.Stream != nil {
		return apiModule.Stream(model, ctx, options)
	}
	return streamAPIUnavailable(model, "api not implemented")
}

func StreamSimple(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
	apiModule := resolveAPIModule(model.API)
	if apiModule != nil && apiModule.StreamSimple != nil {
		return apiModule.StreamSimple(model, ctx, options)
	}
	return streamAPIUnavailable(model, "api not implemented")
}

func Complete(model Model, ctx Context, options ProviderStreamOptions) AssistantMessage {
	return Stream(model, ctx, options).Result()
}

func CompleteSimple(model Model, ctx Context, options SimpleStreamOptions) AssistantMessage {
	return StreamSimple(model, ctx, options).Result()
}

func streamAPIUnavailable(model Model, message string) *AssistantMessageEventStream {
	stream := newAssistantMessageEventStream()
	response := AssistantMessage{
		API:          model.API,
		Provider:     model.Provider,
		Model:        model.ID,
		StopReason:   StopReasonError,
		ErrorMessage: message,
	}
	stream.push(AssistantMessageEvent{
		Type:   AssistantMessageEventError,
		Reason: response.StopReason,
		Error:  response,
	})
	stream.finish(response)
	return stream
}

func (s *AssistantMessageEventStream) Events() <-chan AssistantMessageEvent {
	return s.events
}

func (s *AssistantMessageEventStream) Result() AssistantMessage {
	result, ok := <-s.result
	if !ok {
		return AssistantMessage{
			StopReason:   StopReasonError,
			ErrorMessage: "stream result unavailable",
		}
	}
	return result
}

func (s *AssistantMessageEventStream) push(event AssistantMessageEvent) {
	cloned := cloneAssistantMessageEvent(event)
	droppable := isDroppableAssistantMessageEvent(event.Type)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}
	if droppable && len(s.events) >= cap(s.events)-assistantMessageEventReservedNonDroppableSlots {
		s.dropped++
		return
	}
	if s.dropped > 0 {
		cloned.DroppedEvents += s.dropped
		s.dropped = 0
	}
	if droppable {
		select {
		case s.events <- cloned:
			s.eventCount++
		default:
			s.dropped++
		}
		return
	}

	s.events <- cloned
	s.eventCount++
}

func (s *AssistantMessageEventStream) finish(result AssistantMessage) {
	s.finalizeOnce.Do(func() {
		cloned := cloneAssistantMessage(result)
		s.result <- cloned
		close(s.result)

		s.mu.Lock()
		s.closed = true
		dropped := s.dropped
		eventCount := int(s.eventCount)
		close(s.events)
		s.mu.Unlock()

		if s.observer != nil {
			duration := time.Since(s.startTime)
			if result.StopReason == StopReasonError || result.StopReason == StopReasonAborted {
				s.observer.OnRequestError(s.requestCtx, s.model, errors.New(result.ErrorMessage), duration)
			} else {
				s.observer.OnRequestComplete(s.requestCtx, s.model, cloned, duration)
			}
			s.observer.OnStreamFinish(s.requestCtx, s.model, cloned, eventCount, dropped, duration)
		}
	})
}

func cloneAssistantMessage(message AssistantMessage) AssistantMessage {
	cloned, ok := message.clone().(AssistantMessage)
	if !ok {
		return AssistantMessage{}
	}
	return cloned
}

func cloneAssistantMessageEvent(event AssistantMessageEvent) AssistantMessageEvent {
	cloned := event
	cloned.Partial = cloneAssistantMessage(event.Partial)
	cloned.Message = cloneAssistantMessage(event.Message)
	cloned.Error = cloneAssistantMessage(event.Error)
	cloned.ToolCall = ToolCall{
		ID:               event.ToolCall.ID,
		Name:             event.ToolCall.Name,
		Arguments:        cloneMap(event.ToolCall.Arguments),
		ThoughtSignature: event.ToolCall.ThoughtSignature,
	}
	return cloned
}

func isDroppableAssistantMessageEvent(eventType AssistantMessageEventType) bool {
	switch eventType {
	case AssistantMessageEventTextDelta, AssistantMessageEventThinkingDelta, AssistantMessageEventToolCallDelta:
		return true
	default:
		return false
	}
}
