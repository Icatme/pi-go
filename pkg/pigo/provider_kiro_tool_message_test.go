package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func kiroToolMessageFixture() kiroOfflineResponse {
	return kiroOfflineResponse{StopReason: StopReasonToolUse, Usage: kiroMetadataSnapshot{StopReason: "TOOL_USE"}, Parts: []kiroOfflinePart{
		{Kind: "text", Text: "lookup"}, {Kind: "tool", Tool: &kiroAssembledTool{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"id":9007199254740993}`)}},
	}}
}

func TestKiroCompletedToolsPrecisionReplayAndOwnership(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	r := kiroToolMessageFixture()
	r.Usage.TokenCounts = kiroCompleteCounts()
	m, err := projectKiroCompletedTools(r, model, stamp, []string{"lookup"}, 4096, 4)
	if err != nil || len(m.Content) != 2 || m.StopReason != StopReasonToolUse || !m.UsageReported || m.Usage.TotalTokens != 99 {
		t.Fatalf("projection: %#v %v", m, err)
	}
	call := m.Content[1].(ToolCall)
	if call.ID != "call-1" || call.Name != "lookup" || call.Arguments["id"] != json.Number("9007199254740993") || call.RawArguments != `{"id":9007199254740993}` {
		t.Fatal("wire evidence changed")
	}
	ctx, schemas := kiroToolProjectionFixture()
	ctx.Messages = []Message{UserMessage{Content: "lookup"}, m, ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: []ContentBlock{TextContent{Text: "synthetic"}}}}
	next, err := projectKiroToolConversation(ctx, model.ID, schemas, 10, 4, 4096)
	if err != nil || string(next.History[1].Assistant.Calls[0].Input) != call.RawArguments {
		t.Fatalf("replay: %v", err)
	}
	call.Arguments["id"] = "changed"
	r.Parts[1].Tool.Arguments[0] = '['
	if call.RawArguments != `{"id":9007199254740993}` {
		t.Fatal("raw aliased")
	}
}

func TestKiroCompletedToolsRejectsAllPartialEvidence(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	for i, change := range []func(*kiroOfflineResponse){
		func(r *kiroOfflineResponse) { r.StopReason = StopReasonLength },
		func(r *kiroOfflineResponse) { r.Usage.StopReason = "END_TURN" },
		func(r *kiroOfflineResponse) { r.Usage.TokenCounts = map[string]int64{"outputTokens": 1} },
		func(r *kiroOfflineResponse) { r.Parts = r.Parts[:1] },
		func(r *kiroOfflineResponse) { r.Parts[1].Tool.Name = "not-allowed" },
		func(r *kiroOfflineResponse) { r.Parts = append(r.Parts, r.Parts[1]) },
		func(r *kiroOfflineResponse) { r.Parts[1].Tool.ID = "bad id" },
		func(r *kiroOfflineResponse) { r.Parts[1].Tool.Arguments = json.RawMessage(`{"a":1,"a":2}`) },
		func(r *kiroOfflineResponse) { r.Parts[1].Tool.Arguments = json.RawMessage(`[]`) },
		func(r *kiroOfflineResponse) { r.Parts[1].Text = "mixed" },
		func(r *kiroOfflineResponse) { r.Parts[1].Signature = "signature" },
		func(r *kiroOfflineResponse) { r.Parts[1].Tool = nil },
		func(r *kiroOfflineResponse) { r.Parts[0].Kind = "reasoning" },
		func(r *kiroOfflineResponse) { r.Parts[0].Kind = "context_usage" },
	} {
		r := kiroToolMessageFixture()
		change(&r)
		got, err := projectKiroCompletedTools(r, model, stamp, []string{"lookup"}, 4096, 4)
		if err == nil || !reflect.DeepEqual(got, AssistantMessage{}) {
			t.Fatalf("case %d leaked partial tool", i)
		}
	}
}

func TestKiroCompletedToolsBudgetsAndMultipleCalls(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	r := kiroToolMessageFixture()
	cost := len("lookup") + len("call-1") + len("lookup") + len(r.Parts[1].Tool.Arguments)
	if _, err := projectKiroCompletedTools(r, model, stamp, []string{"lookup"}, cost, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := projectKiroCompletedTools(r, model, stamp, []string{"lookup"}, cost-1, 1); err == nil || len(got.Content) != 0 {
		t.Fatal("budget bypass")
	}
	r.Parts = append(r.Parts, kiroOfflinePart{Kind: "tool", Tool: &kiroAssembledTool{ID: "call-2", Name: "lookup", Arguments: json.RawMessage("{}")}})
	if _, err := projectKiroCompletedTools(r, model, stamp, []string{"lookup"}, 4096, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := projectKiroCompletedTools(r, model, stamp, []string{"lookup"}, 4096, 1); err == nil {
		t.Fatal("tool limit")
	}
	for _, names := range [][]string{nil, {"lookup", "lookup"}, {" lookup"}} {
		if _, err := projectKiroCompletedTools(r, model, stamp, names, 4096, 2); err == nil {
			t.Fatal("invalid allowlist")
		}
	}
}

func TestKiroCompletedToolsOnlyAfterValidatedWire(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	tool := kiroOfflineEvent("toolUseEvent", `{"name":"lookup","toolUseId":"wire-1","input":"{}","stop":true}`)
	stop := kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`)
	for _, complete := range []bool{false, true} {
		wire := append([]byte(nil), tool...)
		if complete {
			wire = append(wire, stop...)
		}
		r, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits())
		if complete && err != nil {
			t.Fatal(err)
		}
		m, projectionErr := projectKiroCompletedTools(r, model, stamp, []string{"lookup"}, 4096, 1)
		if !complete && (err == nil || projectionErr == nil || len(m.Content) != 0) {
			t.Fatal("EOF authorized tool")
		}
		if complete && (projectionErr != nil || m.Content[0].(ToolCall).ID != "wire-1") {
			t.Fatal("completed tool not projected")
		}
	}
}

// History replay cannot represent text after tools without reordering it.
func TestKiroCompletedToolsRejectsWireTextAfterTool(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	for _, text := range []string{"after", ""} {
		wire := kiroOfflineJoin(
			kiroOfflineEvent("toolUseEvent", `{"name":"lookup","toolUseId":"wire-1","input":"{}","stop":true}`),
			kiroOfflineEvent("assistantResponseEvent", `{"content":"`+text+`"}`),
			kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`),
		)
		r, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits())
		if err != nil {
			t.Fatal(err)
		}
		got, err := projectKiroCompletedTools(r, model, stamp, []string{"lookup"}, 4096, 1)
		if err == nil || !reflect.DeepEqual(got, AssistantMessage{}) {
			t.Fatal("unreplayable order accepted")
		}
	}
}
