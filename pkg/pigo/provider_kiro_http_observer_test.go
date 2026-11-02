package pigo

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

func kiroRunObservedHTTPFixture(ctx context.Context, transport func(*http.Request) (*http.Response, error), observer func(string) error) (kiroOfflineResponse, error) {
	return runKiroInjectedAttemptWithTextObserver(ctx, Context{Messages: []Message{UserMessage{Content: "fixture"}}}, nil, kiroHTTPRequestFixture(), kiroCredentialBearer, "LOCAL_ONLY_OBSERVER_FAKE", transport, kiroAttemptLimits(), observer)
}

func TestKiroObservedHTTPDeliversBeforeReadingTailAndClose(t *testing.T) {
	first := kiroOfflineEvent("assistantResponseEvent", `{"content":"incremental"}`)
	tail := kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`)
	gated := &kiroObserverGatedReader{first: bytes.NewReader(first), tail: bytes.NewReader(tail)}
	body := &kiroAttemptBody{reader: gated}
	sends, calls := 0, 0
	got, err := kiroRunObservedHTTPFixture(context.Background(), func(req *http.Request) (*http.Response, error) {
		sends++
		if req.Header.Get("Authorization") != "Bearer LOCAL_ONLY_OBSERVER_FAKE" {
			t.Fatal("explicit fake credential missing")
		}
		r := kiroAttemptResponse(body)
		r.ContentLength = int64(len(first) + len(tail))
		return r, nil
	}, func(text string) error {
		calls++
		if text != "incremental" || body.closes != 0 || gated.tailReads != 0 {
			t.Fatal("callback delayed until response completion")
		}
		gated.unlocked = true
		return nil
	})
	if err != nil || sends != 1 || calls != 1 || body.closes != 1 || got.StopReason != StopReasonStop {
		t.Fatal("observed attempt did not complete", err)
	}
	bufferedBody := &kiroAttemptBody{reader: bytes.NewReader(kiroOfflineJoin(first, tail))}
	buffered, err := kiroRunAttemptFixture(context.Background(), func(*http.Request) (*http.Response, error) { return kiroAttemptResponse(bufferedBody), nil }, kiroAttemptLimits())
	if err != nil || !reflect.DeepEqual(buffered, got) {
		t.Fatal("observer changed final evidence")
	}
}

func TestKiroObservedHTTPLateFailuresKeepOnlyProvisionalText(t *testing.T) {
	for _, mode := range []string{"close", "length", "truncated", "cancel-on-close", "conflicting-stop"} {
		ctx, cancel := context.WithCancel(context.Background())
		wire := kiroAttemptWire()
		if mode == "truncated" {
			wire = wire[:len(wire)-1]
		}
		if mode == "conflicting-stop" {
			wire = kiroOfflineJoin(wire, kiroOfflineEvent("metadataEvent", `{"stopReason":"MAX_TOKENS"}`))
		}
		body := &kiroAttemptBody{reader: bytes.NewReader(wire)}
		if mode == "close" {
			body.closeErr = errors.New("private-close-detail")
		}
		if mode == "cancel-on-close" {
			body.onClose = cancel
		}
		sends, previews := 0, 0
		got, err := kiroRunObservedHTTPFixture(ctx, func(*http.Request) (*http.Response, error) {
			sends++
			r := kiroAttemptResponse(body)
			if mode == "length" {
				r.ContentLength = int64(len(wire) + 1)
			}
			return r, nil
		}, func(text string) error {
			previews++
			if text != "fixture answer" {
				t.Fatal("unexpected text")
			}
			return nil
		})
		cancel()
		if err == nil || err.Error() != errKiroInjectedAttempt.Error() || sends != 1 || previews != 1 || body.closes != 1 || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
			t.Fatal("late HTTP failure promoted preview to success", mode)
		}
	}
}

func TestKiroObservedHTTPRejectsHeadersAndTransportBeforePreview(t *testing.T) {
	for _, mode := range []string{"redirect", "auth", "encoding", "content-type", "response-and-error"} {
		body := &kiroAttemptBody{reader: bytes.NewReader(kiroAttemptWire())}
		sends, previews := 0, 0
		got, err := kiroRunObservedHTTPFixture(context.Background(), func(*http.Request) (*http.Response, error) {
			sends++
			r := kiroAttemptResponse(body)
			switch mode {
			case "redirect":
				r.StatusCode = 307
				r.Header.Set("Location", "https://unused.invalid/")
			case "auth":
				r.StatusCode = 401
			case "encoding":
				r.Header.Set("Content-Encoding", "gzip")
			case "content-type":
				r.Header.Set("Content-Type", "application/json")
			case "response-and-error":
				return r, errors.New("private-transport-detail")
			}
			return r, nil
		}, func(string) error { previews++; return nil })
		if err == nil || err.Error() != errKiroInjectedAttempt.Error() || sends != 1 || previews != 0 || body.reads != 0 || body.closes != 1 || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
			t.Fatal("rejected response observed or retried", mode)
		}
	}
}

func TestKiroObservedHTTPCallbackFailureCancelsNoRetry(t *testing.T) {
	for _, cancelInside := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		gated := &kiroObserverGatedReader{first: bytes.NewReader(kiroOfflineEvent("assistantResponseEvent", `{"content":"preview"}`)), tail: bytes.NewReader(nil)}
		body := &kiroAttemptBody{reader: gated}
		sends, previews := 0, 0
		got, err := kiroRunObservedHTTPFixture(ctx, func(*http.Request) (*http.Response, error) { sends++; return kiroAttemptResponse(body), nil }, func(string) error {
			previews++
			if cancelInside {
				cancel()
				return nil
			}
			return errors.New("private-callback-detail")
		})
		cancel()
		if err == nil || err.Error() != errKiroInjectedAttempt.Error() || sends != 1 || previews != 1 || gated.tailReads != 0 || body.closes != 1 || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
			t.Fatal("callback error leaked, retried, or read ahead")
		}
	}
}

func TestKiroObservedHTTPNoImplicitExecutorOrCancelledSend(t *testing.T) {
	previews := 0
	if got, err := kiroRunObservedHTTPFixture(context.Background(), nil, func(string) error { previews++; return nil }); err == nil || previews != 0 || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
		t.Fatal("nil transport admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sends := 0
	if got, err := kiroRunObservedHTTPFixture(ctx, func(*http.Request) (*http.Response, error) { sends++; return nil, nil }, func(string) error { previews++; return nil }); err == nil || sends != 0 || previews != 0 || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
		t.Fatal("cancelled request sent")
	}
}

func TestKiroObservedHTTPProbeByteCannotAuthorizePreview(t *testing.T) {
	frame := kiroOfflineEvent("assistantResponseEvent", `{"content":"probe-boundary"}`)
	for _, shortDeclared := range []bool{false, true} {
		body := &kiroAttemptBody{reader: bytes.NewReader(frame)}
		limits := kiroAttemptLimits()
		if !shortDeclared {
			limits.WireBytes = int64(len(frame) - 1)
		}
		sends, previews := 0, 0
		got, err := runKiroInjectedAttemptWithTextObserver(context.Background(), Context{Messages: []Message{UserMessage{Content: "fixture"}}}, nil, kiroHTTPRequestFixture(), kiroCredentialBearer, "LOCAL_ONLY_PROBE_FAKE", func(*http.Request) (*http.Response, error) {
			sends++
			r := kiroAttemptResponse(body)
			if shortDeclared {
				r.ContentLength = int64(len(frame) - 1)
			}
			return r, nil
		}, limits, func(string) error { previews++; return nil })
		if err == nil || sends != 1 || previews != 0 || body.closes != 1 || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
			t.Fatal("oversized frame released preview")
		}
	}
}

func TestKiroObservedHTTPExactWireBoundaryStillAllowsValidPreview(t *testing.T) {
	frame := kiroOfflineEvent("assistantResponseEvent", `{"content":"within-boundary"}`)
	body := &kiroAttemptBody{reader: bytes.NewReader(frame)}
	limits := kiroAttemptLimits()
	limits.WireBytes = int64(len(frame))
	previews := 0
	got, err := runKiroInjectedAttemptWithTextObserver(context.Background(), Context{Messages: []Message{UserMessage{Content: "fixture"}}}, nil, kiroHTTPRequestFixture(), kiroCredentialBearer, "LOCAL_ONLY_BOUNDARY_FAKE", func(*http.Request) (*http.Response, error) { return kiroAttemptResponse(body), nil }, limits, func(string) error { previews++; return nil })
	// The frame is within budget; missing stop metadata still denies final success.
	if err == nil || previews != 1 || body.closes != 1 || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
		t.Fatal("valid preview lost or missing terminal accepted")
	}
}
