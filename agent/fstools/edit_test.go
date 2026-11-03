package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editFixture(t *testing.T, source string, opts EditOptions) (string, *os.Root, agent.ToolDefinition) {
	t.Helper()
	dir, root, _ := previewFixture(t, source, PreviewEditOptions{})
	tool, err := EditDefinition(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	return dir, root, tool
}
func TestEditExactByteReplacement(t *testing.T) {
	for _, tt := range []struct {
		name, source, old, new, want string
		line                         int
	}{
		{"single", "before old after", "old", "new", "before new after", 1},
		{"unicode", "雪\nold雪", "old雪", "新", "雪\n新", 2},
		{"CRLF", "first\r\nold\r\nlast\r\n", "old\r\n", "new\r\n", "first\r\nnew\r\nlast\r\n", 2},
		{"mixed untouched", "first\r\nold\nlast\r", "old", "new", "first\r\nnew\nlast\r", 2},
		{"BOM preserved", "\ufeffold\r\n", "old", "new", "\ufeffnew\r\n", 1},
		{"explicit BOM removal", "\ufeffold", "\ufeffold", "new", "new", 1},
		{"no final newline", "first\nold", "old", "new", "first\nnew", 2},
		{"deletion", "old", "old", "", "", 1},
		{"replacement contains old", "old", "old", "old old", "old old", 1},
		{"bare CR is not linebreak", "first\rold", "old", "new", "first\rnew", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, _, tool := editFixture(t, tt.source, EditOptions{})
			raw, _ := json.Marshal(previewArgs("file", tt.old, tt.new))
			out := runCaptured(t, t.Context(), agent.ToolCall{ID: "edit", Name: tool.Name, Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
			if out.Err != nil {
				t.Fatal(out.Err)
			}
			got, err := os.ReadFile(filepath.Join(dir, "file"))
			if err != nil || string(got) != tt.want {
				t.Fatalf("%q %v", got, err)
			}
			if out.Result.Execution == nil || !out.committed() || !strings.Contains(string(out.Result.StructuredContent), `"published":true`) || strings.Contains(string(out.Result.StructuredContent), `"content"`) {
				t.Fatalf("%+v", out)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatal("staging residue")
			}
		})
	}
}
func TestEditRejectedInputLeavesSourceUnchanged(t *testing.T) {
	for _, tt := range []struct {
		source, old, new string
		max              int
	}{
		{"aaa", "aa", "b", 0}, {"old old", "old", "new", 0}, {"original", "missing", "x", 0}, {"old", "", "x", 0}, {"old", "old", "old", 0}, {"a\r\nb", "a\nb", "x", 0}, {"old\x00", "old", "x", 0}, {"old\xff", "old", "x", 0}, {"abc", "a", "zz", 3}, {"1234", "1", "2", 3},
	} {
		t.Run(fmt.Sprintf("%q/%q", tt.source, tt.old), func(t *testing.T) {
			dir, _, tool := editFixture(t, tt.source, EditOptions{MaxBytes: tt.max})
			r, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: previewArgs("file", tt.old, tt.new)})
			if err == nil || len(r.Content) != 0 {
				t.Fatalf("%+v %v", r, err)
			}
			got, _ := os.ReadFile(filepath.Join(dir, "file"))
			entries, _ := os.ReadDir(dir)
			if string(got) != tt.source || len(entries) != 1 {
				t.Fatal("changed invalid input")
			}
		})
	}
}
func TestEditOptionsPathsAndOptIn(t *testing.T) {
	if !supported {
		if _, err := EditDefinition(nil, EditOptions{}); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatal(err)
		}
		return
	}
	if _, err := EditDefinition(nil, EditOptions{}); err == nil {
		t.Fatal("nil root")
	}
	dir, root, tool := editFixture(t, "old", EditOptions{})
	if _, err := EditDefinition(root, EditOptions{MaxBytes: 1<<20 + 1}); err == nil {
		t.Fatal("limit")
	}
	defs, _ := Definitions(root, Options{})
	if len(defs) != 1 || defs[0].Name != "ls" {
		t.Fatal("implicit authority")
	}
	for _, path := range []string{"file/", "file/.", ".", "../file", "/file"} {
		checks := 0
		_, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: previewArgs(path, "old", "new"), CheckPermission: func(context.Context) error { checks++; return nil }})
		if err == nil || checks != 0 {
			t.Fatal("bad path reached I/O", path, err)
		}
	}
	for _, raw := range []string{`{"path":"file","old_text":"old","new_text":"\ud800"}`, `{"path":"file","old_text":"old"}`, `{"path":"file","old_text":"old","new_text":"new","apply":true}`} {
		if _, err := tool.ParseArguments(agent.ToolCall{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatal(raw)
		}
	}
	if _, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: previewArgs("missing", "old", "new")}); err == nil {
		t.Fatal("edited missing source")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "file"))
	if string(got) != "old" {
		t.Fatal("changed source")
	}
}
func TestEditEveryPermissionBoundaryAndCommit(t *testing.T) {
	dir, _, tool := editFixture(t, "old", EditOptions{})
	checks := 0
	if _, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: previewArgs("file", "old", "new"), CheckPermission: func(context.Context) error { checks++; return nil }}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"deny", "cancel"} {
		for stage := 1; stage <= checks; stage++ {
			t.Run(fmt.Sprintf("%s/%d", mode, stage), func(t *testing.T) {
				if err := os.WriteFile(filepath.Join(dir, "file"), []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				n := 0
				committed := false
				r, err := executeCaptured(t, ctx, tool.Execute, agent.ToolExecutionContext{Args: previewArgs("file", "old", "new"), CheckPermission: func(context.Context) error {
					n++
					if n == stage {
						got, err := os.ReadFile(filepath.Join(dir, "file"))
						if err != nil {
							t.Fatal(err)
						}
						committed = string(got) == "new"
						if mode == "cancel" {
							cancel()
							return nil
						}
						return errors.New("private denial")
					}
					return nil
				}})
				if err == nil {
					t.Fatal("ignored cancellation/revocation")
				}
				got, _ := os.ReadFile(filepath.Join(dir, "file"))
				want := "old"
				if committed {
					want = "new"
					var typed *agent.ToolExecutionError
					if r.Execution == nil || !r.committed() || !errors.As(err, &typed) || typed.Execution.Remote != agent.ToolRemoteNotApplicable {
						t.Fatalf("lost commit %+v %v", r, err)
					}
				} else if r.committed() || len(r.Content) > 0 {
					t.Fatal("uncommitted success")
				}
				entries, _ := os.ReadDir(dir)
				if string(got) != want || len(entries) != 1 || strings.Contains(err.Error(), "private") {
					t.Fatal("unsafe failure", string(got), entries, err)
				}
			})
		}
	}
}
func TestEditExternalConcurrentUpdateIsLastWriterWins(t *testing.T) {
	for _, mode := range []string{"update", "delete"} {
		t.Run(mode, func(t *testing.T) {
			dir, _, tool := editFixture(t, "old", EditOptions{})
			changed := false
			_, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: previewArgs("file", "old", "new"), CheckPermission: func(context.Context) error {
				entries, err := os.ReadDir(dir)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".pigo-replace-") && !changed {
						changed = true
						if mode == "delete" {
							return os.Remove(filepath.Join(dir, "file"))
						}
						return os.WriteFile(filepath.Join(dir, "file"), []byte("external update"), 0600)
					}
				}
				return nil
			}})
			got, _ := os.ReadFile(filepath.Join(dir, "file"))
			if err != nil || !changed || string(got) != "new" {
				t.Fatalf("%t %q %v", changed, got, err)
			}
		})
	}
}
func TestEditCodemodeAuthorityAndHookFailure(t *testing.T) {
	for _, mode := range []string{"allowed", "denied", "escape", "hook failure"} {
		t.Run(mode, func(t *testing.T) {
			dir, _, leaf := editFixture(t, "old SECRET_UNMATCHED", EditOptions{})
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
			path := "file"
			if mode == "escape" {
				path = "../" + filepath.Base(dir) + "-outside"
			}
			raw, _ := json.Marshal(map[string]any{"code": `try{const result=await tools.` + binding.ExportedName() + `({path:` + fmtJSON(path) + `,old_text:"old",new_text:"new"});text(result)}catch(e){text(JSON.stringify(e.execution))}`})
			checks := 0
			out := runCaptured(t, t.Context(), agent.ToolCall{ID: "outer", Name: code.Name, Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{code}, CheckToolPermission: func(_ context.Context, in agent.BeforeToolCallContext) error {
				if in.ToolCall.Name != code.Name {
					checks++
					if mode == "denied" {
						return errors.New("denied")
					}
				}
				return nil
			}, AfterToolCall: func(_ context.Context, in agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
				if mode == "hook failure" && in.ParentToolCallID != "" {
					return agent.AfterToolCallResult{}, errors.New("withheld")
				}
				return agent.AfterToolCallResult{}, nil
			}})
			got, _ := os.ReadFile(filepath.Join(dir, "file"))
			want := "old SECRET_UNMATCHED"
			if mode == "allowed" || mode == "hook failure" {
				want = "new SECRET_UNMATCHED"
				if checks == 0 {
					t.Fatal("bypassed permission")
				}
			}
			if string(got) != want {
				t.Fatalf("%q", got)
			}
			if mode == "escape" {
				if _, err := os.Lstat(filepath.Join(dir, path)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("escaped", err)
				}
			}
			output := ""
			for _, part := range out.Result.Content {
				output += part.Text
			}
			if strings.Contains(output, "SECRET_UNMATCHED") {
				t.Fatal("leaked original content")
			}
			if mode == "hook failure" {
				if out.Result.ChildCalls == nil || len(out.Result.ChildCalls.Calls) != 1 || !out.committed() || !strings.Contains(output, "local") || strings.Contains(output, "committed_reported") || strings.Contains(output, "local_effect") || strings.Contains(output, "committed") {
					t.Fatalf("lost commit %+v", out)
				}
			}
		})
	}
}
func TestEditPublicationResolvesPathAgainAfterReading(t *testing.T) {
	dir, root, tool := editFixture(t, "old", EditOptions{})
	if err := os.Mkdir(filepath.Join(dir, "parent"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "file"), filepath.Join(dir, "parent/file")); err != nil {
		t.Fatal(err)
	}
	readChecks := 0
	if _, err := readTextFile(t.Context(), agent.ToolExecutionContext{CheckPermission: func(context.Context) error { readChecks++; return nil }}, root, "parent/file", 64<<10); err != nil {
		t.Fatal(err)
	}
	checks := 0
	changed := false
	_, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: previewArgs("parent/file", "old", "new"), CheckPermission: func(context.Context) error {
		checks++
		if checks == readChecks+1 {
			// The read is finished; candidate computation still uses those old bytes.
			if err := os.Rename(filepath.Join(dir, "parent"), filepath.Join(dir, "original")); err != nil {
				return err
			}
			if err := os.Mkdir(filepath.Join(dir, "parent"), 0700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, "parent/file"), []byte("external different inode"), 0600); err != nil {
				return err
			}
			changed = true
		}
		return nil
	}})
	original, _ := os.ReadFile(filepath.Join(dir, "original/file"))
	current, _ := os.ReadFile(filepath.Join(dir, "parent/file"))
	if err != nil || !changed || string(original) != "old" || string(current) != "new" {
		t.Fatalf("%t old=%q current=%q %v", changed, original, current, err)
	}
}
