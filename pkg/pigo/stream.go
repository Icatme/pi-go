package pigo

import (
	"context"
	"errors"
	"sync"
	"time"
)

const assistantMessageEventDeltaBuffer = 1024

type queuedAssistantMessageEvent struct {
	event     AssistantMessageEvent
	droppable bool
}

type AssistantMessageEventStream struct {
	events       chan AssistantMessageEvent
	result       chan AssistantMessage
	finalizeOnce sync.Once

	queueMu      sync.Mutex
	queueCond    *sync.Cond
	pending      []queuedAssistantMessageEvent
	pendingDelta int
	droppedDelta int
	closing      bool

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

func (s *AssistantMessageEventStream) startRequest(ctx context.Context, payload any) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.observer != nil {
		if observed := s.observer.OnRequestStart(ctx, s.model, payload); observed != nil {
			ctx = observed
		}
	}
	s.requestCtx = ctx
	return ctx
}

func newAssistantMessageEventStream() *AssistantMessageEventStream {
	stream := &AssistantMessageEventStream{
		events: make(chan AssistantMessageEvent, 1024),
		result: make(chan AssistantMessage, 1),
	}
	stream.queueCond = sync.NewCond(&stream.queueMu)
	go stream.dispatchEvents()
	return stream
}

func Stream(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
	options.Headers = mergeRequestHeaders(model.Headers, options.Headers)
	if err := ValidateResponseFormat(model, options.ResponseFormat); err != nil {
		return streamAPIUnavailable(model, err.Error())
	}
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
	options.Headers = mergeRequestHeaders(model.Headers, options.Headers)
	if err := ValidateResponseFormat(model, options.ResponseFormat); err != nil {
		return streamAPIUnavailable(model, err.Error())
	}
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
	queued := queuedAssistantMessageEvent{
		event:     cloneAssistantMessageEvent(event),
		droppable: isDroppableAssistantMessageEvent(event.Type),
	}

	s.queueMu.Lock()
	if s.closing {
		s.queueMu.Unlock()
		return
	}
	if queued.droppable && s.pendingDelta >= assistantMessageEventDeltaBuffer {
		s.dropOldestPendingDeltaLocked()
	}
	if queued.droppable {
		s.pendingDelta++
	}
	s.pending = append(s.pending, queued)
	s.eventCount++
	s.queueCond.Signal()
	s.queueMu.Unlock()

	if s.observer != nil {
		s.observer.OnStreamEvent(s.requestCtx, s.model, cloneAssistantMessageEvent(queued.event))
	}
}

func (s *AssistantMessageEventStream) finish(result AssistantMessage) {
	s.finalizeOnce.Do(func() {
		cloned := cloneAssistantMessage(result)
		s.result <- cloned
		close(s.result)

		s.queueMu.Lock()
		s.closing = true
		s.queueCond.Broadcast()
		dropped := s.droppedDelta
		eventCount := int(s.eventCount)
		s.queueMu.Unlock()

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

func (s *AssistantMessageEventStream) dispatchEvents() {
	for {
		s.queueMu.Lock()
		for len(s.pending) == 0 && !s.closing {
			s.queueCond.Wait()
		}
		if len(s.pending) == 0 && s.closing {
			close(s.events)
			s.queueMu.Unlock()
			return
		}

		queued := s.pending[0]
		s.pending = s.pending[1:]
		if queued.droppable {
			s.pendingDelta--
		}
		if s.droppedDelta > 0 {
			queued.event.DroppedEvents += s.droppedDelta
			s.droppedDelta = 0
		}
		s.queueMu.Unlock()

		s.events <- queued.event
	}
}

func (s *AssistantMessageEventStream) dropOldestPendingDeltaLocked() {
	for index, queued := range s.pending {
		if !queued.droppable {
			continue
		}
		s.pending = append(s.pending[:index], s.pending[index+1:]...)
		s.pendingDelta--
		s.droppedDelta++
		return
	}
}
