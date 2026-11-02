package pigo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"testing"
)

func TestKiroCompletedContextDiagnosticsPreserveObservationOrder(t *testing.T) {
	first, second, metadata := 12.5, 13.0, 14.0
	response, model, stamp := kiroTextMessageFixture()
	response.Parts = []kiroOfflinePart{{Kind: "context_usage", ContextUsagePercentage: &first}, {Kind: "text", Text: "answer"}, {Kind: "context_usage", ContextUsagePercentage: &second}}
	response.Usage.ContextUsagePercentage = &metadata
	message, err := projectKiroCompletedText(response, model, stamp, 4096)
	if err != nil || len(message.Content) != 1 || len(message.Diagnostics) != 3 {
		t.Fatal("projection", err)
	}
	for i, expected := range []float64{12.5, 13, 14} {
		d := message.Diagnostics[i]
		if d.Type != "kiro_context_usage" || d.Details["percentage"] != expected || d.Timestamp != stamp || d.Error != nil {
			t.Fatal("diagnostic")
		}
	}
	if message.Diagnostics[0].Details["source_event"] != "contextUsageEvent" || message.Diagnostics[0].Details["part_index"] != 0 || message.Diagnostics[1].Details["part_index"] != 2 || message.Diagnostics[2].Details["source_event"] != "metadataEvent" {
		t.Fatal("source/order")
	}
	if _, fabricated := message.Diagnostics[2].Details["part_index"]; fabricated {
		t.Fatal("unknown metadata position fabricated")
	}
	if message.UsageReported || !reflect.DeepEqual(message.Usage, Usage{}) || response.Usage.ContextUsagePercentage != &metadata || len(response.Parts) != 3 {
		t.Fatal("usage/input mutation")
	}
	first = 99
	metadata = 99
	if message.Diagnostics[0].Details["percentage"] != 12.5 || message.Diagnostics[2].Details["percentage"] != 14.0 {
		t.Fatal("pointer alias")
	}
}

func TestKiroCompletedContextDiagnosticsCollectorToToolQueue(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	tool := kiroInjectedToolWire()
	// Context observation is valid before explicit metadata terminal, not after.
	wire := kiroOfflineJoin(kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":25}`), tool)
	response, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := newKiroCompletedToolStream(context.Background(), response, model, stamp, []string{"lookup"}, 4096, 4, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	result := stream.Result()
	if result.StopReason != StopReasonToolUse || len(result.Diagnostics) != 1 || result.UsageReported {
		t.Fatal("tool diagnostics")
	}
	result.Diagnostics[0].Details["percentage"] = 99.0
	done := 0
	for event := range stream.Events() {
		if event.Type == AssistantMessageEventDone {
			done++
			if event.Message.Diagnostics[0].Details["percentage"] != 25.0 {
				t.Fatal("Result aliases Done")
			}
		}
		if len(event.Partial.Diagnostics) != 0 {
			t.Fatal("observation released on partial")
		}
	}
	if done != 1 {
		t.Fatal("terminal")
	}
}

func TestKiroCompletedContextDiagnosticsRejectMalformedAndIncomplete(t *testing.T) {
	for _, value := range []float64{-1, 101, math.NaN(), math.Inf(1), math.Copysign(0, -1)} {
		response, model, stamp := kiroTextMessageFixture()
		response.Parts = append(response.Parts, kiroOfflinePart{Kind: "context_usage", ContextUsagePercentage: &value})
		if message, err := projectKiroCompletedText(response, model, stamp, 4096); err == nil || !reflect.DeepEqual(message, AssistantMessage{}) {
			t.Fatal("invalid observation")
		}
	}
	for _, part := range []kiroOfflinePart{{Kind: "context_usage"}, {Kind: "context_usage", Text: "not context"}, {Kind: "context_usage", Signature: "opaque"}, {Kind: "context_usage", Tool: &kiroAssembledTool{}}} {
		response, model, stamp := kiroTextMessageFixture()
		response.Parts = append(response.Parts, part)
		if _, err := projectKiroCompletedText(response, model, stamp, 4096); err == nil {
			t.Fatal("malformed observation")
		}
	}
	response, model, stamp := kiroTextMessageFixture()
	value := 50.0
	response.Parts = []kiroOfflinePart{{Kind: "context_usage", ContextUsagePercentage: &value}}
	if _, err := projectKiroCompletedText(response, model, stamp, 4096); err == nil {
		t.Fatal("context alone became text")
	}
	response.Parts = append(response.Parts, kiroOfflinePart{Kind: "text", Text: "answer"})
	response.Usage.StopReason = ""
	if _, err := projectKiroCompletedText(response, model, stamp, 4096); err == nil {
		t.Fatal("context became completion")
	}
}

func TestKiroCompletedContextDiagnosticsChargeProjectionAndEventBudgets(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	baseline, err := projectKiroCompletedTextEvents(response, model, stamp, 4096, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	budget := 0
	for _, event := range baseline {
		budget += assistantEventBytes(event)
	}
	value := 50.0
	response.Parts = append(response.Parts, kiroOfflinePart{Kind: "context_usage", ContextUsagePercentage: &value})
	if events, err := projectKiroCompletedTextEvents(response, model, stamp, 4096, budget); err == nil || events != nil {
		t.Fatal("diagnostic not charged")
	}
	if _, err := projectKiroCompletedText(response, model, stamp, 10); err == nil {
		t.Fatal("projection diagnostic budget")
	}
	response.Parts = make([]kiroOfflinePart, 4097)
	if _, err := projectKiroCompletedText(response, model, stamp, 4096); err == nil {
		t.Fatal("part ceiling")
	}
}

func TestKiroCompletedContextDiagnosticsPublicRegistration(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "truncated"}[truncated], func(t *testing.T) {
			config, model := kiroRegistrationFixture(t)
			wire := kiroOfflineJoin(kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":37.5}`), kiroOfflineEvent("assistantResponseEvent", `{"content":"answer"}`), kiroOfflineEvent("metadataEvent", `{"tokenUsage":{"contextUsagePercentage":40},"stopReason":"END_TURN"}`))
			if truncated {
				wire = wire[:len(wire)-1]
			}
			config.RoundTrip = func(*http.Request) (*http.Response, error) {
				return kiroAttemptResponse(io.NopCloser(bytes.NewReader(wire))), nil
			}
			if err := RegisterKiroBufferedModule(t.Name(), config); err != nil {
				t.Fatal(err)
			}
			defer UnregisterAPIModules(t.Name())
			stream := StreamSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{RequestContext: context.Background()})
			done := 0
			for event := range stream.Events() {
				if event.Type == AssistantMessageEventDone {
					done++
					if len(event.Message.Diagnostics) != 2 {
						t.Error("Done observations lost")
					}
				}
			}
			result := stream.Result()
			stream.Close()
			if truncated {
				if done != 0 || len(result.Diagnostics) != 0 || result.StopReason != StopReasonError {
					t.Fatal("incomplete observations exposed")
				}
				return
			}
			if done != 1 || result.StopReason != StopReasonStop || len(result.Diagnostics) != 2 || result.UsageReported || result.Diagnostics[0].Details["percentage"] != 37.5 || result.Diagnostics[1].Details["percentage"] != 40.0 {
				t.Fatalf("registered diagnostics: done=%d stop=%s count=%d reported=%t details=%v", done, result.StopReason, len(result.Diagnostics), result.UsageReported, result.Diagnostics)
			}
		})
	}
}

func TestKiroCompletedContextDiagnosticsLiveTextFinalOnly(t *testing.T) {
	for _, lateFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "close-error"}[lateFailure], func(t *testing.T) {
			wire := kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"preview"}`), kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":1}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
			body := &kiroAttemptBody{reader: bytes.NewReader(wire)}
			if lateFailure {
				body.closeErr = errors.New("private close")
			}
			stream, err := startKiroInjectedTextStream(context.Background(), kiroLiveConversation(), kiroLiveTextConfig(), kiroCredentialBearer, "LOCAL_ONLY", func(*http.Request) (*http.Response, error) { return kiroAttemptResponse(body), nil })
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			events := kiroReadHostEvents(t, stream)
			result := kiroReadHostResult(t, stream)
			done, previews := 0, 0
			for _, event := range events {
				if len(event.Partial.Diagnostics) != 0 {
					t.Fatal("diagnostics on provisional message")
				}
				switch event.Type {
				case AssistantMessageEventTextDelta:
					previews++
				case AssistantMessageEventDone:
					done++
					if len(event.Message.Diagnostics) != 1 {
						t.Error("final diagnostics")
					}
				case AssistantMessageEventToolCallStart, AssistantMessageEventToolCallDelta, AssistantMessageEventToolCallEnd:
					t.Fatal("tools in text stream")
				}
			}
			if previews != 1 || body.closes != 1 {
				t.Fatal("preview/close")
			}
			if lateFailure {
				if done != 0 || len(result.Diagnostics) != 0 || result.StopReason != StopReasonError {
					t.Fatal("late failure retained evidence")
				}
				return
			}
			if done != 1 || len(result.Diagnostics) != 1 || result.Diagnostics[0].Details["percentage"] != 1.0 || result.StopReason != StopReasonStop {
				t.Fatal("live final evidence")
			}
		})
	}
}
