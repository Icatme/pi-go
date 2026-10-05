package codemodetool

import (
	"context"
	"fmt"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/codemode"
)

func TestAliasIdentityCompatibility(t *testing.T) {
	execute := func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{}, nil
	}
	tests := []struct {
		binding Binding
		prefix  string
		alias   string
	}{
		{Native(agent.ToolDefinition{Name: "read", Execute: execute}, "local"), "", "read"},
		{Native(agent.ToolDefinition{Name: "path/read", Execute: execute}, "local"), "", "path_read__9c8163afb7b2"},
		{MCP(agent.ToolDefinition{Name: "read", Execute: execute}, "server"), "mcp__server__", "mcp__server__read"},
		{MCP(agent.ToolDefinition{Name: "read", Execute: execute}, "notes-workspace"), "mcp__notes_worksp_99c6c63f3861__", "mcp__notes_worksp_99c6c63f3861__read"},
		{MCP(agent.ToolDefinition{Name: "read-note", Execute: execute}, "notes-workspace"), "mcp__notes_worksp_99c6c63f3861__", "mcp__notes_worksp_99c6c63f3861__read_note__157f4f43e882"},
		{MCP(agent.ToolDefinition{Name: "read", Execute: execute}, "notes_workspace"), "mcp__notes_workspace__", "mcp__notes_workspace__read"},
	}
	bindings := make([]Binding, len(tests))
	for i, test := range tests {
		bindings[i] = test.binding
		if got := test.binding.NamespacePrefix(); got != test.prefix {
			t.Errorf("prefix for %q: got %q, want %q", test.binding.Namespace, got, test.prefix)
		}
		if got := test.binding.ExportedName(); got != test.alias {
			t.Errorf("public alias for %q: got %q, want %q", test.binding.Tool.Name, got, test.alias)
		}
	}
	sandbox := newSandbox(t)
	tool, err := New(sandbox, bindings, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i, test := range tests {
		if got := tool.ChildTools[i].Name; got != test.alias {
			t.Errorf("frozen alias %d: got %q, want %q", i, got, test.alias)
		}
	}

	// The same public binding value may be edited and used for a new catalog.
	// Prefix reuse must not make that catalog retain the old namespace identity.
	bindings[3].Namespace = "server"
	bindings[3].Tool.Name = "write"
	const changed = "mcp__server__write"
	if got := bindings[3].ExportedName(); got != changed {
		t.Fatalf("changed public binding: got %q, want %q", got, changed)
	}
	updated, err := New(sandbox, bindings, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ChildTools[3].Name != changed || tool.ChildTools[3].Name != tests[3].alias {
		t.Fatalf("catalog identity changed or became stale: original=%q updated=%q", tool.ChildTools[3].Name, updated.ChildTools[3].Name)
	}
}

func BenchmarkNewAliasNamespaces(b *testing.B) {
	sandbox, err := codemode.NewSandbox(b.Context(), codemode.DefaultConfig())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = sandbox.Close(context.Background()) })
	for _, namespace := range []string{"server", "notes-workspace"} {
		for _, count := range []int{1, 256} {
			b.Run(fmt.Sprintf("%s/%d", namespace, count), func(b *testing.B) {
				bindings := make([]Binding, count)
				for i := range bindings {
					bindings[i] = MCP(agent.ToolDefinition{
						Name: fmt.Sprintf("read_%d", i),
						Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
							return agent.ToolResult{}, nil
						},
					}, namespace)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := New(sandbox, bindings, Options{}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
