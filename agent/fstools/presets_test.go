package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
)

func presetNames(defs []agent.ToolDefinition) []string {
	names := make([]string, len(defs))
	for i, def := range defs {
		names[i] = def.Name
	}
	return names
}
func TestPresetMembershipFreshnessAndOwnership(t *testing.T) {
	if !supported {
		if defs, err := ReadOnlyDefinitions(nil, ReadOnlyOptions{}); !errors.Is(err, ErrUnsupportedPlatform) || defs != nil {
			t.Fatal(defs, err)
		}
		if defs, err := WorkspaceWriteDefinitions(nil, WorkspaceWriteOptions{}); !errors.Is(err, ErrUnsupportedPlatform) || defs != nil {
			t.Fatal(defs, err)
		}
		return
	}
	_, root, _ := fixture(t, Options{})
	read, err := ReadOnlyDefinitions(root, ReadOnlyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ls", "read", "find", "grep", "preview_edit"}
	if !reflect.DeepEqual(presetNames(read), want) {
		t.Fatal(presetNames(read))
	}
	workspace, err := WorkspaceWriteDefinitions(root, WorkspaceWriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(presetNames(workspace), append(want, "create_file", "atomic_replace", "edit")) {
		t.Fatal(presetNames(workspace))
	}
	base, _ := Definitions(root, Options{})
	if !reflect.DeepEqual(presetNames(base), []string{"ls"}) {
		t.Fatal("changed defaults")
	}
	for i := range read {
		read[i].Parameters["x-test"] = true
		read[i].Name = "changed"
	}
	again, _ := ReadOnlyDefinitions(root, ReadOnlyOptions{})
	for i, def := range again {
		if def.Name != want[i] || def.Parameters["x-test"] != nil || workspace[i].Parameters["x-test"] != nil {
			t.Fatal("shared mutable definition")
		}
	}
	root.Close()
	if defs, err := WorkspaceWriteDefinitions(root, WorkspaceWriteOptions{}); err != nil || len(defs) != 8 {
		t.Fatal("factory performed I/O", err)
	}
	if _, err := again[0].Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "."}}); err == nil {
		t.Fatal("root lifetime bypassed")
	}
}
func TestPresetErrorsAreAllOrNothing(t *testing.T) {
	_, root, _ := fixture(t, Options{})
	for _, opts := range []ReadOnlyOptions{{List: Options{MaxEntries: -1}}, {Read: ReadOptions{MaxBytes: -1}}, {Find: FindOptions{MaxDepth: -1}}, {Grep: GrepOptions{MaxMatches: -1}}, {Preview: PreviewEditOptions{MaxBytes: -1}}} {
		if defs, err := ReadOnlyDefinitions(root, opts); err == nil || defs != nil {
			t.Fatal("partial readonly", defs, err)
		}
		if defs, err := WorkspaceWriteDefinitions(root, WorkspaceWriteOptions{ReadOnly: opts}); err == nil || defs != nil {
			t.Fatal("partial workspace", defs, err)
		}
	}
	for _, opts := range []WorkspaceWriteOptions{{Create: CreateOptions{MaxBytes: -1}}, {Replace: ReplaceOptions{MaxBytes: -1}}, {Edit: EditOptions{MaxBytes: -1}}} {
		if defs, err := WorkspaceWriteDefinitions(root, opts); err == nil || defs != nil {
			t.Fatal("partial writes", defs, err)
		}
	}
	if defs, err := ReadOnlyDefinitions(nil, ReadOnlyOptions{}); err == nil || defs != nil {
		t.Fatal("nil root")
	}
}
func TestPresetRootsAndLimitsRemainIndependent(t *testing.T) {
	for _, source := range []string{"tenant A", "tenant B"} {
		t.Run(source, func(t *testing.T) {
			dir, root, _ := fixture(t, Options{})
			if err := os.WriteFile(filepath.Join(dir, "file"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			defs, err := ReadOnlyDefinitions(root, ReadOnlyOptions{})
			if err != nil {
				t.Fatal(err)
			}
			r, err := defs[1].Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "file"}})
			if err != nil || !strings.Contains(string(r.StructuredContent), source) {
				t.Fatalf("%s %v", r.StructuredContent, err)
			}
			limited, err := WorkspaceWriteDefinitions(root, WorkspaceWriteOptions{ReadOnly: ReadOnlyOptions{Read: ReadOptions{MaxBytes: 3}}, Replace: ReplaceOptions{MaxBytes: 3}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := limited[1].Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "file"}}); err == nil {
				t.Fatal("read cap dropped")
			}
			if _, err := executeCaptured(t, t.Context(), limited[6].Execute, agent.ToolExecutionContext{Args: createArgs("file", "long")}); err == nil {
				t.Fatal("write cap dropped")
			}
			got, _ := os.ReadFile(filepath.Join(dir, "file"))
			if string(got) != source {
				t.Fatal("changed")
			}
		})
	}
}
func TestPresetCodemodeUsesLeafAuthorization(t *testing.T) {
	for _, mode := range []string{"read allowed", "write allowed", "deny outer", "deny read", "deny write", "readonly has no write"} {
		t.Run(mode, func(t *testing.T) {
			dir, root, _ := fixture(t, Options{})
			if err := os.WriteFile(filepath.Join(dir, "file"), []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			defs, err := WorkspaceWriteDefinitions(root, WorkspaceWriteOptions{})
			if err != nil {
				t.Fatal(err)
			}
			editAlias := codemodetool.Native(defs[7], "workspace").ExportedName()
			if mode == "readonly has no write" {
				defs, err = ReadOnlyDefinitions(root, ReadOnlyOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			sandbox, err := codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sandbox.Close(context.Background()) })
			bindings := make([]codemodetool.Binding, len(defs))
			for i, def := range defs {
				bindings[i] = codemodetool.Native(def, "workspace")
			}
			code, err := codemodetool.New(sandbox, bindings, codemodetool.Options{})
			if err != nil {
				t.Fatal(err)
			}
			script := `text(await tools.` + bindings[1].ExportedName() + `({path:"file"}));`
			write := strings.Contains(mode, "write")
			if write {
				script = `text(await tools.` + editAlias + `({path:"file",old_text:"old",new_text:"new"}));`
			}
			raw, _ := json.Marshal(map[string]any{"code": script})
			outerChecks, leafChecks := 0, 0
			out := runCaptured(t, t.Context(), agent.ToolCall{ID: "outer", Name: code.Name, Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{code}, CheckToolPermission: func(_ context.Context, in agent.BeforeToolCallContext) error {
				if in.ToolCall.Name == code.Name {
					outerChecks++
					if mode == "deny outer" {
						return errors.New("denied outer")
					}
					return nil
				}
				leafChecks++
				if mode == "deny read" || mode == "deny write" {
					return errors.New("denied leaf")
				}
				return nil
			}})
			got, _ := os.ReadFile(filepath.Join(dir, "file"))
			want := "old"
			if mode == "write allowed" {
				want = "new"
			}
			if string(got) != want || outerChecks == 0 {
				t.Fatalf("%q checks=%d %v", got, outerChecks, out.Err)
			}
			if mode == "read allowed" || mode == "write allowed" {
				if out.Err != nil || leafChecks == 0 {
					t.Fatalf("missing permission %d %v", leafChecks, out.Err)
				}
			}
			if mode == "deny read" || mode == "deny write" {
				if out.Err == nil || leafChecks == 0 {
					t.Fatal("leaf denial bypassed")
				}
			}
			if mode == "deny outer" && (out.Err == nil || leafChecks != 0) {
				t.Fatal("outer denial bypassed")
			}
			if mode == "readonly has no write" && out.Err == nil {
				t.Fatal("readonly acquired write")
			}
		})
	}
}
func TestPresetMultipleLiveRootsDoNotRebind(t *testing.T) {
	var sets [2][]agent.ToolDefinition
	for i, source := range []string{"tenant A private", "tenant B private"} {
		dir, root, _ := fixture(t, Options{})
		if err := os.WriteFile(filepath.Join(dir, "file"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		defs, err := ReadOnlyDefinitions(root, ReadOnlyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		sets[i] = defs
	}
	for _, i := range []int{0, 1, 0} {
		out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "read", Name: "read", Arguments: json.RawMessage(`{"path":"file"}`)}, agent.RunToolCallOptions{Tools: sets[i]})
		var result readOutput
		if out.Err != nil {
			t.Fatal(out.Err)
		}
		if err := json.Unmarshal(out.Result.StructuredContent, &result); err != nil {
			t.Fatal(err)
		}
		want := []string{"tenant A private", "tenant B private"}[i]
		if result.Text != want {
			t.Fatalf("root rebound: %q != %q", result.Text, want)
		}
	}
}
