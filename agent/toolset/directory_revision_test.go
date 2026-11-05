package toolset

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Icatme/pi-go/agent"
	managed "github.com/Icatme/pi-go/mcp"
)

func TestDirectoryRevisionTracksFrozenScope(t *testing.T) {
	f := newFixture(t, managed.Direct, nil)
	ts, err := New(f.manager, f.sandbox, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var previous directorySnapshot
	var old []entry
	for _, scope := range []managed.Scope{{Identity: "account-a", AuthEpoch: 1}, {Identity: "account-a", AuthEpoch: 2}, {Identity: "account-b", AuthEpoch: 2}} {
		if err := f.manager.SetScope(scope); err != nil {
			t.Fatal(err)
		}
		if old != nil {
			if !errors.Is(previous.check(f.manager), managed.ErrStale) {
				t.Fatal("old frozen scope remained current")
			}
			before := f.requests.Load()
			for _, value := range old {
				if value.binding.Namespace != "issues" {
					continue
				}
				_, err := value.binding.Tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"id": "abc"}})
				var failure *agent.ToolExecutionError
				if !errors.As(err, &failure) {
					t.Fatalf("old executor accepted scope drift: %v", err)
				}
				assertCatalogRejected(t, err, failure.Execution, f.writes.Load())
			}
			if f.requests.Load() != before {
				t.Fatal("stale executor performed MCP I/O")
			}
		}
		values, _, frozen, err := ts.directory(t.Context(), true, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if frozen.scope != scope || len(values) != 7 {
			t.Fatalf("unexpected directory: scope=%+v entries=%d", frozen.scope, len(values))
		}
		for _, value := range values {
			want := scopeRevision(scope)
			if value.binding.Namespace == "issues" {
				for _, catalog := range frozen.catalogs {
					want = fmt.Sprintf("%s/%d/%d", want, catalog.Generation, catalog.Revision)
				}
			}
			if value.binding.Tool.Revision != want {
				t.Fatalf("%s revision=%q want=%q", value.binding.ExportedName(), value.binding.Tool.Revision, want)
			}
		}
		previous, old = frozen, values
	}
}

func TestScopeDriftDuringPermissionBlocksDirectoryExecutor(t *testing.T) {
	for _, scope := range []managed.Scope{{Identity: "account-a", AuthEpoch: 2}, {Identity: "account-b", AuthEpoch: 1}} {
		t.Run(fmt.Sprintf("%s/%d", scope.Identity, scope.AuthEpoch), func(t *testing.T) {
			f := newFixture(t, managed.Direct, mixedRules())
			ts, err := New(f.manager, f.sandbox, Options{})
			if err != nil {
				t.Fatal(err)
			}
			tools, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
			if err != nil {
				t.Fatal(err)
			}
			selected := findTool(t, tools, "mcp__issues__direct_issue")
			checks := 0
			_, err = selected.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"id": "abc"}, CheckPermission: func(context.Context) error {
				checks++
				return f.manager.SetScope(scope)
			}})
			if checks != 1 || err == nil || f.writes.Load() != 0 {
				t.Fatalf("scope drift bypassed permission guard: checks=%d writes=%d err=%v", checks, f.writes.Load(), err)
			}
		})
	}
}
