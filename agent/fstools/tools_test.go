package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func fixture(t *testing.T, options Options) (string, *os.Root, agent.ToolDefinition) {
	t.Helper()
	if !supported {
		t.Skip("directory tools not supported on this OS")
	}
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	tools, err := Definitions(root, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "ls" {
		t.Fatalf("tools: %+v", tools)
	}
	return dir, root, tools[0]
}
func call(ctx context.Context, tool agent.ToolDefinition, p string) agent.ToolCallOutcome {
	raw, _ := json.Marshal(map[string]any{"path": p})
	return agent.RunToolCall(ctx, agent.ToolCall{ID: "list", Name: "ls", Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
}
func decoded(t *testing.T, result agent.ToolCallOutcome) output {
	t.Helper()
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	var out output
	if err := json.Unmarshal(result.Result.StructuredContent, &out); err != nil {
		t.Fatal(err)
	}
	if len(result.Result.Content) != 1 || result.Result.Content[0].Text != string(result.Result.StructuredContent) {
		t.Fatal("text and structured output differ")
	}
	return out
}
func TestDefinitionsValidation(t *testing.T) {
	if !supported {
		if _, err := Definitions(nil, Options{}); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatal(err)
		}
		return
	}
	if _, err := Definitions(nil, Options{}); err == nil {
		t.Fatal("nil root accepted")
	}
	_, root, _ := fixture(t, Options{})
	for _, o := range []Options{{MaxEntries: -1}, {MaxEntries: 4097}, {MaxResultBytes: -1}, {MaxResultBytes: 255}, {MaxResultBytes: (1 << 20) + 1}} {
		if _, err := Definitions(root, o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
	root.Close()
	// Factory is inert; closed handles fail only when invoked.
	tools, err := Definitions(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := call(t.Context(), tools[0], "."); got.Err == nil {
		t.Fatal("closed root read succeeded")
	}
}
func TestListEntriesAndEmpty(t *testing.T) {
	dir, _, tool := fixture(t, Options{})
	if out := decoded(t, call(t.Context(), tool, ".")); len(out.Entries) != 0 || out.Truncated {
		t.Fatal(out)
	}
	if err := os.WriteFile(filepath.Join(dir, "hello\n雪<.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("child", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	got := decoded(t, call(t.Context(), tool, "."))
	if got.Truncated || len(got.Entries) != 3 {
		t.Fatal(got)
	}
	kinds := map[string]string{}
	for _, e := range got.Entries {
		kinds[e.Name] = e.Type
	}
	if kinds["hello\n雪<.txt"] != "file" || kinds["child"] != "directory" || kinds["link"] != "symlink" {
		t.Fatal(kinds)
	}
	if got := decoded(t, call(t.Context(), tool, "./link/./")); len(got.Entries) != 0 {
		t.Fatal(got)
	}
}
func TestRejectInvalidAndEscapingPaths(t *testing.T) {
	dir, _, tool := fixture(t, Options{})
	outside := t.TempDir()
	for name, target := range map[string]string{"outside": outside, "absolute_inside": dir, "relative_out": "../" + filepath.Base(outside)} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"", "/", "../", "child/../", "a/../../b", "C:/x", `a\b`, "a\x00b", strings.Repeat("a", 4097), "outside", "outside/child", "absolute_inside", "relative_out", "missing"} {
		t.Run(fmt.Sprintf("%q", p), func(t *testing.T) {
			if got := call(t.Context(), tool, p); got.Err == nil {
				t.Fatal("accepted", p)
			}
		})
	}
	for _, raw := range []string{`null`, `[]`, `{"path":".","extra":true}`, `{"path":null}`, `{"path":1}`, `{"path":"\ud800"}`, `{"path":"."} {}`} {
		if _, err := tool.ParseArguments(agent.ToolCall{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": string([]byte{255})}}); err == nil {
		t.Fatal("invalid Unicode accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "regular"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := call(t.Context(), tool, "regular"); got.Err == nil {
		t.Fatal("regular file accepted")
	}
}
func TestBoundsAndEscaping(t *testing.T) {
	dir, root, tool := fixture(t, Options{MaxEntries: 2})
	for i := range 20 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	out := decoded(t, call(t.Context(), tool, "."))
	if !out.Truncated || len(out.Entries) != 2 {
		t.Fatal(out)
	}
	tools, err := Definitions(root, Options{MaxResultBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	got := call(t.Context(), tools[0], ".")
	out = decoded(t, got)
	if !out.Truncated || len(got.Result.StructuredContent) > 256 {
		t.Fatal(out)
	}
	dir, _, tool = fixture(t, Options{MaxResultBytes: 256})
	if err := os.WriteFile(filepath.Join(dir, strings.Repeat("<", 100)), nil, 0600); err != nil {
		t.Fatal(err)
	}
	got = call(t.Context(), tool, ".")
	out = decoded(t, got)
	if !out.Truncated || len(out.Entries) != 0 || len(got.Result.StructuredContent) > 256 {
		t.Fatal(out)
	}
}
func TestPermissionAndCancellation(t *testing.T) {
	dir, root, tool := fixture(t, Options{})
	os.WriteFile(filepath.Join(dir, "secret"), nil, 0600)
	denied := errors.New("revoked")
	for _, at := range []int{1, 2, 3, 4} {
		checks := 0
		result, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "."}, CheckPermission: func(context.Context) error {
			checks++
			if checks == at {
				return denied
			}
			return nil
		}})
		if !errors.Is(err, denied) || len(result.Content) != 0 {
			t.Fatalf("check %d: %+v %v", at, result, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := call(ctx, tool, "."); !errors.Is(got.Err, context.Canceled) {
		t.Fatal(got.Err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	_, err := tool.Execute(ctx, agent.ToolExecutionContext{Args: map[string]any{"path": "."}, CheckPermission: func(context.Context) error { cancel(); return nil }})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_, err = tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "."}, CheckPermission: func(context.Context) error { root.Close(); return nil }})
	if err == nil {
		t.Fatal("closed root accepted")
	}
}
func TestConcurrentListings(t *testing.T) {
	_, _, tool := fixture(t, Options{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if got := call(t.Context(), tool, "."); got.Err != nil {
				t.Error(got.Err)
			}
		})
	}
	wg.Wait()
}
func TestInvalidFilenameFailsWithoutLossyReplacement(t *testing.T) {
	dir, _, tool := fixture(t, Options{})
	if err := os.WriteFile(filepath.Join(dir, string([]byte{255})), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := call(t.Context(), tool, "."); got.Err == nil {
		t.Fatal("lossy filename returned")
	}
}

func TestRootRenameAndArgumentRevalidation(t *testing.T) {
	dir, _, tool := fixture(t, Options{})
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(moved) })
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wrong-root"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := decoded(t, call(t.Context(), tool, ".")); len(got.Entries) != 0 {
		t.Fatalf("followed replacement root: %+v", got)
	}
	for _, args := range []any{map[string]any{"path": "../"}, map[string]any{"path": ".", "extra": 1}, nil} {
		if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: args}); err == nil {
			t.Fatalf("direct Execute accepted %#v", args)
		}
	}
}

type emptyDirectoryRead struct {
	err       error
	requested int
}

func (r *emptyDirectoryRead) ReadDir(n int) ([]os.DirEntry, error) {
	r.requested = n
	return nil, r.err
}
func TestEmptyDirectoryReadIsNotAutomaticallyEOF(t *testing.T) {
	for _, cause := range []error{nil, io.EOF, os.ErrPermission} {
		reader := &emptyDirectoryRead{err: cause}
		entries, err := readOneEntry(reader)
		want := cause
		if want == nil {
			want = io.ErrNoProgress
		}
		if len(entries) != 0 || !errors.Is(err, want) || reader.requested != 1 {
			t.Fatalf("entries=%v error=%v requested=%d", entries, err, reader.requested)
		}
	}
}
