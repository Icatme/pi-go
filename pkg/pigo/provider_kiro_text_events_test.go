package pigo

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestKiroBufferedTextEventsLifecycle(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	response.Parts = []kiroOfflinePart{{Kind: "text", Text: "你好"}, {Kind: "text"}, {Kind: "text", Text: " world"}}
	response.Usage.TokenCounts = kiroCompleteCounts()
	events, err := projectKiroCompletedTextEvents(response, model, stamp, 1024, 1<<20)
	if err != nil || len(events) != 11 || events[0].Type != AssistantMessageEventStart || len(events[0].Partial.Content) != 0 {
		t.Fatal("invalid start or event count", err)
	}
	for index, part := range response.Parts {
		start, delta, end := events[1+index*3], events[2+index*3], events[3+index*3]
		if start.Type != AssistantMessageEventTextStart || delta.Type != AssistantMessageEventTextDelta || end.Type != AssistantMessageEventTextEnd || start.ContentIndex != index || delta.ContentIndex != index || end.ContentIndex != index || delta.Delta != part.Text || end.Content != part.Text {
			t.Fatal("text lifecycle changed")
		}
		if len(start.Partial.Content) != index+1 || start.Partial.Content[index].(TextContent).Text != "" || len(delta.Partial.Content) != index+1 || delta.Partial.Content[index].(TextContent).Text != part.Text {
			t.Fatal("partial includes future text or omits current block")
		}
		for previous := 0; previous < index; previous++ {
			if start.Partial.Content[previous].(TextContent).Text != response.Parts[previous].Text {
				t.Fatal("previous text lost")
			}
		}
	}
	for _, event := range events[:len(events)-1] {
		if event.Partial.StopReason != "" || event.Partial.UsageReported || event.Partial.Usage != (Usage{}) || event.Reason != "" || !reflect.DeepEqual(event.Message, AssistantMessage{}) {
			t.Fatal("future completion or usage leaked into partial")
		}
	}
	want, _ := projectKiroCompletedText(response, model, stamp, 1024)
	last := events[len(events)-1]
	if last.Type != AssistantMessageEventDone || last.Reason != StopReasonStop || !reflect.DeepEqual(last.Message, want) {
		t.Fatal("terminal does not preserve completed message")
	}
}

func TestKiroBufferedTextEventsOwnershipAndLength(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	response.StopReason, response.Usage.StopReason = StopReasonLength, "MAX_TOKENS"
	events, err := projectKiroCompletedTextEvents(response, model, stamp, 1024, 1<<20)
	if err != nil || events[7].Reason != StopReasonLength || events[7].Message.StopReason != StopReasonLength || events[7].Message.UsageReported {
		t.Fatal("explicit length or unknown usage lost", err)
	}
	events[2].Partial.Content[0] = TextContent{Text: "edited"}
	if events[3].Partial.Content[0].(TextContent).Text != "first" || events[7].Message.Content[0].(TextContent).Text != "first" || response.Parts[0].Text != "first" {
		t.Fatal("snapshots alias")
	}
	events[7].Message.Content[0] = TextContent{Text: "final edited"}
	response.Parts[1].Text = "source edited"
	if events[6].Partial.Content[1].(TextContent).Text != " second" || events[3].Partial.Content[0].(TextContent).Text != "first" {
		t.Fatal("source or final mutation changed earlier events")
	}
}

func TestKiroBufferedTextEventsFailClosed(t *testing.T) {
	for _, mutate := range []func(*kiroOfflineResponse){
		func(r *kiroOfflineResponse) { r.Usage.StopReason = "" },
		func(r *kiroOfflineResponse) { r.StopReason = StopReasonError },
		func(r *kiroOfflineResponse) { r.Parts[1].Kind = "tool" },
		func(r *kiroOfflineResponse) { r.Parts[1].Kind = "reasoning" },
		func(r *kiroOfflineResponse) { r.Parts[1].Kind = "context_usage" },
		func(r *kiroOfflineResponse) { r.Parts[1].Signature = "opaque" },
		func(r *kiroOfflineResponse) { r.Usage.TokenCounts = map[string]int64{"outputTokens": 1} },
	} {
		response, model, stamp := kiroTextMessageFixture()
		mutate(&response)
		events, err := projectKiroCompletedTextEvents(response, model, stamp, 1024, 1<<20)
		if err == nil || events != nil {
			t.Fatal("unsupported evidence released partial events")
		}
	}
	for _, budget := range []int{-1, 0, 1, assistantMessageEventByteBudget + 1} {
		response, model, stamp := kiroTextMessageFixture()
		events, err := projectKiroCompletedTextEvents(response, model, stamp, 1024, budget)
		if err == nil || events != nil {
			t.Fatal("invalid budget accepted")
		}
	}
	response, model, stamp := kiroTextMessageFixture()
	// A budget that can pay the start but not all snapshots must release nothing.
	partial := AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID, Timestamp: stamp}
	budget := assistantEventBytes(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: partial})
	events, err := projectKiroCompletedTextEvents(response, model, stamp, 1024, budget)
	if err == nil || events != nil {
		t.Fatal("late budget failure returned start")
	}
}

// Independently authored scenario matching the upstream lifecycle/delta invariant,
// not its context-percentage completion heuristic or adjacent-block coalescing.
func TestKiroBufferedTextEventsFromSyntheticWire(t *testing.T) {
	wire := kiroOfflineJoin(
		kiroOfflineEvent("assistantResponseEvent", `{"content":"海"}`),
		kiroOfflineEvent("assistantResponseEvent", `{"content":"海!"}`),
		kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
	)
	response, err := collectKiroOfflineResponse(context.Background(), kiroFragmentReader{bytes.NewReader(wire)}, kiroOfflineTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, model, stamp := kiroTextMessageFixture()
	events, err := projectKiroCompletedTextEvents(response, model, stamp, 1024, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var deltas, completed strings.Builder
	terminal := 0
	for _, event := range events {
		if event.Type == AssistantMessageEventTextDelta {
			deltas.WriteString(event.Delta)
		}
		if event.Type == AssistantMessageEventDone {
			terminal++
			for _, block := range event.Message.Content {
				completed.WriteString(block.(TextContent).Text)
			}
		}
	}
	if terminal != 1 || deltas.String() != "海海!" || completed.String() != "海海!" {
		t.Fatal("delta/final parity or terminal count changed")
	}
	// A truncated wire stream has no projection, even if it already carried text.
	failed, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire[:len(wire)-1]), kiroOfflineTestLimits())
	if err == nil {
		t.Fatal("truncated wire accepted")
	}
	if events, err := projectKiroCompletedTextEvents(failed, model, stamp, 1024, 1<<20); err == nil || events != nil {
		t.Fatal("failed stream exposed host events")
	}
}

func TestKiroBufferedTextEventsCumulativeBudget(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	response.Parts = nil
	for i := 0; i < 12; i++ {
		response.Parts = append(response.Parts, kiroOfflinePart{Kind: "text", Text: strings.Repeat("x", 128)})
	}
	events, err := projectKiroCompletedTextEvents(response, model, stamp, 4096, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	largest, total := 0, 0
	for _, event := range events {
		cost := assistantEventBytes(event)
		if cost > largest {
			largest = cost
		}
		total += cost
	}
	if total <= largest*2 {
		t.Fatal("fixture does not exercise amplification")
	}
	// Allow more than every individual event needs, but not the accumulated snapshots.
	limited, err := projectKiroCompletedTextEvents(response, model, stamp, 4096, largest*2)
	if err == nil || limited != nil {
		t.Fatal("cumulative snapshot budget ignored")
	}
}
