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
	mapper, err := prepareKiroTextAttempt(ctx, conversation, config, roundTrip)
	if err != nil {
		return nil, err
	}
	request, err := newKiroOfflineRequestWithToken(ctx, conversation, nil, config.Request, kind, token)
	if err != nil {
		return nil, errKiroLiveTextStream
	}
	return startKiroPreparedTextStream(ctx, request, mapper, config, roundTrip), nil
}

// startKiroScopedTextStream obtains one explicit scoped credential after request
// serialization, then starts the existing text-only incremental worker. No tools,
// retries, credential cache/refresh or transport selection are added. A returned
// stream may expose provisional text; only final validated evidence is completion.
func startKiroScopedTextStream(ctx context.Context, conversation Context, config kiroInjectedTextStreamConfig, source kiroCredentialSource, roundTrip func(*http.Request) (*http.Response, error)) (*AssistantMessageEventStream, error) {
	if source == nil {
		return nil, errKiroLiveTextStream
	}
	mapper, err := prepareKiroTextAttempt(ctx, conversation, config, roundTrip)
	if err != nil {
		return nil, err
	}
	request, err := newKiroOfflineRequestWithCredentialSource(ctx, conversation, nil, config.Request, source)
	if err != nil {
		return nil, errKiroLiveTextStream
	}
	return startKiroPreparedTextStream(ctx, request, mapper, config, roundTrip), nil
}

func prepareKiroTextAttempt(ctx context.Context, conversation Context, config kiroInjectedTextStreamConfig, roundTrip func(*http.Request) (*http.Response, error)) (*kiroIncrementalTextMapper, error) {
	if ctx == nil || ctx.Err() != nil || roundTrip == nil || !kiroHTTPResponseLimitsValid(config.Response) || len(conversation.Tools) != 0 || config.Model.ID != config.Request.Identity.ModelID {
		return nil, errKiroLiveTextStream
	}
	mapper, err := newKiroIncrementalTextMapper(config.Model, config.Timestamp, config.MaxTextBytes, config.MaxEventBytes)
	if err != nil {
		return nil, errKiroLiveTextStream
	}
	return mapper, nil
}

// The builder has validated and snapshotted this owned request before any worker
// starts. executeKiroPreparedAttempt owns body Close; source never receives it.
func startKiroPreparedTextStream(ctx context.Context, request *http.Request, mapper *kiroIncrementalTextMapper, config kiroInjectedTextStreamConfig, roundTrip func(*http.Request) (*http.Response, error)) *AssistantMessageEventStream {
	return startKiroPreparedIncrementalStream(ctx, request, mapper, config.Response, roundTrip, func(_ context.Context, response kiroOfflineResponse) ([]AssistantMessageEvent, error) {
		return mapper.complete(response)
	})
}

// completion is an internal validated terminal projector, never caller-supplied.
// Success must return a nonempty batch ending in Done; check before indexing or
// publishing so future projector mistakes cannot leak a partial tool batch.
func startKiroPreparedIncrementalStream(ctx context.Context, request *http.Request, mapper *kiroIncrementalTextMapper, responseLimits kiroHTTPResponseLimits, roundTrip func(*http.Request) (*http.Response, error), completion func(context.Context, kiroOfflineResponse) ([]AssistantMessageEvent, error)) *AssistantMessageEventStream {
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
			response, attemptErr := executeKiroPreparedAttempt(ownedRequest, roundTrip, responseLimits, func(text string) error {
				events, err := mapper.accept(text)
				if err != nil {
					return err
				}
				return pushBatch(events)
			})
			var final AssistantMessage
			if attemptErr == nil && requestCtx.Err() == nil {
				events, err := completion(requestCtx, response)
				if err == nil && len(events) > 0 && events[len(events)-1].Type == AssistantMessageEventDone {
					// Completion evidence is committed before delivery; subsequent caller
					// cancellation may discard events but must not rewrite that evidence.
					final = events[len(events)-1].Message
					_ = pushBatch(events)
					stream.finish(final)
					return
				}
			}
			event, ok := mapper.fail(requestCtx.Err() != nil)
			if !ok {
				// A malformed internal completion may have set terminal already, but
				// no final batch was published. Do not convert that into zero/success.
				event, ok = mapper.failureEvent(requestCtx.Err() != nil), true
			}
			if ok {
				stream.push(event)
				final = event.Error
			}
			stream.finish(final)
		}()
		return stream
	})
}
