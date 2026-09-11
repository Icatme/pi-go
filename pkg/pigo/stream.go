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
	bytes     int
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
	pendingBytes int
	bufferErr    error

	deliveryDone   chan struct{}
	dispatcherDone chan struct{}
	deliveryClosed bool
	cancelRequest  context.CancelFunc
	stopContext    func() bool

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
		// Keep the public channel small; the accounted pending queue owns buffering.
		events:         make(chan AssistantMessageEvent, 1),
		result:         make(chan AssistantMessage, 1),
		deliveryDone:   make(chan struct{}),
		dispatcherDone: make(chan struct{}),
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
	options.HTTPClient = providerHTTPClient(options.HTTPClient)
	if apiModule != nil && apiModule.Stream != nil {
		return managedAssistantStream(options.RequestContext, func(requestCtx context.Context) *AssistantMessageEventStream {
			options.RequestContext = requestCtx
			return apiModule.Stream(model, ctx, options)
		})
	}
	return streamAPIUnavailable(model, "api not implemented")
}

func StreamSimple(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
	options.Headers = mergeRequestHeaders(model.Headers, options.Headers)
	if err := ValidateResponseFormat(model, options.ResponseFormat); err != nil {
		return streamAPIUnavailable(model, err.Error())
	}
	options.HTTPClient = providerHTTPClient(options.HTTPClient)
	apiModule := resolveAPIModule(model.API)
	if apiModule != nil && apiModule.StreamSimple != nil {
		return managedAssistantStream(options.RequestContext, func(requestCtx context.Context) *AssistantMessageEventStream {
			options.RequestContext = requestCtx
			return apiModule.StreamSimple(model, ctx, options)
		})
	}
	return streamAPIUnavailable(model, "api not implemented")
}

func Complete(model Model, ctx Context, options ProviderStreamOptions) AssistantMessage {
	stream := Stream(model, ctx, options)
	for range stream.Events() {
	}
	return stream.Result()
}

func CompleteSimple(model Model, ctx Context, options SimpleStreamOptions) AssistantMessage {
	stream := StreamSimple(model, ctx, options)
	for range stream.Events() {
	}
	return stream.Result()
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

// Events exposes the event channel. Consumers that stop early must Close the
// stream or cancel the RequestContext supplied to Stream/StreamSimple.
func (s *AssistantMessageEventStream) Events() <-chan AssistantMessageEvent {
	return s.events
}

// Result does not consume events. Events remain readable afterwards unless the
// stream was closed or its caller context was cancelled.
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
	s.queueMu.Lock()
	if s.closing {
		s.queueMu.Unlock()
		return
	}
	s.eventCount++
	var cancel context.CancelFunc
	if !s.deliveryClosed && s.bufferErr == nil {
		cancel = s.enqueueLocked(event)
	}
	s.queueMu.Unlock()
	if cancel != nil {
		cancel()
	}

	// Delivery cancellation must not suppress the provider's observer accounting.
	if s.observer != nil {
		s.observer.OnStreamEvent(s.requestCtx, s.model, cloneAssistantMessageEvent(event))
	}
}

func (s *AssistantMessageEventStream) finish(result AssistantMessage) {
	s.finalizeOnce.Do(func() {
		s.queueMu.Lock()
		if s.bufferErr != nil {
			// Keep final provider content, response ID and usage, but never report
			// success after a lifecycle event was lost to a hard buffer limit.
			result.StopReason = StopReasonError
			result.ErrorMessage = s.bufferErr.Error()
		}
		cloned := cloneAssistantMessage(result)
		s.result <- cloned
		close(s.result)
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
	defer close(s.dispatcherDone)
	defer close(s.events)
	defer s.releaseDelivery()
	for {
		s.queueMu.Lock()
		for len(s.pending) == 0 && !s.closing && !s.deliveryClosed {
			s.queueCond.Wait()
		}
		if s.deliveryClosed || len(s.pending) == 0 && s.closing {
			s.queueMu.Unlock()
			return
		}

		queued := s.pending[0]
		s.pending[0] = queuedAssistantMessageEvent{}
		s.pending = s.pending[1:]
		if len(s.pending) == 0 {
			s.pending = nil
		}
		s.pendingBytes -= queued.bytes
		if queued.droppable {
			s.pendingDelta--
		}
		if s.droppedDelta > 0 {
			queued.event.DroppedEvents += s.droppedDelta
			s.droppedDelta = 0
		}
		s.queueMu.Unlock()

		select {
		case s.events <- queued.event:
		case <-s.deliveryDone:
			return
		}
	}
}

func (s *AssistantMessageEventStream) dropOldestPendingDeltaLocked() bool {
	for index, queued := range s.pending {
		if !queued.droppable {
			continue
		}
		copy(s.pending[index:], s.pending[index+1:])
		s.pending[len(s.pending)-1] = queuedAssistantMessageEvent{}
		s.pending = s.pending[:len(s.pending)-1]
		s.pendingBytes -= queued.bytes
		s.pendingDelta--
		s.droppedDelta++
		return true
	}
	return false
}
