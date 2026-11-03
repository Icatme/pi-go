package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
)

func createFixture(t *testing.T, options CreateOptions) (string, *os.Root, agent.ToolDefinition) {
	t.Helper()
	dir, root, _ := fixture(t, Options{})
	tool, err := CreateFileDefinition(root, options)
	if err != nil {
		t.Fatal(err)
	}
	return dir, root, tool
}
func createArgs(path, content string) map[string]any {
	return map[string]any{"path": path, "content": content}
}
func TestCreateNewFileAndNoOverwrite(t *testing.T) {
	dir, _, tool := createFixture(t, CreateOptions{})
	for _, data := range []string{"", "snow 雪\n"} {
		name := "empty"
		if data != "" {
			name = "text"
		}
		r, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: createArgs(name, data)})
		if err != nil {
			t.Fatal(err)
		}
		if r.Execution.LocalEffect != agent.ToolLocalEffectCommittedReported || !strings.Contains(string(r.StructuredContent), `"durability":"synced"`) {
			t.Fatalf("%+v", r)
		}
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != data {
			t.Fatalf("%q %v", got, err)
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm()&^0600 != 0 {
			t.Fatal("file permissions")
		}
		if _, err = tool.Execute(t.Context(), agent.ToolExecutionContext{Args: createArgs(name, "replacement")}); err == nil {
			t.Fatal("overwrote")
		}
		got, _ = os.ReadFile(filepath.Join(dir, name))
		if string(got) != data {
			t.Fatal("changed existing data")
		}
	}
	if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: createArgs("missing/child", "data")}); err == nil {
		t.Fatal("implicitly created parents")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("named staging residue: %v %v", entries, err)
	}
}
func TestCreateConcurrentSameName(t *testing.T) {
	dir, _, tool := createFixture(t, CreateOptions{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, s := range []string{"first", "second"} {
		wg.Go(func() {
			_, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: createArgs("new", s)})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "new"))
	if err != nil || success != 1 || (string(data) != "first" && string(data) != "second") {
		t.Fatalf("%d %q %v", success, data, err)
	}
}
func TestCreateArgumentsOptionsAndExplicitOptIn(t *testing.T) {
	if !supported {
		if _, err := CreateFileDefinition(nil, CreateOptions{}); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatal(err)
		}
		return
	}
	if _, err := CreateFileDefinition(nil, CreateOptions{}); err == nil {
		t.Fatal("nil root")
	}
	dir, root, tool := createFixture(t, CreateOptions{MaxBytes: 3})
	for _, o := range []CreateOptions{{MaxBytes: -1}, {MaxBytes: 1<<20 + 1}} {
		if _, err := CreateFileDefinition(root, o); err == nil {
			t.Fatal(o)
		}
	}
	for _, args := range []map[string]any{createArgs("../escape", "a"), createArgs("/absolute", "a"), createArgs(".", "a"), createArgs("dir/", "a"), createArgs("dir/.", "a"), createArgs("file", "1234"), createArgs("file", "a\x00"), {"path": "file"}, {"path": "file", "content": nil}, {"path": "file", "content": "x", "mode": 0777}} {
		n := 0
		_, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: args, CheckPermission: func(context.Context) error { n++; return nil }})
		if err == nil || n != 0 {
			t.Fatalf("accepted/reached I/O: %v %d %v", args, n, err)
		}
	}
	if _, err := tool.ParseArguments(agent.ToolCall{Arguments: json.RawMessage(`{"path":"f","content":"x"} {}`)}); err == nil {
		t.Fatal("trailing JSON")
	}
	if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: createArgs("unicode", "雪")}); err != nil {
		t.Fatal(err)
	}
	definitions, err := Definitions(root, Options{})
	if err != nil || len(definitions) != 1 || definitions[0].Name != "ls" {
		t.Fatal("expanded default authority")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tool.Execute(ctx, agent.ToolExecutionContext{Args: createArgs("canceled", "x")}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "canceled")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published despite cancellation")
	}
	root.Close()
	if _, err := CreateFileDefinition(root, CreateOptions{}); err != nil {
		t.Fatal("factory performs I/O")
	}
	if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: createArgs("closed", "x")}); err == nil {
		t.Fatal("closed root")
	}
}

type fakeCreate struct {
	data                                            []byte
	writes, published, closed, dirSync              int
	writeErr, syncErr, publishErr, dirErr, closeErr error
	zero, short                                     bool
	afterPublish                                    func()
}

func (f *fakeCreate) Write(b []byte) (int, error) {
	f.writes++
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.zero {
		return 0, nil
	}
	n := len(b)
	if f.short {
		n = 1
	}
	f.data = append(f.data, b[:n]...)
	return n, nil
}
func (f *fakeCreate) Sync() error { return f.syncErr }
func (f *fakeCreate) Publish() error {
	f.published++
	if f.afterPublish != nil {
		f.afterPublish()
	}
	return f.publishErr
}
func (f *fakeCreate) SyncDirectory() error { f.dirSync++; return f.dirErr }
func (f *fakeCreate) Close() error         { f.closed++; return f.closeErr }
func TestCreateTransactionFaultsAndCommitFacts(t *testing.T) {
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
			r, err := publishCreate(t.Context(), agent.ToolExecutionContext{}, &f, []byte("complete"))
			if (err != nil) != tt.fail || f.closed != 1 {
				t.Fatalf("%+v %v", f, err)
			}
			if tt.committed {
				if r.Execution == nil || r.Execution.LocalEffect != agent.ToolLocalEffectCommittedReported || !strings.Contains(string(r.StructuredContent), `"created":true`) {
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
			r, err := publishCreate(ctx, in, f, []byte("x"))
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
func TestCreateCodemodeAndHookFailureKeepCommit(t *testing.T) {
	dir, _, leaf := createFixture(t, CreateOptions{})
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

func TestCreateCodemodeDenialAndEscapeDoNotPublish(t *testing.T) {
	for _, mode := range []string{"denied", "escape"} {
		t.Run(mode, func(t *testing.T) {
			dir, _, leaf := createFixture(t, CreateOptions{})
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
