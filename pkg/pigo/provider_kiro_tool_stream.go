package pigo

import (
	"context"
	"errors"
	"time"
)

var errKiroToolStream = errors.New("kiro: unsupported completed tool stream")

// newKiroCompletedToolStream bridges validated buffered text/tools to the existing host
// delivery lifecycle. It performs no network operation and is not registered as
// a provider. Projection is synchronous; inputs must not be concurrently mutated.
// As with other host streams, consume Events or Close, and read Result only once.
// Completion is evidence, NOT execution authorization: the agent must recheck
// cancellation, tool schema and permissions at execution. Cancellation cannot
// revoke a ToolCall already delivered or a Result completed before cancellation.
func newKiroCompletedToolStream(ctx context.Context, response kiroOfflineResponse, model Model, timestamp time.Time, allowedNames []string, maxBytes, maxTools, maxEventBytes int) (*AssistantMessageEventStream, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errKiroToolStream
	}
	// Reserve every event even if no consumer runs until after this function returns.
	// 3 events per text/tool part + start/done; delta count is also below its own limit.
	maxParts := (assistantMessageEventPendingLimit - 2) / 3
	if len(response.Parts) > maxParts || len(response.Parts) > assistantMessageEventDeltaBuffer {
		return nil, errKiroToolStream
	}
	events, err := projectKiroCompletedToolEvents(ctx, response, model, timestamp, allowedNames, maxBytes, maxTools, maxEventBytes)
	if err != nil || ctx.Err() != nil {
		return nil, errKiroToolStream
	}
	// Re-account the actual detached events before touching a queue. This ensures
	// stream.push's accounting fits even if the projection's cloning changes later.
	failed := AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID, Timestamp: timestamp, StopReason: StopReasonAborted, ErrorMessage: "kiro: buffered tool delivery cancelled"}
	aborted := AssistantMessageEvent{Type: AssistantMessageEventError, Reason: StopReasonAborted, Error: failed}
	remaining := maxEventBytes - assistantEventBytes(aborted)
	for _, event := range events {
		cost := assistantEventBytes(event)
		if cost > remaining {
			return nil, errKiroToolStream
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
