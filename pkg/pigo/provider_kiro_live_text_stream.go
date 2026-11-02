package pigo

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var errKiroLiveTextStream = errors.New("kiro: invalid injected text stream")

type kiroInjectedTextStreamConfig struct {
	Request  kiroHTTPRequestOptions
	Response kiroHTTPResponseLimits
	// Model supplies identity only; no capability, pricing or header inference.
	Model         Model
	Timestamp     time.Time
	MaxTextBytes  int
	MaxEventBytes int
}

// startKiroInjectedTextStream connects an explicit single-attempt executor to
// actual host event delivery. It is NOT registered and never chooses a transport
// or credentials. Request serialization/validation completes before returning;
// callers may then edit their original conversation without racing the worker.
// Executor/body/Close must cooperate with context; blocked trusted code cannot
// be preempted. Consume Events or Close, and read Result once.
func startKiroInjectedTextStream(ctx context.Context, conversation Context, config kiroInjectedTextStreamConfig, kind kiroCredentialKind, token string, roundTrip func(*http.Request) (*http.Response, error)) (*AssistantMessageEventStream, error) {
	if ctx == nil || ctx.Err() != nil || roundTrip == nil || !kiroHTTPResponseLimitsValid(config.Response) || len(conversation.Tools) != 0 || config.Model.ID != config.Request.Identity.ModelID {
		return nil, errKiroLiveTextStream
	}
	mapper, err := newKiroIncrementalTextMapper(config.Model, config.Timestamp, config.MaxTextBytes, config.MaxEventBytes)
	if err != nil {
		return nil, errKiroLiveTextStream
	}
	request, err := newKiroOfflineRequestWithToken(ctx, conversation, nil, config.Request, kind, token)
	if err != nil {
		return nil, errKiroLiveTextStream
	}
	return managedAssistantStream(ctx, func(requestCtx context.Context) *AssistantMessageEventStream {
		stream := newAssistantMessageEventStream()
		ownedRequest := request.WithContext(requestCtx)
		go func() {
			pushBatch := func(events []AssistantMessageEvent) error {
				for _, event := range events {
					if requestCtx.Err() != nil {
						return errKiroLiveTextStream
					}
					stream.push(event)
				}
				return nil
			}
			response, attemptErr := executeKiroPreparedAttempt(ownedRequest, roundTrip, config.Response, func(text string) error {
				events, err := mapper.accept(text)
				if err != nil {
					return err
				}
				return pushBatch(events)
			})
			var final AssistantMessage
			if attemptErr == nil && requestCtx.Err() == nil {
				events, err := mapper.complete(response)
				if err == nil {
					// Completion evidence is committed before delivery; subsequent caller
					// cancellation may discard events but must not rewrite that evidence.
					final = events[len(events)-1].Message
					_ = pushBatch(events)
					stream.finish(final)
					return
				}
			}
			event, ok := mapper.fail(requestCtx.Err() != nil)
			if ok {
				stream.push(event)
				final = event.Error
			}
			stream.finish(final)
		}()
		return stream
	}), nil
}
