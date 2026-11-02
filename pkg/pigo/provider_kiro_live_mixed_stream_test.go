package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

func kiroMixedSource(_ context.Context, s kiroCredentialScope) (kiroScopedCredential, error) {
	return kiroScopedCredential{Scope: s, Kind: kiroCredentialBearer, Token: "LOCAL_ONLY_MIXED"}, nil
}

func TestKiroLiveMixedTextBeforeTerminalToolsAfterClose(t *testing.T) {
	conversation, schemas, config := kiroInjectedToolFixture()
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close(); pr.Close() })
	closing, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	calls := 0
	body := &kiroAttemptBody{reader: pr, onClose: func() { close(closing); <-release; pr.Close() }}
	stream, err := startKiroScopedMixedStream(context.Background(), conversation, schemas, config, kiroMixedSource, func(*http.Request) (*http.Response, error) { calls++; return kiroAttemptResponse(body), nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	go func() { pw.Write(kiroOfflineEvent("assistantResponseEvent", `{"content":"preview"}`)) }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	deltas := 0
	for {
		select {
		case e, ok := <-stream.Events():
			if !ok {
				t.Fatal("closed before preview")
			}
			if e.Type == AssistantMessageEventTextDelta {
				deltas++
				goto preview
			}
		case <-timer.C:
			t.Fatal("buffered text")
		}
	}
preview:
	go func() { pw.Write(kiroInjectedToolWire()); pw.Close() }()
	select {
	case <-closing:
	case <-timer.C:
		t.Fatal("close not reached")
	}
	select {
	case e := <-stream.Events():
		t.Fatal("event escaped while Close blocked", e.Type)
	default:
	}
	unblock()
	events := kiroReadHostEvents(t, stream)
	result := kiroReadHostResult(t, stream)
	ends := 0
	for _, e := range events {
		if e.Type == AssistantMessageEventTextDelta {
			deltas++
		}
		if e.Type == AssistantMessageEventToolCallEnd {
			ends++
		}
		if e.DroppedEvents != 0 {
			t.Fatal("dropped event")
		}
	}
	if deltas != 1 || ends != 1 || calls != 1 || body.closes != 1 || result.StopReason != StopReasonToolUse || len(result.Content) != 2 {
		t.Fatal("mixed lifecycle", deltas, ends, calls)
	}
	call := result.Content[1].(ToolCall)
	if call.ID != "http-tool" || call.RawArguments != `{"id":9007199254740993}` || call.Arguments["id"] != json.Number("9007199254740993") {
		t.Fatal("tool precision")
	}
	conversation.Messages = append(conversation.Messages, result, ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: []ContentBlock{TextContent{Text: "not actually executed"}}})
	replay, err := projectKiroToolConversation(conversation, config.Model.ID, schemas, 10, 4, 4096)
	if err != nil || string(replay.History[1].Assistant.Calls[0].Input) != call.RawArguments {
		t.Fatal("roundtrip", err)
	}
}

func TestKiroLiveMixedFailuresNeverReleaseToolsOrRetry(t *testing.T) {
	for _, mode := range []string{"truncated", "close", "cancel", "unknown-tool", "source-mutates-name"} {
		t.Run(mode, func(t *testing.T) {
			conversation, schemas, config := kiroInjectedToolFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wire := kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"preview"}`), kiroInjectedToolWire())
			if mode == "truncated" {
				wire = wire[:len(wire)-1]
			}
			if mode == "unknown-tool" {
				wire = kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"preview"}`), kiroOfflineEvent("toolUseEvent", `{"name":"other","toolUseId":"http-tool","input":"{}","stop":true}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`))
			}
			body := &kiroAttemptBody{reader: bytes.NewReader(wire)}
			if mode == "close" {
				body.closeErr = errors.New("PRIVATE")
			}
			if mode == "cancel" {
				body.onClose = cancel
			}
			sourceCalls, calls := 0, 0
			source := func(ctx context.Context, s kiroCredentialScope) (kiroScopedCredential, error) {
				sourceCalls++
				if mode == "source-mutates-name" {
					conversation.Tools[0].Name = "changed"
				}
				return kiroMixedSource(ctx, s)
			}
			stream, err := startKiroScopedMixedStream(ctx, conversation, schemas, config, source, func(*http.Request) (*http.Response, error) { calls++; return kiroAttemptResponse(body), nil })
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			events := kiroReadHostEvents(t, stream)
			result := kiroReadHostResult(t, stream)
			if mode == "source-mutates-name" {
				if result.StopReason != StopReasonToolUse || calls != 1 || sourceCalls != 1 || body.closes != 1 {
					t.Fatal("allowlist was not snapshotted")
				}
				return
			}
			for _, e := range events {
				if e.Type == AssistantMessageEventDone || e.Type == AssistantMessageEventToolCallStart || e.Type == AssistantMessageEventToolCallDelta || e.Type == AssistantMessageEventToolCallEnd {
					t.Fatal("partial tool released")
				}
			}
			if sourceCalls != 1 || calls != 1 || len(result.Content) != 0 || (result.StopReason != StopReasonError && result.StopReason != StopReasonAborted) {
				t.Fatal("failure or retry")
			}
		})
	}
}

func TestKiroLiveMixedTextAndToolOnlyTerminals(t *testing.T) {
	for _, toolOnly := range []bool{false, true} {
		conversation, schemas, config := kiroInjectedToolFixture()
		wire := kiroInjectedToolWire()
		if !toolOnly {
			wire = kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"answer without tool"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
		}
		stream, err := startKiroScopedMixedStream(context.Background(), conversation, schemas, config, kiroMixedSource, func(*http.Request) (*http.Response, error) {
			return kiroAttemptResponse(io.NopCloser(bytes.NewReader(wire))), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		kiroReadHostEvents(t, stream)
		result := kiroReadHostResult(t, stream)
		stream.Close()
		expected := StopReasonStop
		if toolOnly {
			expected = StopReasonToolUse
		}
		if result.StopReason != expected {
			t.Fatal("terminal dispatch")
		}
	}
}

func TestKiroLiveMixedPreflightAndSourceFailures(t *testing.T) {
	for _, mode := range []string{"budget", "model", "schema", "nil-context", "source-error"} {
		t.Run(mode, func(t *testing.T) {
			conversation, schemas, config := kiroInjectedToolFixture()
			var ctx context.Context = context.Background()
			sources, calls := 0, 0
			switch mode {
			case "budget":
				config.MaxEventBytes = 1
			case "model":
				config.Model.ID = "wrong"
			case "schema":
				schemas = nil
			case "nil-context":
				ctx = nil
			}
			source := func(context.Context, kiroCredentialScope) (kiroScopedCredential, error) {
				sources++
				return kiroScopedCredential{}, errors.New("PRIVATE_SOURCE")
			}
			stream, err := startKiroScopedMixedStream(ctx, conversation, schemas, config, source, func(*http.Request) (*http.Response, error) { calls++; return nil, nil })
			expected := 0
			if mode == "source-error" {
				expected = 1
			}
			if stream != nil || err != errKiroInjectedToolStream || sources != expected || calls != 0 {
				t.Fatal("unexpected source/transport")
			}
		})
	}
}

func TestKiroIncrementalWorkerRejectsMalformedCompletionBeforePublish(t *testing.T) {
	for _, mode := range []string{"empty", "tool-end-only"} {
		t.Run(mode, func(t *testing.T) {
			c := kiroLiveTextConfig()
			mapper, err := newKiroIncrementalTextMapper(c.Model, c.Timestamp, c.MaxTextBytes, c.MaxEventBytes)
			if err != nil {
				t.Fatal(err)
			}
			request, err := newKiroOfflineRequestWithToken(context.Background(), kiroLiveConversation(), nil, c.Request, kiroCredentialBearer, "LOCAL_ONLY")
			if err != nil {
				t.Fatal(err)
			}
			wire := kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"preview"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
			completion := func(context.Context, kiroOfflineResponse) ([]AssistantMessageEvent, error) {
				mapper.terminal = true
				if mode == "empty" {
					return nil, nil
				}
				return []AssistantMessageEvent{{Type: AssistantMessageEventToolCallEnd, ToolCall: ToolCall{ID: "must-not-escape"}}}, nil
			}
			stream := startKiroPreparedIncrementalStream(context.Background(), request, mapper, c.Response, func(*http.Request) (*http.Response, error) {
				return kiroAttemptResponse(io.NopCloser(bytes.NewReader(wire))), nil
			}, completion)
			defer stream.Close()
			for _, event := range kiroReadHostEvents(t, stream) {
				if event.Type == AssistantMessageEventToolCallEnd || event.Type == AssistantMessageEventDone {
					t.Fatal("malformed batch escaped")
				}
			}
			result := kiroReadHostResult(t, stream)
			if result.StopReason != StopReasonError || len(result.Content) != 0 {
				t.Fatal("malformed completion succeeded")
			}
		})
	}
}
