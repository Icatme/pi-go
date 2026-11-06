//go:build linux && !android

package fstools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestReplaceCleansStagingAfterTypedNilPermissionPanic(t *testing.T) {
	dir, _, tool := replaceFixture(t, ReplaceOptions{})
	path := filepath.Join(dir, "original")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	call := agent.ToolCall{ID: "replace", Name: tool.Name, Arguments: json.RawMessage(`{"path":"original","content":"private new data"}`)}
	out, handle, err := agent.RunToolCallWithReceipts(t.Context(), call, agent.RunToolCallOptions{
		Tools: []agent.ToolDefinition{tool}, CheckToolPermission: func(context.Context, agent.BeforeToolCallContext) error {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".pigo-replace-") {
					panic((*agent.ToolExecutionError)(nil))
				}
			}
			return nil
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "original" {
		t.Fatalf("staging remained after panic: entries=%v err=%v", entries, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "old" {
		t.Fatalf("destination=%q err=%v", content, err)
	}
	if out.Failure == nil || out.Failure.Reason != "replace_not_published" {
		t.Fatalf("wrong publication failure: %+v", out.Failure)
	}
}
