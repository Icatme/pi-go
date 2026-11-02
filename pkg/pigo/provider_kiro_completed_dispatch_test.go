package pigo

import (
	"bytes"
	"context"
	"net/http"
	"testing"
)

func TestKiroCompletedDispatchTextWithAndWithoutTools(t *testing.T) {
	for _, tools := range []bool{false, true} {
		for _, stop := range []string{"END_TURN", "MAX_TOKENS"} {
			conversation, schemas, config := kiroInjectedToolFixture()
			if !tools {
				conversation.Tools = nil
				schemas = nil
			}
			wire := kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"completed text"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"`+stop+`"}`))
			body := &kiroAttemptBody{reader: bytes.NewReader(wire)}
			calls := 0
			s, err := runKiroInjectedCompletedStream(context.Background(), conversation, schemas, config, kiroCredentialBearer, "LOCAL_ONLY_DISPATCH_FAKE", func(*http.Request) (*http.Response, error) { calls++; return kiroAttemptResponse(body), nil })
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			result := s.Result()
			want := StopReasonStop
			if stop == "MAX_TOKENS" {
				want = StopReasonLength
			}
			if calls != 1 || body.closes != 1 || result.StopReason != want || result.Content[0].(TextContent).Text != "completed text" {
				t.Fatal("dispatch lost text terminal")
			}
			for e := range s.Events() {
				if e.Type == AssistantMessageEventToolCallEnd || e.Type == AssistantMessageEventToolCallDelta || e.Type == AssistantMessageEventToolCallStart {
					t.Fatal("text dispatch exposed tool")
				}
			}
		}
	}
}

func TestKiroCompletedDispatchToolAndUnrequestedTool(t *testing.T) {
	for _, tools := range []bool{false, true} {
		conversation, schemas, config := kiroInjectedToolFixture()
		if !tools {
			conversation.Tools = nil
			schemas = nil
		}
		body := &kiroAttemptBody{reader: bytes.NewReader(kiroInjectedToolWire())}
		s, err := runKiroInjectedCompletedStream(context.Background(), conversation, schemas, config, kiroCredentialBearer, "LOCAL_ONLY_DISPATCH_FAKE", func(*http.Request) (*http.Response, error) { return kiroAttemptResponse(body), nil })
		if !tools {
			if err == nil || s != nil {
				t.Fatal("unrequested tool exposed")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if result := s.Result(); result.StopReason != StopReasonToolUse || result.Content[0].(ToolCall).ID != "http-tool" {
			t.Fatal("tool dispatch")
		}
		for range s.Events() {
		}
	}
}

func TestKiroCompletedDispatchTruncationNeverExecutesTools(t *testing.T) {
	conversation, schemas, config := kiroInjectedToolFixture()
	for _, stop := range []string{"MAX_TOKENS", "END_TURN", "CONTENT_FILTERED", "TOOL_USE"} {
		wire := kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"partial"}`), kiroOfflineEvent("toolUseEvent", `{"name":"lookup","toolUseId":"staged","input":"{}","stop":true}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"`+stop+`"}`))
		if stop == "TOOL_USE" {
			wire = wire[:len(wire)-1]
		}
		body := &kiroAttemptBody{reader: bytes.NewReader(wire)}
		s, err := runKiroInjectedCompletedStream(context.Background(), conversation, schemas, config, kiroCredentialBearer, "LOCAL_ONLY_DISPATCH_FAKE", func(*http.Request) (*http.Response, error) { return kiroAttemptResponse(body), nil })
		if stop == "MAX_TOKENS" {
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if result := s.Result(); result.StopReason != StopReasonLength || len(result.Content) != 1 {
				t.Fatal("length didn't discard staged tool")
			}
			for e := range s.Events() {
				if e.Type == AssistantMessageEventToolCallEnd || e.Type == AssistantMessageEventToolCallStart || e.Type == AssistantMessageEventToolCallDelta {
					t.Fatal("truncated tool escaped")
				}
			}
		} else if err == nil || s != nil {
			if s != nil {
				s.Close()
			}
			t.Fatalf("%s returned queue", stop)
		}
	}
}

func TestKiroCompletedDispatchPreservesToolOnlyContract(t *testing.T) {
	conversation, schemas, config := kiroInjectedToolFixture()
	s, err := runKiroInjectedToolStream(context.Background(), conversation, schemas, config, kiroCredentialBearer, "LOCAL_ONLY_DISPATCH_FAKE", func(*http.Request) (*http.Response, error) {
		return kiroAttemptResponse(&kiroAttemptBody{reader: bytes.NewReader(kiroAttemptWire())}), nil
	})
	if err == nil || s != nil {
		t.Fatal("tool-only API broadened")
	}
}
