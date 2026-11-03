package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func replaceFixture(t *testing.T, options ReplaceOptions) (string, *os.Root, agent.ToolDefinition) {
	t.Helper()
	dir, root, _ := fixture(t, Options{})
	tool, err := AtomicReplaceDefinition(root, options)
	if err != nil {
		t.Fatal(err)
	}
	return dir, root, tool
}
func TestReplaceFullContentAndConcurrentWriters(t *testing.T) {
	dir, root, tool := replaceFixture(t, ReplaceOptions{})
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "file"), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, value := range []string{strings.Repeat("a", 10000), strings.Repeat("雪", 3000)} {
		wg.Go(func() {
			r, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: createArgs("file", value)})
			if err != nil || r.Execution == nil || r.Execution.LocalEffect != agent.ToolLocalEffectCommittedReported {
				t.Errorf("%+v %v", r, err)
			}
		})
	}
	wg.Wait()
	got, err := os.ReadFile(filepath.Join(dir, "file"))
	if err != nil || (string(got) != strings.Repeat("a", 10000) && string(got) != strings.Repeat("雪", 3000)) {
		t.Fatalf("partial %q %v", got, err)
	}
	old, _ := os.ReadFile(filepath.Join(dir, "alias"))
	if string(old) != "old" {
		t.Fatal("changed hard link inode")
	}
	st, err := root.Stat("file")
	if err != nil || st.Mode().Perm()&^0600 != 0 {
		t.Fatal("mode was inherited")
	}
	for _, name := range []string{"new", "file"} {
		if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: createArgs(name, "")}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: createArgs("missing/file", "x")}); err == nil {
		t.Fatal("made parents")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatal("staging residue", entries)
	}
}
func TestReplaceValidationAndAuthority(t *testing.T) {
	if !supported {
		if _, err := AtomicReplaceDefinition(nil, ReplaceOptions{}); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatal(err)
		}
		return
	}
	if _, err := AtomicReplaceDefinition(nil, ReplaceOptions{}); err == nil {
		t.Fatal("nil root")
	}
	dir, root, tool := replaceFixture(t, ReplaceOptions{MaxBytes: 4})
	if _, err := AtomicReplaceDefinition(root, ReplaceOptions{MaxBytes: -1}); err == nil {
		t.Fatal("limit")
	}
	core, _ := Definitions(root, Options{})
	for _, def := range core {
		if def.Name == tool.Name {
			t.Fatal("enabled by default")
		}
	}
	for _, args := range []map[string]any{createArgs("../outside", "x"), createArgs("/abs", "x"), createArgs("a/", "x"), createArgs("file", "12345"), createArgs("file", "a\x00"), {"path": "file"}} {
		if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: args}); err == nil {
			t.Fatal("invalid accepted", args)
		}
	}
	for _, mode := range []string{"deny", "cancel"} {
		ctx, cancel := context.WithCancel(t.Context())
		in := agent.ToolExecutionContext{Args: createArgs("file", "x")}
		if mode == "cancel" {
			cancel()
		} else {
			in.CheckPermission = func(context.Context) error { return errors.New("secret") }
		}
		_, err := tool.Execute(ctx, in)
		cancel()
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("unauthorized write")
	}
}
func TestReplaceTransactionFaultsAndCommitFacts(t *testing.T) {
	cause := errors.New("private fault")
	for _, tt := range []struct {
		name            string
		f               fakeCreate
		committed, fail bool
	}{
		{name: "short writes", f: fakeCreate{short: true}, committed: true},
		{name: "write fail", f: fakeCreate{writeErr: cause}, fail: true},
		{name: "zero progress", f: fakeCreate{zero: true}, fail: true},
		{name: "file sync fail", f: fakeCreate{syncErr: cause}, fail: true},
		{name: "publication uncertain", f: fakeCreate{publishErr: cause}, fail: true},
		{name: "directory sync fail", f: fakeCreate{dirErr: cause}, committed: true, fail: true},
		{name: "close after commit", f: fakeCreate{closeErr: cause}, committed: true, fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.f
			r, err := publishFile(t.Context(), agent.ToolExecutionContext{}, &f, []byte("complete"), "replace")
			if (err != nil) != tt.fail || f.closed != 1 {
				t.Fatalf("%+v %v", f, err)
			}
			if tt.committed {
				if r.Execution == nil || r.Execution.LocalEffect != agent.ToolLocalEffectCommittedReported || !strings.Contains(string(r.StructuredContent), `"published":true`) {
					t.Fatalf("lost commit %+v %v", r, err)
				}
				if err != nil {
					var typed *agent.ToolExecutionError
					if !errors.As(err, &typed) || typed.Execution.LocalEffect != agent.ToolLocalEffectCommittedReported {
						t.Fatal("error lost commit")
					}
				}
			} else if len(r.Content) > 0 || len(r.StructuredContent) > 0 {
				t.Fatal("uncommitted success")
			}
			if (tt.name == "write fail" || tt.name == "zero progress" || tt.name == "file sync fail") && f.published != 0 {
				t.Fatal("published incomplete staging")
			}
			if tt.name == "publication uncertain" {
				var typed *agent.ToolExecutionError
				if f.published != 1 || !errors.As(err, &typed) || typed.Execution.LocalEffect != agent.ToolLocalEffectUnknown {
					t.Fatal("unknown publication misreported")
				}
			}
			if tt.name == "short writes" && string(f.data) != "complete" {
				t.Fatal("partial content")
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("private error leaked")
			}
		})
	}
	for _, mode := range []string{"deny before publish", "cancel after publish", "deny after publish", "panic after publish"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f := &fakeCreate{}
			checks := 0
			in := agent.ToolExecutionContext{CheckPermission: func(context.Context) error {
				checks++
				if mode == "deny before publish" && checks == 3 {
					return cause
				}
				if f.published > 0 {
					if mode == "deny after publish" {
						return cause
					}
					if mode == "panic after publish" {
						panic(cause)
					}
				}
				return nil
			}}
			if mode == "cancel after publish" {
				f.afterPublish = cancel
			}
			r, err := publishFile(ctx, in, f, []byte("x"), "replace")
			if err == nil || f.closed != 1 {
				t.Fatalf("%+v %v", f, err)
			}
			if mode == "deny before publish" {
				if f.published != 0 || r.Execution != nil {
					t.Fatal("published on denial")
				}
			} else {
				var typed *agent.ToolExecutionError
				if r.Execution == nil || r.Execution.LocalEffect != agent.ToolLocalEffectCommittedReported || !errors.As(err, &typed) || typed.Execution.LocalEffect != agent.ToolLocalEffectCommittedReported {
					t.Fatalf("commit disappeared %+v %v", r, err)
				}
			}
		})
	}
}
func TestReplaceCodemodeAndHookFailureKeepCommit(t *testing.T) {
	dir, _, leaf := replaceFixture(t, ReplaceOptions{})
	if err := os.WriteFile(filepath.Join(dir, "created"), []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
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
	raw, _ := json.Marshal(map[string]any{"code": `try{await tools.` + binding.ExportedName() + `({path:"created",content:"complete"})}catch(e){text(e.execution.local_effect)}`})
	checks := 0
	out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: code.Name, Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{code}, CheckToolPermission: func(_ context.Context, in agent.BeforeToolCallContext) error {
		if in.ToolCall.Name != code.Name {
			checks++
		}
		return nil
	}, AfterToolCall: func(_ context.Context, in agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		if in.ParentToolCallID != "" {
			return agent.AfterToolCallResult{}, errors.New("withheld")
		}
		return agent.AfterToolCallResult{}, nil
	}})
	got, err := os.ReadFile(filepath.Join(dir, "created"))
	if err != nil || string(got) != "complete" || checks == 0 {
		t.Fatalf("%q %v", got, err)
	}
	text := ""
	for _, p := range out.Result.Content {
		text += p.Text
	}
	if !strings.Contains(text, "committed_reported") || out.Result.ChildCalls == nil || len(out.Result.ChildCalls.Calls) != 1 || out.Result.ChildCalls.Calls[0].Execution.LocalEffect != agent.ToolLocalEffectCommittedReported {
		t.Fatalf("lost committed fact %+v", out)
	}
}

func TestReplaceCodemodeDenialAndEscapeDoNotPublish(t *testing.T) {
	for _, mode := range []string{"denied", "escape"} {
		t.Run(mode, func(t *testing.T) {
			dir, _, leaf := replaceFixture(t, ReplaceOptions{})
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
			name := "new"
			if mode == "escape" {
				name = "../" + filepath.Base(dir) + "-escaped"
			}
			raw, _ := json.Marshal(map[string]any{"code": `await tools.` + binding.ExportedName() + `({path:` + fmtJSON(name) + `,content:"complete"});`})
			checks := 0
			out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: code.Name, Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{code}, CheckToolPermission: func(_ context.Context, in agent.BeforeToolCallContext) error {
				if in.ToolCall.Name != code.Name {
					checks++
					if mode == "denied" {
						return errors.New("denied")
					}
				}
				return nil
			}})
			if out.Err == nil && !out.IsError {
				t.Fatal("unauthorized create succeeded")
			}
			if mode == "denied" && checks == 0 {
				t.Fatal("leaf permission bypassed")
			}
			if mode == "escape" {
				if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("outside destination exists: %v", err)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("unexpected destination %v %v", entries, err)
			}
		})
	}
}
