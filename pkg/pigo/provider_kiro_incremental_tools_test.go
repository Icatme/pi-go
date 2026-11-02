package pigo

import (
	"context"
	"encoding/json"
	"testing"
)

func TestKiroIncrementalToolCompletionNoTextReplayAndOwnership(t *testing.T) {
	m, _ := kiroMapperFixture(t)
	first, err := m.accept("look")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.accept("up")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range append(first, second...) {
		if e.Type == AssistantMessageEventToolCallStart || e.Type == AssistantMessageEventToolCallEnd {
			t.Fatal("provisional tools")
		}
	}
	response := kiroToolMessageFixture()
	response.Usage.TokenCounts = kiroCompleteCounts()
	response.Parts = append(response.Parts, kiroOfflinePart{Kind: "tool", Tool: &kiroAssembledTool{ID: "second", Name: "lookup", Arguments: json.RawMessage(`{"nested":{"v":2}}`)}})
	events, err := m.completeTools(context.Background(), response, []string{"lookup"}, 4096, 2)
	if err != nil || len(events) != 8 {
		t.Fatal("completion", len(events), err)
	}
	if events[0].Type != AssistantMessageEventTextEnd || events[0].Content != "lookup" {
		t.Fatal("text end")
	}
	for _, e := range events {
		if e.Type == AssistantMessageEventStart || e.Type == AssistantMessageEventTextDelta || e.Type == AssistantMessageEventTextStart {
			t.Fatal("text replay")
		}
	}
	start := events[1].Partial.Content[1].(ToolCall)
	if start.RawArguments != "" || start.Arguments != nil {
		t.Fatal("future argument snapshot")
	}
	done := events[7].Message
	if done.StopReason != StopReasonToolUse || len(done.Content) != 3 || !done.UsageReported || done.Content[1].(ToolCall).Arguments["id"] != json.Number("9007199254740993") {
		t.Fatal("final evidence")
	}
	events[3].ToolCall.Arguments["id"] = "edited"
	if done.Content[1].(ToolCall).RawArguments != `{"id":9007199254740993}` || done.Content[1].(ToolCall).Arguments["id"] != json.Number("9007199254740993") {
		t.Fatal("shared args")
	}
	if _, ok := m.fail(false); ok {
		t.Fatal("error after done")
	}
	if e, err := m.completeTools(context.Background(), response, []string{"lookup"}, 4096, 2); err == nil || e != nil {
		t.Fatal("duplicate completion")
	}
}

func TestKiroIncrementalToolOnlyCompletion(t *testing.T) {
	m, _ := kiroMapperFixture(t)
	response := kiroToolMessageFixture()
	response.Parts = response.Parts[1:]
	events, err := m.completeTools(context.Background(), response, []string{"lookup"}, 4096, 1)
	if err != nil || len(events) != 5 || events[0].Type != AssistantMessageEventStart || events[1].ContentIndex != 0 || events[4].Type != AssistantMessageEventDone {
		t.Fatal("tool-only", err)
	}
	for _, e := range events {
		if e.Type == AssistantMessageEventTextStart || e.Type == AssistantMessageEventTextDelta || e.Type == AssistantMessageEventTextEnd {
			t.Fatal("invented text")
		}
	}
}

func TestKiroIncrementalToolCompletionFailureIsAtomic(t *testing.T) {
	for _, mode := range []string{"mismatch", "missing-preview", "interleaved", "allowlist", "terminal", "budget", "cancel", "nil-context", "missing-usage"} {
		t.Run(mode, func(t *testing.T) {
			m, _ := kiroMapperFixture(t)
			response := kiroToolMessageFixture()
			var ctx context.Context = context.Background()
			allowed := []string{"lookup"}
			if mode != "missing-preview" {
				if _, err := m.accept("lookup"); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "mismatch":
				response.Parts[0].Text = "other"
			case "interleaved":
				response.Parts = append(response.Parts, kiroOfflinePart{Kind: "text", Text: "late"})
			case "allowlist":
				allowed = []string{"other"}
			case "terminal":
				response.StopReason = StopReasonLength
			case "budget":
				m.remainingBytes = 1
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-context":
				ctx = nil
			case "missing-usage":
				response.Usage.TokenCounts = map[string]int64{"outputTokens": 1}
			}
			events, err := m.completeTools(ctx, response, allowed, 4096, 1)
			if err == nil || events != nil {
				t.Fatal("partial final batch escaped")
			}
			if _, err := m.accept("later"); err == nil {
				t.Fatal("failure not sticky")
			}
			failure, ok := m.fail(false)
			if !ok || len(failure.Error.Content) != 0 || failure.Type != AssistantMessageEventError {
				t.Fatal("failure retains executable content")
			}
		})
	}
}

func TestKiroIncrementalToolCompletionCancellationDuringBatch(t *testing.T) {
	for _, after := range []int{1, 3, 6, 8} {
		m, _ := kiroMapperFixture(t)
		m.accept("lookup")
		base, cancel := context.WithCancel(context.Background())
		ctx := &kiroCancelDuringEventProjection{Context: base, cancel: cancel, remaining: after}
		events, err := m.completeTools(ctx, kiroToolMessageFixture(), []string{"lookup"}, 4096, 1)
		cancel()
		if err == nil || events != nil {
			t.Fatalf("cancel step %d leaked tools", after)
		}
	}
}

func TestKiroIncrementalToolCompletionCombinedDeltaBudget(t *testing.T) {
	for _, count := range []int{assistantMessageEventDeltaBuffer - 1, assistantMessageEventDeltaBuffer} {
		_, model, stamp := kiroTextMessageFixture()
		m, err := newKiroIncrementalTextMapper(model, stamp, 4096, assistantMessageEventByteBudget)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < count; i++ {
			if _, err := m.accept(""); err != nil {
				t.Fatal("preview budget", err)
			}
		}
		response := kiroToolMessageFixture()
		response.Parts[0].Text = ""
		events, err := m.completeTools(context.Background(), response, []string{"lookup"}, 4096, 1)
		if count == assistantMessageEventDeltaBuffer {
			if err == nil || events != nil {
				t.Fatal("combined delta overflow")
			}
		} else if err != nil {
			t.Fatal("exact combined limit", err)
		}
	}
}
