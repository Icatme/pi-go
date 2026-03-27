package pigo

import (
	"sync"
)

type AssistantMessageEventStream struct {
	events       chan AssistantMessageEvent
	result       chan AssistantMessage
	finalizeOnce sync.Once
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
	s.events <- cloneAssistantMessageEvent(event)
}

func (s *AssistantMessageEventStream) finish(result AssistantMessage) {
	s.finalizeOnce.Do(func() {
		s.result <- cloneAssistantMessage(result)
		close(s.result)
		close(s.events)
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
