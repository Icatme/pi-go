package pigo

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func TestKiroOfflineContextUsagePreservesObservationsNotAccounting(t *testing.T) {
	wire := kiroOfflineJoin(
		kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":0}`),
		kiroOfflineEvent("assistantResponseEvent", `{"content":"answer"}`),
		kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":42.5}`),
		kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":42.5}`),
		kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":100.0}`),
		kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN","tokenUsage":{"outputTokens":2,"contextUsagePercentage":43}}`),
	)
	got, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits())
	if err != nil || got.StopReason != StopReasonStop || len(got.Parts) != 5 {
		t.Fatalf("context observations rejected: %#v, %v", got, err)
	}
	for index, want := range map[int]float64{0: 0, 2: 42.5, 3: 42.5, 4: 100} {
		part := got.Parts[index]
		if part.Kind != "context_usage" || part.ContextUsagePercentage == nil || *part.ContextUsagePercentage != want ||
			part.Text != "" || part.Signature != "" || part.Tool != nil {
			t.Fatalf("observation changed: %#v", part)
		}
	}
	if got.Parts[1].Kind != "text" || got.Parts[1].Text != "answer" ||
		!reflect.DeepEqual(got.Usage.TokenCounts, map[string]int64{"outputTokens": 2}) ||
		got.Usage.ContextUsagePercentage == nil || *got.Usage.ContextUsagePercentage != 43 ||
		got.Usage.NormalizedTokenUsage != nil {
		t.Fatalf("context pressure rewritten as token/accounting metadata: %#v", got)
	}
}

func TestKiroOfflineContextUsageStrictPayloads(t *testing.T) {
	for _, payload := range []string{
		`{}`, `{"contextUsagePercentage":null}`, `{"contextUsagePercentage":"50"}`,
		`{"contextUsagePercentage":true}`, `{"contextUsagePercentage":-0}`,
		`{"contextUsagePercentage":-1}`, `{"contextUsagePercentage":1e2}`,
		`{"contextUsagePercentage":100.00000000000000001}`, `{"contextUsagePercentage":101}`,
		`{"contextUsagePercentage":1,"contextUsagePercentage":2}`,
		`{"contextUsagePercentage":1,"stopReason":"TOOL_USE"}`,
		`{"contextUsagePercentage":1,"usage":2}`, `{"usage":2}`,
	} {
		wire := kiroOfflineJoin(
			kiroOfflineEvent("assistantResponseEvent", `{"content":"staged"}`),
			kiroOfflineEvent("contextUsageEvent", payload),
			kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
		)
		got, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits())
		if err == nil || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
			t.Fatalf("unsupported context payload exposed output: %s, %#v, %v", payload, got, err)
		}
	}
}

func TestKiroOfflineContextUsageNeverAuthorizesToolsOrLateEvents(t *testing.T) {
	pressure := kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":25}`)
	tool := kiroOfflineEvent("toolUseEvent", `{"name":"lookup","toolUseId":"fixture-1","input":{},"stop":true}`)
	end := kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`)
	for _, wire := range [][]byte{
		pressure,
		kiroOfflineJoin(tool, pressure),
		kiroOfflineJoin(tool, end, pressure),
		kiroOfflineJoin(tool, pressure, kiroOfflineEvent("metadataEvent", `{"stopReason":"CONTENT_FILTERED"}`)),
	} {
		got, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits())
		if err == nil || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
			t.Fatalf("pressure authorized incomplete/late/refused stream: %#v, %v", got, err)
		}
	}
	got, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(kiroOfflineJoin(
		tool, pressure, kiroOfflineEvent("metadataEvent", `{"stopReason":"MAX_TOKENS"}`),
	)), kiroOfflineTestLimits())
	if err != nil || got.StopReason != StopReasonLength || len(got.Parts) != 1 ||
		got.Parts[0].Kind != "context_usage" || got.Parts[0].Tool != nil {
		t.Fatalf("MAX_TOKENS released tools or discarded observation: %#v, %v", got, err)
	}
}

func TestKiroOfflineContextUsageDoesNotFlushOpenTool(t *testing.T) {
	frames := kiroOfflineToolFrames()
	wire := kiroOfflineJoin(frames[0], kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":12.5}`),
		frames[1], frames[2], kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`))
	got, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits())
	if err != nil || got.StopReason != StopReasonToolUse || len(got.Parts) != 2 ||
		got.Parts[0].Kind != "context_usage" || got.Parts[1].Tool == nil ||
		got.Parts[1].Tool.ID != "fixture-1" || string(got.Parts[1].Tool.Arguments) != `{"q":"demo"}` {
		t.Fatalf("pressure changed tool assembly: %#v, %v", got, err)
	}
}

func TestKiroOfflineContextUsageRetainsEventAndPayloadBudgets(t *testing.T) {
	wire := kiroOfflineJoin(
		kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":10}`),
		kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":20}`),
		kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
	)
	for _, change := range []func(*kiroOfflineLimits){
		func(l *kiroOfflineLimits) { l.Events = 2 },
		func(l *kiroOfflineLimits) { l.TotalPayloadBytes = 2 },
		func(l *kiroOfflineLimits) { l.MessageBytes = 16 },
	} {
		limits := kiroOfflineTestLimits()
		change(&limits)
		got, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), limits)
		if err == nil || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
			t.Fatal("context event bypassed existing budgets")
		}
	}
}
