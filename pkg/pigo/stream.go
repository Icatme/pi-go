package pigo

import (
	"sync"
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
	queued := queuedAssistantMessageEvent{
		event:     cloneAssistantMessageEvent(event),
		droppable: isDroppableAssistantMessageEvent(event.Type),
	}

	s.queueMu.Lock()
	defer s.queueMu.Unlock()

	if s.closing {
		return
	}
	if queued.droppable && s.pendingDelta >= assistantMessageEventDeltaBuffer {
		s.dropOldestPendingDeltaLocked()
	}
	if queued.droppable {
		s.pendingDelta++
	}
	s.pending = append(s.pending, queued)
	s.queueCond.Signal()
}

func (s *AssistantMessageEventStream) finish(result AssistantMessage) {
	s.finalizeOnce.Do(func() {
		s.result <- cloneAssistantMessage(result)
		close(s.result)
		s.queueMu.Lock()
		s.closing = true
		s.queueCond.Broadcast()
		s.queueMu.Unlock()
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

func isDroppableAssistantMessageEvent(eventType AssistantMessageEventType) bool {
	switch eventType {
	case AssistantMessageEventTextDelta, AssistantMessageEventThinkingDelta, AssistantMessageEventToolCallDelta:
		return true
	default:
		return false
	}
}
