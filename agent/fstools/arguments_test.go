package fstools

import (
	"encoding/json"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestFilesystemToolsRejectAmbiguousArguments(t *testing.T) {
	_, root, _ := fixture(t, Options{})
	tools, err := WorkspaceWriteDefinitions(root, WorkspaceWriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ tool, raw string }{
		{"ls", `{"PATH":"."}`},
		{"read", `{"path":"public","pAth":"secret"}`},
		{"read", `{"path":"public","\u0070ath":"secret"}`},
		{"find", `{"path":".","Pattern":"*"}`},
		{"grep", `{"path":"file","pattern":"public","pattern":"secret"}`},
		{"create_file", `{"path":"file","CONTENT":""}`},
		{"atomic_replace", `{"path":"file","content":"first","content":"second"}`},
		{"preview_edit", `{"path":"file","OLD_TEXT":"x","new_text":""}`},
		{"edit", `{"path":"file","old_text":"x","New_Text":""}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			raw := json.RawMessage(tc.raw)
			var parsed map[string]any
			if err := json.Unmarshal(raw, &parsed); err != nil {
				t.Fatal(err)
			}
			for _, args := range []map[string]any{nil, parsed} {
				out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "call", Name: tc.tool, Arguments: raw, ParsedArgs: args}, agent.RunToolCallOptions{Tools: tools})
				if out.Failure == nil || out.Failure.Code != agent.ToolFailureArgumentInvalid || out.Execution.Local != agent.ToolLocalNotStarted {
					t.Fatalf("%s (has parsed map=%t): failure=%+v execution=%+v", raw, args != nil, out.Failure, out.Execution)
				}
			}
		})
	}
}

func TestFilesystemToolAcceptsAuthoredPath(t *testing.T) {
	_, _, tool := fixture(t, Options{})
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{"path":"original"}`)} {
		args, err := tool.ParseArguments(agent.ToolCall{Arguments: raw, ParsedArgs: map[string]any{"path": "edited"}})
		if err != nil || args != (arguments{Path: "edited"}) {
			t.Fatalf("authored path with raw %s: args=%+v err=%v", raw, args, err)
		}
	}
}
