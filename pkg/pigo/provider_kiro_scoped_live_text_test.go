package pigo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestKiroScopedLiveTextDeltaBeforeEOFAndSourceSnapshot(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close(); pr.Close() })
	conversation := kiroLiveConversation()
	config := kiroLiveTextConfig()
	sourceCalls, httpCalls := 0, 0
	source := func(_ context.Context, scope kiroCredentialScope) (kiroScopedCredential, error) {
		sourceCalls++
		conversation.Messages[0] = UserMessage{Content: "after-source"}
		return kiroScopedCredential{Scope: scope, Kind: kiroCredentialBearer, Token: "LOCAL_ONLY_SCOPED"}, nil
	}
	body := &kiroAttemptBody{reader: pr}
	stream, err := startKiroScopedTextStream(context.Background(), conversation, config, source, func(request *http.Request) (*http.Response, error) {
		httpCalls++
		raw, _ := io.ReadAll(request.Body)
		if strings.Contains(string(raw), "after-source") || !strings.Contains(string(raw), "before") || request.Header.Get("Authorization") != "Bearer LOCAL_ONLY_SCOPED" {
			t.Error("source/request isolation")
		}
		stop := context.AfterFunc(request.Context(), func() { pr.CloseWithError(context.Canceled) })
		body.onClose = func() { stop(); pr.Close() }
		return kiroAttemptResponse(body), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	go func() { pw.Write(kiroOfflineEvent("assistantResponseEvent", `{"content":"actual delta"}`)) }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-stream.Events():
			if !ok {
				t.Fatal("closed before delta")
			}
			if event.Type == AssistantMessageEventTextDelta {
				if event.Delta != "actual delta" {
					t.Fatal("delta")
				}
				goto delivered
			}
		case <-timer.C:
			t.Fatal("delta waited for EOF")
		}
	}
delivered:
	// Final wire does not exist until after the actual host delta was observed.
	go func() { pw.Write(kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`)); pw.Close() }()
	events := kiroReadHostEvents(t, stream)
	result := kiroReadHostResult(t, stream)
	done := 0
	for _, event := range events {
		if event.Type == AssistantMessageEventDone {
			done++
		}
	}
	if done != 1 || result.StopReason != StopReasonStop || sourceCalls != 1 || httpCalls != 1 || body.closes != 1 {
		t.Fatal("single scoped completion")
	}
}

func TestKiroScopedLiveTextPreflightBeforeSource(t *testing.T) {
	for _, mode := range []string{"nil-source", "nil-executor", "nil-context", "tools", "system", "budget", "model"} {
		t.Run(mode, func(t *testing.T) {
			config := kiroLiveTextConfig()
			conversation := kiroLiveConversation()
			var ctx context.Context = context.Background()
			sourceCalls, httpCalls := 0, 0
			source := kiroCredentialSource(func(context.Context, kiroCredentialScope) (kiroScopedCredential, error) {
				sourceCalls++
				return kiroScopedCredential{}, nil
			})
			executor := func(*http.Request) (*http.Response, error) { httpCalls++; return nil, nil }
			switch mode {
			case "nil-source":
				source = nil
			case "nil-executor":
				executor = nil
			case "nil-context":
				ctx = nil
			case "tools":
				conversation.Tools = []Tool{{Name: "lookup"}}
			case "system":
				conversation.SystemPrompt = "must reject by default"
			case "budget":
				config.MaxEventBytes = 1
			case "model":
				config.Model.ID = "different"
			}
			stream, err := startKiroScopedTextStream(ctx, conversation, config, source, executor)
			if stream != nil || err != errKiroLiveTextStream || sourceCalls != 0 || httpCalls != 0 {
				t.Fatal("preflight started source/worker")
			}
		})
	}
}

func TestKiroScopedLiveTextCredentialErrorsNeverStartWorker(t *testing.T) {
	for _, mode := range []string{"error", "scope", "token", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sourceCalls, httpCalls := 0, 0
			source := func(_ context.Context, scope kiroCredentialScope) (kiroScopedCredential, error) {
				sourceCalls++
				credential := kiroScopedCredential{Scope: scope, Kind: kiroCredentialBearer, Token: "LOCAL_ONLY"}
				switch mode {
				case "error":
					return credential, errors.New("PRIVATE_SOURCE")
				case "scope":
					credential.Scope.ProfileARN = "wrong"
				case "token":
					credential.Token = "bad\nPRIVATE"
				case "cancel":
					cancel()
				}
				return credential, nil
			}
			stream, err := startKiroScopedTextStream(ctx, kiroLiveConversation(), kiroLiveTextConfig(), source, func(*http.Request) (*http.Response, error) { httpCalls++; return nil, nil })
			if stream != nil || err != errKiroLiveTextStream || sourceCalls != 1 || httpCalls != 0 {
				t.Fatal("credential failure started stream")
			}
		})
	}
}

func TestKiroScopedLiveTextLateFailureNeverReplaysOrReleasesTools(t *testing.T) {
	for _, mode := range []string{"close", "truncated", "unsolicited-tool"} {
		t.Run(mode, func(t *testing.T) {
			frames := [][]byte{kiroOfflineEvent("assistantResponseEvent", `{"content":"preview"}`)}
			if mode == "unsolicited-tool" {
				frames = append(frames, kiroInjectedToolWire())
			} else {
				frames = append(frames, kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
			}
			wire := kiroOfflineJoin(frames...)
			if mode == "truncated" {
				wire = wire[:len(wire)-1]
			}
			body := &kiroAttemptBody{reader: bytes.NewReader(wire)}
			if mode == "close" {
				body.closeErr = errors.New("PRIVATE_CLOSE")
			}
			sourceCalls, httpCalls := 0, 0
			source := func(_ context.Context, scope kiroCredentialScope) (kiroScopedCredential, error) {
				sourceCalls++
				return kiroScopedCredential{Scope: scope, Kind: kiroCredentialBearer, Token: "LOCAL_ONLY"}, nil
			}
			stream, err := startKiroScopedTextStream(context.Background(), kiroLiveConversation(), kiroLiveTextConfig(), source, func(*http.Request) (*http.Response, error) { httpCalls++; return kiroAttemptResponse(body), nil })
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			events := kiroReadHostEvents(t, stream)
			result := kiroReadHostResult(t, stream)
			for _, event := range events {
				switch event.Type {
				case AssistantMessageEventDone, AssistantMessageEventToolCallStart, AssistantMessageEventToolCallDelta, AssistantMessageEventToolCallEnd:
					t.Fatal("success or tool escaped")
				}
			}
			if sourceCalls != 1 || httpCalls != 1 || body.closes != 1 || result.StopReason != StopReasonError || len(result.Content) != 0 || strings.Contains(result.ErrorMessage, "PRIVATE") {
				t.Fatal("failed attempt retained evidence or retried")
			}
		})
	}
}
