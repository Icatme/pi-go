package pigo

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAudit10106RepeatedForeignCallIDs(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	var messages []Message
	for _, name := range []string{"read", "edit"} {
		messages = append(messages, AssistantMessage{Provider: "google", API: "google-generative-ai", Model: "gemini-fixture", StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "call_134460", Name: name, Arguments: map[string]any{"name": name}}}}, ToolResultMessage{ToolCallID: "call_134460", ToolName: name, Content: []ContentBlock{TextContent{Text: name + " result"}}})
	}
	request, err := buildOpenAIResponsesRequest(nil, model, Context{Messages: messages}, ProviderStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wire := request.Input
	seen := map[string]bool{}
	for _, item := range wire {
		if item["type"] == "function_call" {
			id := item["call_id"].(string)
			if seen[id] {
				t.Fatalf("duplicate call ID: %s; wire=%v", id, wire)
			}
			seen[id] = true
		}
	}
}

func TestForeignResponsesCallIDBoundaries(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	pair := func(id, name string) []Message {
		return []Message{AssistantMessage{Provider: "google", API: "google-generative-ai", Model: "fixture", StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: id, Name: name, Arguments: map[string]any{}}}}, ToolResultMessage{ToolCallID: id, ToolName: name, Content: []ContentBlock{TextContent{Text: name}}}}
	}
	base := append(pair("repeat", "first"), pair("repeat", "second")...)
	t.Run("stable pairs avoid future ID and preserve history", func(t *testing.T) {
		source := append(cloneMessages(base), pair("call_pigo_replay_1", "third")...)
		before := mustJSON(source)
		out, err := uniqueForeignResponsesCallIDs(model, source, "")
		if err != nil {
			t.Fatal(err)
		}
		again, err := uniqueForeignResponsesCallIDs(model, source, "")
		if err != nil {
			t.Fatal(err)
		}
		if before != mustJSON(source) || mustJSON(out) != mustJSON(again) {
			t.Fatal("mutation or nondeterministic")
		}
		id := out[2].(AssistantMessage).Content[0].(ToolCall).ID
		if id != "call_pigo_replay_2" || out[3].(ToolResultMessage).ToolCallID != id {
			t.Fatal(out)
		}
	})
	for _, tc := range []struct {
		name     string
		alter    func([]Message) []Message
		previous string
	}{
		{"previous response", func(m []Message) []Message { return m }, "resp_x"},
		{"missing result", func(m []Message) []Message { return m[:3] }, ""},
		{"duplicate result", func(m []Message) []Message { return append(m, m[3]) }, ""},
		{"mismatched name", func(m []Message) []Message { r := m[3].(ToolResultMessage); r.ToolName = "wrong"; m[3] = r; return m }, ""},
		{"aborted", func(m []Message) []Message {
			a := m[2].(AssistantMessage)
			a.StopReason = StopReasonAborted
			m[2] = a
			return m
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uniqueForeignResponsesCallIDs(model, tc.alter(cloneMessages(base)), tc.previous)
			if err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	t.Run("native IDs untouched", func(t *testing.T) {
		src := cloneMessages(base)
		for _, i := range []int{0, 2} {
			a := src[i].(AssistantMessage)
			a.API = model.API
			a.Provider = model.Provider
			a.Model = model.ID
			src[i] = a
		}
		out, err := uniqueForeignResponsesCallIDs(model, src, "resp_x")
		if err != nil || mustJSON(out) != mustJSON(src) {
			t.Fatal(out, err)
		}
	})
	t.Run("unique foreign previous allowed", func(t *testing.T) {
		src := pair("one", "first")
		out, err := uniqueForeignResponsesCallIDs(model, src, "resp_x")
		if err != nil || mustJSON(out) != mustJSON(src) {
			t.Fatal(out, err)
		}
	})
}

func TestForeignResponsesGeneratedIDsReserveOrphanResults(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	var source []Message
	for i := 0; i < 2; i++ {
		source = append(source, AssistantMessage{API: "google-generative-ai", StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "repeat", Name: "same", Arguments: map[string]any{}}}}, ToolResultMessage{ToolCallID: "repeat", ToolName: "same"})
	}
	source = append(source, ToolResultMessage{ToolCallID: "call_pigo_replay_1", ToolName: "unrelated"})
	out, err := uniqueForeignResponsesCallIDs(model, source, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := out[2].(AssistantMessage).Content[0].(ToolCall).ID; got != "call_pigo_replay_2" {
		t.Fatal(got)
	}
}

func TestForeignResponsesRejectSameRoundNormalizedCollision(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	source := []Message{AssistantMessage{API: "google-generative-ai", StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "x?y", Name: "a"}, ToolCall{ID: "x!y", Name: "b"}}}, ToolResultMessage{ToolCallID: "x?y", ToolName: "a"}, ToolResultMessage{ToolCallID: "x!y", ToolName: "b"}}
	if _, err := uniqueForeignResponsesCallIDs(model, source, ""); err == nil {
		t.Fatal("ambiguous normalized round accepted")
	}
}
func TestForeignResponsesCustomAndFunctionPairs(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	var source []Message
	for _, name := range []string{"code", "read"} {
		source = append(source, AssistantMessage{API: "google-generative-ai", StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "repeat", Name: name, Arguments: map[string]any{"code": "text(1)"}}}}, ToolResultMessage{ToolCallID: "repeat", ToolName: name, Content: []ContentBlock{TextContent{Text: name}}})
	}
	req, err := buildOpenAIResponsesRequest(map[string]string{"code": "code"}, model, Context{Messages: source}, ProviderStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"custom_tool_call", "custom_tool_call_output", "function_call", "function_call_output"}
	if len(req.Input) != 4 {
		t.Fatal(req.Input)
	}
	for i, kind := range want {
		if req.Input[i]["type"] != kind {
			t.Fatal(req.Input)
		}
	}
	if req.Input[0]["call_id"] != req.Input[1]["call_id"] || req.Input[2]["call_id"] != req.Input[3]["call_id"] || req.Input[0]["call_id"] == req.Input[2]["call_id"] {
		t.Fatal(req.Input)
	}
}

func TestForeignResponsesRejectBeforeHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.Error(w, "unexpected", 500) }))
	defer server.Close()
	model := *GetModel("openai", "gpt-6-astra")
	model.BaseURL = server.URL
	var source []Message
	for i := 0; i < 2; i++ {
		source = append(source, AssistantMessage{API: "google-generative-ai", StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "repeat", Name: "read"}}}, ToolResultMessage{ToolCallID: "repeat", ToolName: "read"})
	}
	result := Complete(model, Context{Messages: source}, ProviderStreamOptions{APIKey: "fixture", PreviousResponseID: "resp_x"})
	if result.StopReason != StopReasonError || requests.Load() != 0 {
		t.Fatal(result, requests.Load())
	}
}

func TestForeignResponsesParallelDistinctResultsOutOfOrder(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	var source []Message
	for i := 0; i < 2; i++ {
		source = append(source, AssistantMessage{API: "google-generative-ai", StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "a", Name: "read"}, ToolCall{ID: "b", Name: "edit"}}}, ToolResultMessage{ToolCallID: "b", ToolName: "edit"}, ToolResultMessage{ToolCallID: "a", ToolName: "read"})
	}
	out, err := uniqueForeignResponsesCallIDs(model, source, "")
	if err != nil {
		t.Fatal(err)
	}
	calls := out[3].(AssistantMessage).Content
	if calls[0].(ToolCall).ID != out[5].(ToolResultMessage).ToolCallID || calls[1].(ToolCall).ID != out[4].(ToolResultMessage).ToolCallID {
		t.Fatal(out)
	}
}
