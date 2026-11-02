package pigo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func kiroLiveTextConfig() kiroInjectedTextStreamConfig {
	_, model, stamp := kiroTextMessageFixture()
	request := kiroHTTPRequestFixture()
	request.Identity.ModelID = model.ID
	return kiroInjectedTextStreamConfig{Request: request, Response: kiroAttemptLimits(), Model: model, Timestamp: stamp, MaxTextBytes: 4096, MaxEventBytes: assistantMessageEventByteBudget}
}
func kiroLiveConversation() Context {
	return Context{Messages: []Message{UserMessage{Content: "before"}}}
}
func kiroReadHostEvents(t *testing.T, s *AssistantMessageEventStream) []AssistantMessageEvent {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var out []AssistantMessageEvent
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				return out
			}
			out = append(out, e)
		case <-timer.C:
			t.Fatal("event delivery did not terminate")
			return nil
		}
	}
}
func kiroReadHostResult(t *testing.T, s *AssistantMessageEventStream) AssistantMessage {
	t.Helper()
	result := make(chan AssistantMessage, 1)
	go func() { result <- s.Result() }()
	select {
	case r := <-result:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("result did not terminate")
		return AssistantMessage{}
	}
}

func TestKiroLiveHostDeliversDeltaBeforeRemainingHTTPBytes(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	body := &kiroAttemptBody{reader: pr}
	calls := 0
	stream, err := startKiroInjectedTextStream(context.Background(), kiroLiveConversation(), kiroLiveTextConfig(), kiroCredentialBearer, "LOCAL_ONLY_LIVE_FAKE", func(req *http.Request) (*http.Response, error) {
		calls++
		stop := context.AfterFunc(req.Context(), func() { _ = pr.CloseWithError(context.Canceled) })
		body.onClose = func() { stop(); _ = pr.Close() }
		return kiroAttemptResponse(body), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	wrote := make(chan error, 1)
	go func() {
		_, err := pw.Write(kiroOfflineEvent("assistantResponseEvent", `{"content":"actual delta"}`))
		wrote <- err
	}()
	var events []AssistantMessageEvent
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e, ok := <-stream.Events():
			if !ok {
				t.Fatal("closed before first delta")
			}
			events = append(events, e)
			if e.Type == AssistantMessageEventTextDelta {
				if e.Delta != "actual delta" {
					t.Fatal("wrong delta")
				}
				goto delivered
			}
		case <-timer.C:
			t.Fatal("delta waited for final HTTP data")
		}
	}
delivered:
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	// Only after receiving a real host delta do we make final wire bytes available.
	go func() {
		_, _ = pw.Write(kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
		_ = pw.Close()
	}()
	events = append(events, kiroReadHostEvents(t, stream)...)
	result := kiroReadHostResult(t, stream)
	if result.StopReason != StopReasonStop || calls != 1 || body.closes != 1 || result.Content[0].(TextContent).Text != "actual delta" {
		t.Fatal("live host completion failed")
	}
	done := 0
	for _, e := range events {
		if e.Type == AssistantMessageEventDone {
			done++
		}
		if e.DroppedEvents != 0 {
			t.Fatal("unexpected drops")
		}
	}
	if done != 1 {
		t.Fatal("terminal count", done)
	}
}

func TestKiroLiveHostLateFailureNeverSendsDoneOrTools(t *testing.T) {
	for _, mode := range []string{"close", "tool", "reasoning", "context_usage"} {
		frames := [][]byte{kiroOfflineEvent("assistantResponseEvent", `{"content":"preview"}`)}
		stop := "END_TURN"
		switch mode {
		case "tool":
			frames = append(frames, kiroOfflineToolFrames()...)
			stop = "TOOL_USE"
		case "reasoning":
			frames = append(frames, kiroOfflineEvent("reasoningContentEvent", `{"text":"opaque thought"}`))
		case "context_usage":
			frames = append(frames, kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":1}`))
		}
		frames = append(frames, kiroOfflineEvent("metadataEvent", `{"stopReason":"`+stop+`"}`))
		body := &kiroAttemptBody{reader: bytes.NewReader(kiroOfflineJoin(frames...))}
		if mode == "close" {
			body.closeErr = errors.New("private-close-secret")
		}
		stream, err := startKiroInjectedTextStream(context.Background(), kiroLiveConversation(), kiroLiveTextConfig(), kiroCredentialBearer, "LOCAL_ONLY_FAILURE_FAKE", func(*http.Request) (*http.Response, error) { return kiroAttemptResponse(body), nil })
		if err != nil {
			t.Fatal(err)
		}
		events := kiroReadHostEvents(t, stream)
		result := kiroReadHostResult(t, stream)
		stream.Close()
		errorsSeen, previews := 0, 0
		for _, e := range events {
			switch e.Type {
			case AssistantMessageEventDone, AssistantMessageEventToolCallStart, AssistantMessageEventToolCallDelta, AssistantMessageEventToolCallEnd:
				t.Fatal("unsupported response authorized success/tools", mode)
			case AssistantMessageEventError:
				errorsSeen++
			case AssistantMessageEventTextDelta:
				previews++
			}
		}
		if errorsSeen != 1 || previews != 1 || result.StopReason != StopReasonError || len(result.Content) != 0 || strings.Contains(result.ErrorMessage, "secret") || body.closes != 1 {
			t.Fatal("late failure leaked result", mode)
		}
	}
}

type kiroLiveReadSignal struct {
	io.Reader
	once    sync.Once
	started chan struct{}
}

func (r *kiroLiveReadSignal) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.Reader.Read(p)
}

func TestKiroLiveHostCancelAndCloseInterruptCooperativeRead(t *testing.T) {
	for _, explicitClose := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		pr, pw := io.Pipe()
		started := make(chan struct{})
		body := &kiroAttemptBody{reader: &kiroLiveReadSignal{Reader: pr, started: started}}
		stream, err := startKiroInjectedTextStream(ctx, kiroLiveConversation(), kiroLiveTextConfig(), kiroCredentialBearer, "LOCAL_ONLY_CANCEL_FAKE", func(req *http.Request) (*http.Response, error) {
			stop := context.AfterFunc(req.Context(), func() { _ = pr.CloseWithError(context.Canceled) })
			body.onClose = func() { stop(); _ = pr.Close() }
			return kiroAttemptResponse(body), nil
		})
		if err != nil {
			cancel()
			_ = pw.Close()
			t.Fatal(err)
		}
		awaitStreamSignal(t, started)
		if explicitClose {
			stream.Close()
		} else {
			cancel()
		}
		result := kiroReadHostResult(t, stream)
		events := kiroReadHostEvents(t, stream)
		stream.Close()
		cancel()
		_ = pw.Close()
		_ = pr.Close()
		if result.StopReason != StopReasonAborted || body.closes != 1 {
			t.Fatal("cooperative cancellation did not close/abort")
		}
		if explicitClose && len(events) != 0 {
			t.Fatal("explicit Close retained delivery")
		}
		if !explicitClose && (len(events) != 1 || events[0].Reason != StopReasonAborted) {
			t.Fatal("caller cancellation terminal mismatch")
		}
	}
}

func TestKiroLiveHostSlowConsumerResultFirstNoDrops(t *testing.T) {
	for _, count := range []int{128, assistantMessageEventDeltaBuffer, assistantMessageEventDeltaBuffer + 1} {
		var frames [][]byte
		for i := 0; i < count; i++ {
			frames = append(frames, kiroOfflineEvent("assistantResponseEvent", `{"content":"x"}`))
		}
		frames = append(frames, kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
		config := kiroLiveTextConfig()
		config.Response.WireBytes = 1 << 20
		config.Response.Stream.Events = count + 4
		config.Response.Stream.TotalPayloadBytes = 1 << 20
		body := &kiroAttemptBody{reader: bytes.NewReader(kiroOfflineJoin(frames...))}
		stream, err := startKiroInjectedTextStream(context.Background(), kiroLiveConversation(), config, kiroCredentialBearer, "LOCAL_ONLY_SLOW_FAKE", func(*http.Request) (*http.Response, error) { return kiroAttemptResponse(body), nil })
		if err != nil {
			t.Fatal(err)
		}
		result := kiroReadHostResult(t, stream)
		events := kiroReadHostEvents(t, stream)
		stream.Close()
		if count > assistantMessageEventDeltaBuffer {
			if result.StopReason != StopReasonError || len(result.Content) != 0 || len(events) != assistantMessageEventDeltaBuffer+3 || events[len(events)-1].Type != AssistantMessageEventError {
				t.Fatal("excess fragment did not fail with one terminal", count)
			}
			for _, event := range events {
				if event.Type == AssistantMessageEventDone {
					t.Fatal("excess fragment sent Done")
				}
			}
		} else if result.StopReason != StopReasonStop || result.Content[0].(TextContent).Text != strings.Repeat("x", count) || len(events) != count+4 {
			t.Fatal("slow consumer lost lifecycle", count)
		}
		for _, e := range events {
			if e.DroppedEvents != 0 {
				t.Fatal("slow consumer dropped deltas", count)
			}
		}
	}
}

func TestKiroLiveHostSerializesBeforeWorkerAndRejectsPreflight(t *testing.T) {
	conversation := kiroLiveConversation()
	release := make(chan struct{})
	captured := make(chan string, 1)
	stream, err := startKiroInjectedTextStream(context.Background(), conversation, kiroLiveTextConfig(), kiroCredentialBearer, "LOCAL_ONLY_SNAPSHOT_FAKE", func(req *http.Request) (*http.Response, error) {
		<-release
		data, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		captured <- string(data)
		return kiroAttemptResponse(&kiroAttemptBody{reader: bytes.NewReader(kiroAttemptWire())}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	conversation.Messages[0] = UserMessage{Content: "after"}
	close(release)
	result := kiroReadHostResult(t, stream)
	kiroReadHostEvents(t, stream)
	if result.StopReason != StopReasonStop {
		t.Fatal("snapshot attempt failed")
	}
	body := <-captured
	if !strings.Contains(body, "before") || strings.Contains(body, "after") {
		t.Fatal("worker read mutable conversation")
	}
	for _, mode := range []string{"nil-executor", "tools", "model-mismatch", "limits", "credential", "nil-context"} {
		config := kiroLiveTextConfig()
		conversation := kiroLiveConversation()
		ctx := context.Background()
		token := "LOCAL_ONLY_PREFLIGHT_FAKE"
		calls := 0
		execute := func(*http.Request) (*http.Response, error) { calls++; return nil, nil }
		switch mode {
		case "nil-executor":
			execute = nil
		case "tools":
			conversation.Tools = []Tool{{Name: "unexpected"}}
		case "model-mismatch":
			config.Model.ID = "different"
		case "limits":
			config.Response.WireBytes = 0
		case "credential":
			token = "bad\ncredential"
		case "nil-context":
			ctx = nil
		}
		if got, err := startKiroInjectedTextStream(ctx, conversation, config, kiroCredentialBearer, token, execute); err == nil || got != nil || calls != 0 {
			t.Fatal("invalid preflight started worker", mode)
		}
	}
}
