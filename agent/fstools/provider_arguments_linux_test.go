//go:build linux && !android

package fstools_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/fstools"
)

// A Responses function call carries its arguments as a JSON string. The raw
// string is the only evidence that two path fields were present on the wire.
func TestResponsesAtomicReplaceValidatesWireArguments(t *testing.T) {
	for _, tc := range []struct {
		name, arguments string
		reject          bool
	}{
		{"duplicate path", `{"path":"first-private-target","path":"secret","content":"changed"}`, true},
		{"one path", `{"path":"first-private-target","content":"changed"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"first-private-target", "secret"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			tool, err := fstools.AtomicReplaceDefinition(root, fstools.ReplaceOptions{})
			if err != nil {
				t.Fatal(err)
			}

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" {
					t.Errorf("unexpected provider endpoint %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				item := map[string]any{"type": "message", "id": "msg_done", "content": []any{map[string]string{"type": "output_text", "text": "done"}}}
				if requests.Add(1) == 1 {
					item = map[string]any{"type": "function_call", "id": "fc_replace", "call_id": "call_replace", "name": tool.Name, "arguments": tc.arguments, "status": "completed"}
				}
				data, err := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{item}}})
				if err != nil {
					t.Error(err)
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", data)
			}))
			defer server.Close()
			runner, err := agent.NewRunner(agent.AgentDefinition{
				DefaultModel: agent.ModelRef{Provider: "openai", Model: "gpt-5.4", ProviderConfig: agent.ProviderConfig{BaseURL: server.URL, APIKey: "fixture"}},
				Tools:        []agent.ToolDefinition{tool}, MaxTurns: 2,
			})
			if err != nil {
				t.Fatal(err)
			}
			stream, handle, err := runner.RunWithReceipts(t.Context(), agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("update the file")}, agent.LoopHooks{}, 1)
			if err != nil {
				t.Fatal(err)
			}
			for range stream.Events() {
			}
			snapshot, err := stream.Wait()
			if !tc.reject && err != nil {
				t.Fatal(err)
			}
			if err := handle.Wait(t.Context()); err != nil {
				t.Fatal(err)
			}
			var result *agent.ToolResultPayload
			var reply *agent.Message
			for _, m := range snapshot.Messages {
				if m.Role == agent.RoleTool {
					result = m.ToolResult
				} else if m.Role == agent.RoleAssistant {
					reply = &m
				}
			}
			facts := handle.Snapshot()
			if tc.reject {
				if requests.Load() != 1 || result != nil || reply == nil || reply.StopReason != agent.StopReasonError || len(reply.ToolCalls) != 0 || !strings.Contains(strings.ToLower(reply.ErrorMessage), "argument") {
					t.Errorf("expected a rejected model reply and no tool call, requests=%d reply=%+v result=%+v", requests.Load(), reply, result)
				}
				saved, err := json.Marshal(snapshot)
				if err != nil || bytes.Contains(saved, []byte("first-private-target")) {
					t.Errorf("discarded argument leaked into snapshot or snapshot could not be serialized: %v", err)
				}
				if len(facts) != 0 {
					t.Errorf("rejected call entered executor, receipts=%d committed=%t", len(facts), facts[0].Committed())
				}
			} else if requests.Load() != 2 || result == nil || result.ToolName != tool.Name || result.IsError || len(facts) != 1 || !facts[0].Completed() || !facts[0].Committed() {
				t.Errorf("valid replacement failed: result=%+v receipts=%v", result, facts)
			}
			for _, name := range []string{"first-private-target", "secret"} {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				want := name
				if !tc.reject && name == "first-private-target" {
					want = "changed"
				}
				if string(data) != want {
					t.Errorf("%s contains %q, want %q", name, data, want)
				}
			}
		})
	}
}
