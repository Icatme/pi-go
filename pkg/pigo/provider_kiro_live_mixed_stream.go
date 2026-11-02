package pigo

import (
	"context"
	"encoding/json"
	"net/http"
)

// startKiroScopedMixedStream is internal and not registered. It sends one request,
// streams provisional text and releases tools only after complete HTTP/terminal
// validation. There is no response-triggered fallback, retry or tool execution.
// Callers must use Events/Close and treat completed ToolCalls as evidence only;
// current context, permissions and schemas remain required before execution.
func startKiroScopedMixedStream(ctx context.Context, conversation Context, schemas map[string]json.RawMessage, config kiroInjectedToolStreamConfig, source kiroCredentialSource, roundTrip func(*http.Request) (*http.Response, error)) (*AssistantMessageEventStream, error) {
	names, err := validateKiroCompletedAttempt(ctx, conversation, config, true)
	if err != nil || source == nil || roundTrip == nil {
		return nil, errKiroInjectedToolStream
	}
	mapper, err := newKiroIncrementalTextMapper(config.Model, config.Timestamp, config.MaxBytes, config.MaxEventBytes)
	if err != nil {
		return nil, errKiroInjectedToolStream
	}
	// Names and serialized request are snapshotted before the trusted source.
	request, err := newKiroOfflineRequestWithCredentialSource(ctx, conversation, schemas, config.Request, source)
	if err != nil {
		return nil, errKiroInjectedToolStream
	}
	complete := func(completionCtx context.Context, response kiroOfflineResponse) ([]AssistantMessageEvent, error) {
		if response.StopReason == StopReasonToolUse {
			return mapper.completeTools(completionCtx, response, names, config.MaxBytes, config.MaxTools)
		}
		return mapper.complete(response)
	}
	return startKiroPreparedIncrementalStream(ctx, request, mapper, config.Response, roundTrip, complete), nil
}
