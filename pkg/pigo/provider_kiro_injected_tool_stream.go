package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

var errKiroInjectedToolStream = errors.New("kiro: invalid or incomplete injected tool stream")

type kiroInjectedToolStreamConfig struct {
	Request       kiroHTTPRequestOptions
	Response      kiroHTTPResponseLimits
	Model         Model
	Timestamp     time.Time
	MaxBytes      int
	MaxTools      int
	MaxEventBytes int
}

// runKiroInjectedToolStream is a SYNCHRONOUS, buffered single-attempt bridge.
// It does not return any stream until HTTP EOF/length/Close AND whole tool/event/
// queue validation succeed. It is not registered or a realtime provider API.
// Inputs must not mutate while this call runs; request/name data are snapshotted
// before invoking the trusted executor. No tool callback or automatic network
// client exists. The executor/Read/Close must cooperate with context.
// Returned calls are evidence only: agent permissions/schema/context remain
// required at execution, and later cancellation cannot revoke completed Result.
func runKiroInjectedToolStream(ctx context.Context, conversation Context, schemas map[string]json.RawMessage, config kiroInjectedToolStreamConfig, kind kiroCredentialKind, token string, roundTrip func(*http.Request) (*http.Response, error)) (*AssistantMessageEventStream, error) {
	return runKiroInjectedCompletedAttempt(ctx, conversation, schemas, config, kind, token, roundTrip, false)
}

// runKiroInjectedCompletedStream dispatches validated text or tool termination.
// It is buffered and synchronous, not the incremental-text adapter or a
// registered provider. A declared tool set does not force a tool response.
func runKiroInjectedCompletedStream(ctx context.Context, conversation Context, schemas map[string]json.RawMessage, config kiroInjectedToolStreamConfig, kind kiroCredentialKind, token string, roundTrip func(*http.Request) (*http.Response, error)) (*AssistantMessageEventStream, error) {
	return runKiroInjectedCompletedAttempt(ctx, conversation, schemas, config, kind, token, roundTrip, true)
}

func runKiroInjectedCompletedAttempt(ctx context.Context, conversation Context, schemas map[string]json.RawMessage, config kiroInjectedToolStreamConfig, kind kiroCredentialKind, token string, roundTrip func(*http.Request) (*http.Response, error), allowText bool) (*AssistantMessageEventStream, error) {
	if ctx == nil || ctx.Err() != nil || roundTrip == nil || !kiroHTTPResponseLimitsValid(config.Response) || !kiroTextModelIdentityValid(config.Model) || config.Model.ID != config.Request.Identity.ModelID || config.MaxBytes < 2 || config.MaxBytes > 1<<20 || config.MaxTools < 1 || config.MaxTools > 128 || config.MaxEventBytes < 1 || config.MaxEventBytes > assistantMessageEventByteBudget || (!allowText && len(conversation.Tools) < 1) || len(conversation.Tools) > 128 {
		return nil, errKiroInjectedToolStream
	}
	// Reject an obviously impossible budget before any potentially billable call.
	// Actual content/snapshot costs still require post-response preflight.
	identity := AssistantMessage{API: config.Model.API, Provider: config.Model.Provider, Model: config.Model.ID, Timestamp: config.Timestamp}
	failed := identity
	failed.StopReason, failed.ErrorMessage = StopReasonAborted, "kiro: buffered tool delivery cancelled"
	minimum := assistantEventBytes(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: identity}) +
		assistantEventBytes(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: StopReasonAborted, Error: failed})
	if config.MaxEventBytes < minimum {
		return nil, errKiroInjectedToolStream
	}
	names := make([]string, len(conversation.Tools))
	for i, tool := range conversation.Tools {
		names[i] = tool.Name
	}
	if len(names) > 0 && kiroAllowedToolNames(names) == nil {
		return nil, errKiroInjectedToolStream
	}
	request, err := newKiroOfflineRequestWithToken(ctx, conversation, schemas, config.Request, kind, token)
	if err != nil {
		return nil, errKiroInjectedToolStream
	}
	response, err := executeKiroPreparedAttempt(request, roundTrip, config.Response, nil)
	if err != nil || ctx.Err() != nil {
		return nil, errKiroInjectedToolStream
	}
	var stream *AssistantMessageEventStream
	switch response.StopReason {
	case StopReasonToolUse:
		stream, err = newKiroCompletedToolStream(ctx, response, config.Model, config.Timestamp, names, config.MaxBytes, config.MaxTools, config.MaxEventBytes)
	case StopReasonStop, StopReasonLength:
		if !allowText {
			return nil, errKiroInjectedToolStream
		}
		stream, err = newKiroCompletedTextStream(ctx, response, config.Model, config.Timestamp, config.MaxBytes, config.MaxEventBytes)
	default:
		return nil, errKiroInjectedToolStream
	}
	if err != nil {
		return nil, errKiroInjectedToolStream
	}
	return stream, nil
}
