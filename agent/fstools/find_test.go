package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
)

func findFixture(t *testing.T, options FindOptions) (string, *os.Root, agent.ToolDefinition) {
	t.Helper()
	dir, root, _ := fixture(t, Options{})
	tool, err := FindDefinition(root, options)
	if err != nil {
		t.Fatal(err)
	}
	return dir, root, tool
}
func findFile(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("contents are not searched"), 0600); err != nil {
		t.Fatal(err)
	}
}
func findDir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0700); err != nil {
		t.Fatal(err)
	}
}
func findCall(ctx context.Context, tool agent.ToolDefinition, p, pattern string) agent.ToolCallOutcome {
	raw, _ := json.Marshal(findArguments{Path: p, Pattern: pattern})
	return agent.RunToolCall(ctx, agent.ToolCall{ID: "find", Name: "find", Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
}
func findDecoded(t *testing.T, outcome agent.ToolCallOutcome) findOutput {
	t.Helper()
	if outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	var out findOutput
	if err := json.Unmarshal(outcome.Result.StructuredContent, &out); err != nil {
		t.Fatal(err)
	}
	if len(outcome.Result.Content) != 1 || outcome.Result.Content[0].Text != string(outcome.Result.StructuredContent) {
		t.Fatal("inconsistent text and structured output")
	}
	return out
}
func TestFindOptInAndValidation(t *testing.T) {
	if !supported {
		if _, err := FindDefinition(nil, FindOptions{}); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatal(err)
		}
		return
	}
	if _, err := FindDefinition(nil, FindOptions{}); err == nil {
		t.Fatal("nil root accepted")
	}
	_, root, tool := findFixture(t, FindOptions{})
	for _, options := range []FindOptions{{MaxVisitedEntries: -1}, {MaxVisitedEntries: 100001}, {MaxDepth: -1}, {MaxDepth: 65}, {MaxMatches: -1}, {MaxMatches: 4097}, {MaxResultBytes: 255}, {MaxResultBytes: (1 << 20) + 1}} {
		if _, err := FindDefinition(root, options); err == nil {
			t.Fatal("accepted limits", options)
		}
	}
	existing, err := Definitions(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) != 1 || existing[0].Name != "ls" {
		t.Fatal("existing caller gained find authority")
	}
	for _, args := range []any{nil, findArguments{Path: "../", Pattern: "*"}, findArguments{Path: ".", Pattern: "["}, findArguments{Path: ".", Pattern: "a/b"}, findArguments{Path: ".", Pattern: ""}, findArguments{Path: ".", Pattern: "a\x00b"}, findArguments{Path: ".", Pattern: strings.Repeat("a", 257)}, map[string]any{"path": ".", "pattern": "*", "limit": 999}} {
		checked := 0
		_, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: args, CheckPermission: func(context.Context) error { checked++; return nil }})
		if err == nil || checked != 0 {
			t.Fatalf("invalid args entered permission/filesystem path: %#v %v checks=%d", args, err, checked)
		}
	}
	root.Close()
	if _, err := FindDefinition(root, FindOptions{}); err != nil {
		t.Fatal("constructor performed I/O")
	}
	if got := findCall(t.Context(), tool, ".", "*"); got.Err == nil {
		t.Fatal("closed root accepted")
	}
}
func TestFindDescendsThroughNonmatchingDirectories(t *testing.T) {
	dir, _, tool := findFixture(t, FindOptions{})
	findDir(t, filepath.Join(dir, "unmatched", "nested"))
	findFile(t, filepath.Join(dir, "unmatched", "nested", "雪.go"))
	findFile(t, filepath.Join(dir, "outside.txt"))
	out := findDecoded(t, findCall(t.Context(), tool, ".", "*.go"))
	if out.Truncated || out.Visited != 4 || len(out.Entries) != 1 || out.Entries[0].Path != "unmatched/nested/雪.go" || out.Entries[0].Type != "file" {
		t.Fatal(out)
	}
	out = findDecoded(t, findCall(t.Context(), tool, "./unmatched", "?.go"))
	if out.Truncated || len(out.Entries) != 1 || out.Entries[0].Path != "unmatched/nested/雪.go" {
		t.Fatal(out)
	}
	out = findDecoded(t, findCall(t.Context(), tool, ".", "*.GO"))
	if len(out.Entries) != 0 || out.Truncated {
		t.Fatal(out)
	}
}
func TestFindDepthLimit(t *testing.T) {
	dir, root, tool := findFixture(t, FindOptions{MaxDepth: 1})
	findDir(t, filepath.Join(dir, "first", "nested"))
	findFile(t, filepath.Join(dir, "first", "nested", "leaf.go"))
	out := findDecoded(t, findCall(t.Context(), tool, ".", "*"))
	if out.Visited != 1 || len(out.Entries) != 1 || out.Entries[0].Path != "first" || !out.Truncated || !slices.Contains(out.Limits, "max_depth") {
		t.Fatal(out)
	}
	tool, err := FindDefinition(root, FindOptions{MaxDepth: 2})
	if err != nil {
		t.Fatal(err)
	}
	out = findDecoded(t, findCall(t.Context(), tool, ".", "nested"))
	if out.Visited != 2 || len(out.Entries) != 1 || out.Entries[0].Path != "first/nested" || !out.Truncated {
		t.Fatal(out)
	}
}
func TestFindBudgets(t *testing.T) {
	t.Run("nonmatches count", func(t *testing.T) {
		dir, _, tool := findFixture(t, FindOptions{MaxVisitedEntries: 3})
		for i := range 10 {
			findFile(t, filepath.Join(dir, fmt.Sprint(i)))
		}
		out := findDecoded(t, findCall(t.Context(), tool, ".", "missing"))
		if out.Visited != 3 || len(out.Entries) != 0 || !out.Truncated || !slices.Contains(out.Limits, "max_visited_entries") {
			t.Fatal(out)
		}
	})
	t.Run("exact visited conservatively truncated", func(t *testing.T) {
		dir, _, tool := findFixture(t, FindOptions{MaxVisitedEntries: 1})
		findFile(t, filepath.Join(dir, "one"))
		out := findDecoded(t, findCall(t.Context(), tool, ".", "missing"))
		if out.Visited != 1 || !out.Truncated {
			t.Fatal(out)
		}
	})
	t.Run("exact match conservatively truncated", func(t *testing.T) {
		dir, _, tool := findFixture(t, FindOptions{MaxMatches: 1})
		findFile(t, filepath.Join(dir, "one"))
		out := findDecoded(t, findCall(t.Context(), tool, ".", "*"))
		if len(out.Entries) != 1 || !out.Truncated || !slices.Contains(out.Limits, "max_matches") {
			t.Fatal(out)
		}
	})
	t.Run("result escaping", func(t *testing.T) {
		dir, _, tool := findFixture(t, FindOptions{MaxResultBytes: 256})
		findFile(t, filepath.Join(dir, strings.Repeat("<", 100)))
		got := findCall(t.Context(), tool, ".", "*")
		out := findDecoded(t, got)
		if len(got.Result.StructuredContent) > 256 || len(out.Entries) != 0 || !slices.Contains(out.Limits, "max_result_bytes") {
			t.Fatal(out)
		}
	})
	t.Run("no remaining budget opens no child", func(t *testing.T) {
		dir, _, tool := findFixture(t, FindOptions{MaxVisitedEntries: 1})
		findDir(t, filepath.Join(dir, "child"))
		checks := 0
		result, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: findArguments{Path: ".", Pattern: "missing"}, CheckPermission: func(context.Context) error { checks++; return nil }})
		if err != nil || checks != 3 {
			t.Fatalf("checks=%d err=%v result=%+v", checks, err, result)
		}
	})
}
func TestFindRevocationCancellationAndSafeErrors(t *testing.T) {
	dir, _, tool := findFixture(t, FindOptions{})
	findFile(t, filepath.Join(dir, "one.go"))
	denied := errors.New("revoked")
	for at := 1; at <= 4; at++ {
		checks := 0
		result, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: findArguments{Path: ".", Pattern: "*"}, CheckPermission: func(context.Context) error {
			checks++
			if checks == at {
				return denied
			}
			return nil
		}})
		if !errors.Is(err, denied) || len(result.Content) != 0 {
			t.Fatalf("check %d: %v %+v", at, err, result)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := findCall(ctx, tool, ".", "*"); !errors.Is(got.Err, context.Canceled) {
		t.Fatal(got.Err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	checks := 0
	result, err := tool.Execute(ctx, agent.ToolExecutionContext{Args: findArguments{Path: ".", Pattern: "*"}, CheckPermission: func(context.Context) error {
		checks++
		if checks == 4 {
			cancel()
		}
		return nil
	}})
	if !errors.Is(err, context.Canceled) || len(result.Content) != 0 {
		t.Fatal("late cancellation published results", err)
	}
	got := findCall(t.Context(), tool, "missing", "*")
	if got.Err == nil || strings.Contains(got.Err.Error(), dir) {
		t.Fatal("unsafe error", got.Err)
	}
}
func TestFindFilenameHandling(t *testing.T) {
	dir, _, tool := findFixture(t, FindOptions{})
	name := "quote\"\n雪<:back\\slash"
	findFile(t, filepath.Join(dir, name))
	out := findDecoded(t, findCall(t.Context(), tool, ".", "*"))
	if len(out.Entries) != 1 || out.Entries[0].Path != name {
		t.Fatal(out)
	}
	findFile(t, filepath.Join(dir, string([]byte{255})))
	if got := findCall(t.Context(), tool, ".", "*"); got.Err == nil {
		t.Fatal("invalid UTF8 filename silently replaced")
	}
}
func TestFindConcurrentInvocations(t *testing.T) {
	dir, _, tool := findFixture(t, FindOptions{})
	findDir(t, filepath.Join(dir, "nested"))
	findFile(t, filepath.Join(dir, "nested", "one.go"))
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			out := findCall(t.Context(), tool, ".", "*.go")
			if out.Err != nil {
				t.Error(out.Err)
			}
		})
	}
	wg.Wait()
}
func TestFindCodemodePermissionAndLimits(t *testing.T) {
	dir, _, leaf := findFixture(t, FindOptions{MaxMatches: 1})
	findFile(t, filepath.Join(dir, "one.go"))
	sandbox, err := codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sandbox.Close(context.Background()) })
	binding := codemodetool.Native(leaf, "workspace")
	code, err := codemodetool.New(sandbox, []codemodetool.Binding{binding}, codemodetool.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, deny := range []bool{false, true} {
		checks := 0
		args, _ := json.Marshal(map[string]any{"code": `text(await tools.` + binding.ExportedName() + `({path:".",pattern:"*.go"}));`})
		got := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: code.Name, Arguments: args}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{code}, CheckToolPermission: func(_ context.Context, call agent.BeforeToolCallContext) error {
			if call.ToolCall.Name != code.Name {
				checks++
				if deny {
					return errors.New("denied")
				}
			}
			return nil
		}})
		text := ""
		for _, p := range got.Result.Content {
			text += p.Text
		}
		if checks == 0 {
			t.Fatal("permission bypass")
		}
		if deny {
			if got.Err == nil && !got.IsError {
				t.Fatal("denied call succeeded")
			}
			if strings.Contains(text, "one.go") {
				t.Fatal("denied result leaked")
			}
		} else if got.Err != nil || !strings.Contains(text, "one.go") || !strings.Contains(text, "max_matches") {
			t.Fatalf("%s: %v", text, got.Err)
		}
	}
}
