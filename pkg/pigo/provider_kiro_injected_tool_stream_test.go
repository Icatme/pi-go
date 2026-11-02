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

func kiroInjectedToolFixture() (Context, map[string]json.RawMessage, kiroInjectedToolStreamConfig) {
	conversation, schemas := kiroToolProjectionFixture()
	conversation.Messages = []Message{UserMessage{Content: "find"}}
	_, model, stamp := kiroTextMessageFixture()
	return conversation, schemas, kiroInjectedToolStreamConfig{Request: kiroHTTPRequestFixture(), Response: kiroAttemptLimits(), Model: model, Timestamp: stamp, MaxBytes: 4096, MaxTools: 4, MaxEventBytes: 1 << 20}
}
func kiroInjectedToolWire() []byte {
	return kiroOfflineJoin(
		kiroOfflineEvent("toolUseEvent", `{"name":"lookup","toolUseId":"http-tool","input":"{\"id\":9007199254740993}","stop":true}`),
		kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`))
}

func TestKiroInjectedToolStreamSuccessAndReplay(t *testing.T) {
	conversation, schemas, config := kiroInjectedToolFixture()
	calls := 0
	body := &kiroAttemptBody{reader: bytes.NewReader(kiroInjectedToolWire())}
	s, err := runKiroInjectedToolStream(context.Background(), conversation, schemas, config, kiroCredentialBearer, "LOCAL_ONLY_TOOL_FAKE", func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte("toolSpecification")) {
			t.Fatal("tool request missing")
		}
		return kiroAttemptResponse(body), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	final := s.Result()
	if calls != 1 || body.closes != 1 || final.StopReason != StopReasonToolUse || len(final.Content) != 1 {
		t.Fatal("single attempt/close/result")
	}
	call := final.Content[0].(ToolCall)
	if call.ID != "http-tool" || call.Arguments["id"] != json.Number("9007199254740993") {
		t.Fatal("tool identity/precision")
	}
	count := 0
	for e := range s.Events() {
		count++
		if e.DroppedEvents != 0 {
			t.Fatal("drop")
		}
	}
	if count != 5 {
		t.Fatal(count)
	}
	conversation.Messages = append(conversation.Messages, final, ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: []ContentBlock{TextContent{Text: "fake not executed"}}})
	next, err := projectKiroToolConversation(conversation, config.Model.ID, schemas, 10, 4, 4096)
	if err != nil || string(next.History[1].Assistant.Calls[0].Input) != call.RawArguments {
		t.Fatal("replay", err)
	}
}

func TestKiroInjectedToolStreamNeverReleasesBeforeClose(t *testing.T) {
	conversation, schemas, config := kiroInjectedToolFixture()
	closing, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	body := &kiroAttemptBody{reader: bytes.NewReader(kiroInjectedToolWire()), closeErr: errors.New("private-close-error"), onClose: func() { close(closing); <-release }}
	type result struct {
		s   *AssistantMessageEventStream
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := runKiroInjectedToolStream(context.Background(), conversation, schemas, config, kiroCredentialBearer, "LOCAL_ONLY_TOOL_FAKE", func(*http.Request) (*http.Response, error) { return kiroAttemptResponse(body), nil })
		done <- result{s, err}
	}()
	select {
	case <-closing:
	case <-time.After(5 * time.Second):
		t.Fatal("Close not reached")
	}
	select {
	case r := <-done:
		if r.s != nil {
			r.s.Close()
		}
		t.Fatal("stream exposed before Close")
	default:
	}
	// Let Close fail only after confirming that completed tool wire was not enough.
	unblock()
	select {
	case r := <-done:
		if r.err == nil || r.s != nil || body.closes != 1 {
			if r.s != nil {
				r.s.Close()
			}
			t.Fatal("failed Close released tool queue")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion blocked")
	}
}

func TestKiroInjectedToolStreamFailuresReturnNoQueue(t *testing.T) {
	for _, which := range []string{"truncated", "close", "length", "unknown-tool", "cancel-close", "transport"} {
		conversation, schemas, config := kiroInjectedToolFixture()
		ctx, cancel := context.WithCancel(context.Background())
		wire := kiroInjectedToolWire()
		if which == "truncated" {
			wire = wire[:len(wire)-1]
		}
		if which == "unknown-tool" {
			wire = kiroOfflineJoin(kiroOfflineEvent("toolUseEvent", `{"name":"unseen","toolUseId":"http-tool","input":"{}","stop":true}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`))
		}
		body := &kiroAttemptBody{reader: bytes.NewReader(wire)}
		if which == "close" {
			body.closeErr = errors.New("private")
		}
		if which == "cancel-close" {
			body.onClose = cancel
		}
		calls := 0
		s, err := runKiroInjectedToolStream(ctx, conversation, schemas, config, kiroCredentialBearer, "LOCAL_ONLY_TOOL_FAKE", func(*http.Request) (*http.Response, error) {
			calls++
			r := kiroAttemptResponse(body)
			if which == "length" {
				r.ContentLength = int64(len(wire) + 1)
			}
			if which == "transport" {
				return r, errors.New("private-token")
			}
			return r, nil
		})
		cancel()
		if err == nil || s != nil || calls != 1 || body.closes != 1 {
			if s != nil {
				s.Close()
			}
			t.Fatalf("%s returned queue/incorrect ownership", which)
		}
		if err.Error() != "kiro: invalid or incomplete injected tool stream" {
			t.Fatal("private error escaped")
		}
	}
}

func TestKiroInjectedToolStreamInvalidInputNeverExecutes(t *testing.T) {
	for _, which := range []string{"no-tools", "bad-name", "model", "limits", "schema", "credential", "budget"} {
		conversation, schemas, config := kiroInjectedToolFixture()
		token := "LOCAL_ONLY_TOOL_FAKE"
		switch which {
		case "budget":
			config.MaxEventBytes = 1
		case "no-tools":
			conversation.Tools = nil
		case "bad-name":
			conversation.Tools[0].Name = " lookup"
		case "model":
			config.Model.ID = "other"
		case "limits":
			config.MaxTools = 129
		case "schema":
			schemas = nil
		case "credential":
			token = "bad\r\ntoken"
		}
		calls := 0
		s, err := runKiroInjectedToolStream(context.Background(), conversation, schemas, config, kiroCredentialBearer, token, func(*http.Request) (*http.Response, error) { calls++; return nil, nil })
		if err == nil || s != nil || calls != 0 {
			t.Fatal("invalid preflight", which)
		}
	}
}

// Executor is invoked only after request bytes and allowed names are owned.
func TestKiroInjectedToolStreamRequestAndNamesAreSnapshotted(t *testing.T) {
	conversation, schemas, config := kiroInjectedToolFixture()
	body := &kiroAttemptBody{reader: bytes.NewReader(kiroInjectedToolWire())}
	s, err := runKiroInjectedToolStream(context.Background(), conversation, schemas, config, kiroCredentialBearer, "LOCAL_ONLY_TOOL_FAKE", func(r *http.Request) (*http.Response, error) {
		conversation.Tools[0].Name = "changed"
		schemas["lookup"][0] = '['
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte(`"name":"lookup"`)) || bytes.Contains(raw, []byte("changed")) {
			t.Fatal("request bytes aliased input")
		}
		return kiroAttemptResponse(body), nil
	})
	if err != nil {
		t.Fatal("allowlist aliased input", err)
	}
	defer s.Close()
	if result := s.Result(); result.Content[0].(ToolCall).Name != "lookup" {
		t.Fatal("name snapshot lost")
	}
	for range s.Events() {
	}
	// Reusing the now-invalid original inputs cannot inherit the successful call.
	calls := 0
	next, err := runKiroInjectedToolStream(context.Background(), conversation, schemas, config, kiroCredentialBearer, "LOCAL_ONLY_TOOL_FAKE", func(*http.Request) (*http.Response, error) { calls++; return nil, nil })
	if err == nil || next != nil || calls != 0 {
		t.Fatal("attempt state leaked")
	}
}
