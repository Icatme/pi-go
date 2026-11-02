package pigo

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestKiroCompletedToolEventsLifecycleAndOwnership(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	r := kiroToolMessageFixture()
	r.Usage.TokenCounts = kiroCompleteCounts()
	r.Parts = append(r.Parts, kiroOfflinePart{Kind: "tool", Tool: &kiroAssembledTool{ID: "second", Name: "lookup", Arguments: json.RawMessage(`{"nested":{"v":2}}`)}})
	events, err := projectKiroCompletedToolEvents(context.Background(), r, model, stamp, []string{"lookup"}, 4096, 2, 1<<20)
	if err != nil || len(events) != 11 {
		t.Fatal("lifecycle", len(events), err)
	}
	expected := []AssistantMessageEventType{AssistantMessageEventStart, AssistantMessageEventTextStart, AssistantMessageEventTextDelta, AssistantMessageEventTextEnd, AssistantMessageEventToolCallStart, AssistantMessageEventToolCallDelta, AssistantMessageEventToolCallEnd, AssistantMessageEventToolCallStart, AssistantMessageEventToolCallDelta, AssistantMessageEventToolCallEnd, AssistantMessageEventDone}
	for i, e := range events {
		if e.Type != expected[i] {
			t.Fatalf("event %d", i)
		}
		if i < 10 && (e.Partial.StopReason != "" || e.Partial.UsageReported || e.Reason != "" || len(e.Message.Content) != 0) {
			t.Fatal("premature terminal")
		}
	}
	for _, index := range []int{1, 2} {
		start, delta, end := events[1+index*3], events[2+index*3], events[3+index*3]
		if start.ContentIndex != index || delta.ContentIndex != index || end.ContentIndex != index || len(start.Partial.Content) != index+1 {
			t.Fatal("index/future blocks")
		}
		call := start.Partial.Content[index].(ToolCall)
		if call.RawArguments != "" || call.Arguments != nil {
			t.Fatal("start includes future args")
		}
		if delta.Delta != string(r.Parts[index].Tool.Arguments) || end.ToolCall.RawArguments != delta.Delta {
			t.Fatal("raw delta changed")
		}
	}
	final := events[10].Message
	if events[10].Reason != StopReasonToolUse || !final.UsageReported || final.Usage.TotalTokens != 99 || final.Content[1].(ToolCall).Arguments["id"] != json.Number("9007199254740993") {
		t.Fatal("final evidence")
	}
	events[9].ToolCall.Arguments["nested"].(map[string]any)["v"] = "edited"
	if events[9].Partial.Content[2].(ToolCall).Arguments["nested"].(map[string]any)["v"] != json.Number("2") || final.Content[2].(ToolCall).Arguments["nested"].(map[string]any)["v"] != json.Number("2") {
		t.Fatal("event maps alias")
	}
	events[5].Partial.Content[1].(ToolCall).Arguments["id"] = "edited"
	if events[6].ToolCall.Arguments["id"] != json.Number("9007199254740993") {
		t.Fatal("snapshot alias")
	}
}

type kiroCancelDuringEventProjection struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *kiroCancelDuringEventProjection) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestKiroCompletedToolEventsCancellationIsAtomic(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	for _, after := range []int{1, 3, 6, 10} {
		base, cancel := context.WithCancel(context.Background())
		ctx := &kiroCancelDuringEventProjection{Context: base, cancel: cancel, remaining: after}
		events, err := projectKiroCompletedToolEvents(ctx, kiroToolMessageFixture(), model, stamp, []string{"lookup"}, 4096, 1, 1<<20)
		cancel()
		if err == nil || events != nil {
			t.Fatalf("cancel step %d leaked batch", after)
		}
	}
	if events, err := projectKiroCompletedToolEvents(nil, kiroToolMessageFixture(), model, stamp, []string{"lookup"}, 4096, 1, 1<<20); err == nil || events != nil {
		t.Fatal("nil ctx")
	}
}

func TestKiroCompletedToolEventsErrorsAndBudgetAreAtomic(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	for _, budget := range []int{0, 1, 4096, assistantMessageEventByteBudget + 1} {
		events, err := projectKiroCompletedToolEvents(context.Background(), kiroToolMessageFixture(), model, stamp, []string{"lookup"}, 4096, 1, budget)
		if err == nil || events != nil {
			t.Fatalf("budget %d leaked batch", budget)
		}
	}
	for _, reason := range []StopReason{StopReasonError, StopReasonAborted, StopReasonLength, StopReasonStop, ""} {
		r := kiroToolMessageFixture()
		r.StopReason = reason
		events, err := projectKiroCompletedToolEvents(context.Background(), r, model, stamp, []string{"lookup"}, 4096, 1, 1<<20)
		if err == nil || events != nil {
			t.Fatal("failed terminal emitted tools")
		}
	}
	r := kiroToolMessageFixture()
	original := string(r.Parts[1].Tool.Arguments)
	events, err := projectKiroCompletedToolEvents(context.Background(), r, model, stamp, []string{"other"}, 4096, 1, 1<<20)
	if err == nil || events != nil || !reflect.DeepEqual(string(r.Parts[1].Tool.Arguments), original) {
		t.Fatal("allowlist/input")
	}
}
