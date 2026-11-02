package pigo

import (
	"context"
	"errors"
	"time"
)

var errKiroTextStream = errors.New("kiro: unsupported completed text stream")

// newKiroCompletedTextStream bridges validated buffered text to the existing host
// delivery lifecycle. It performs no network operation and is not registered as
// a provider. Projection is synchronous; inputs must not be concurrently mutated.
// As with other host streams, consume Events or Close, and read Result only once.
func newKiroCompletedTextStream(ctx context.Context, response kiroOfflineResponse, model Model, timestamp time.Time, maxTextBytes, maxEventBytes int) (*AssistantMessageEventStream, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errKiroTextStream
	}
	// Reserve every event even if no consumer runs until after this function returns.
	// 3 events per text part + start/done; delta count is also below its own limit.
	maxParts := (assistantMessageEventPendingLimit - 2) / 3
	if len(response.Parts) > maxParts || len(response.Parts) > assistantMessageEventDeltaBuffer {
		return nil, errKiroTextStream
	}
	events, err := projectKiroCompletedTextEvents(response, model, timestamp, maxTextBytes, maxEventBytes)
	if err != nil || ctx.Err() != nil {
		return nil, errKiroTextStream
	}
	// Re-account the actual detached events before touching a queue. This ensures
	// stream.push's accounting fits even if the projection's cloning changes later.
	failed := AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID, Timestamp: timestamp, StopReason: StopReasonAborted, ErrorMessage: "kiro: buffered text delivery cancelled"}
	aborted := AssistantMessageEvent{Type: AssistantMessageEventError, Reason: StopReasonAborted, Error: failed}
	remaining := maxEventBytes - assistantEventBytes(aborted)
	for _, event := range events {
		cost := assistantEventBytes(event)
		if cost > remaining {
			return nil, errKiroTextStream
		}
		remaining -= cost
	}
	stream := managedAssistantStream(ctx, func(requestCtx context.Context) *AssistantMessageEventStream {
		s := newAssistantMessageEventStream()
		for _, event := range events {
			if requestCtx.Err() != nil {
				s.push(aborted)
				s.finish(failed)
				return s
			}
			s.push(event)
		}
		s.finish(events[len(events)-1].Message)
		return s
	})
	return stream, nil
}
