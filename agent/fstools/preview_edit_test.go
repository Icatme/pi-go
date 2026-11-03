package fstools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
)

func previewFixture(t *testing.T, source string, opts PreviewEditOptions) (string, *os.Root, agent.ToolDefinition) {
	t.Helper()
	dir, root, _ := fixture(t, Options{})
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	tool, err := PreviewEditDefinition(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	return dir, root, tool
}
func previewArgs(p, old, new string) map[string]any {
	return map[string]any{"path": p, "old_text": old, "new_text": new}
}
func shaText(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func TestPreviewExactReplacementAndRawFingerprints(t *testing.T) {
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
			dir, _, tool := previewFixture(t, tt.source, PreviewEditOptions{})
			raw, _ := json.Marshal(previewArgs("file", tt.old, tt.new))
			out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "preview", Name: tool.Name, Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
			if out.Err != nil {
				t.Fatal(out.Err)
			}
			var got previewOutput
			if err := json.Unmarshal(out.Result.StructuredContent, &got); err != nil {
				t.Fatal(err)
			}
			if !got.PreviewOnly || got.Content != tt.want || got.MatchLine != tt.line || got.OriginalSHA256 != shaText(tt.source) || got.CandidateSHA256 != shaText(tt.want) {
				t.Fatalf("%+v", got)
			}
			if len(out.Result.Content) != 1 || out.Result.Content[0].Text != string(out.Result.StructuredContent) {
				t.Fatal("output mismatch")
			}
			unchanged, err := os.ReadFile(filepath.Join(dir, "file"))
			entries, listErr := os.ReadDir(dir)
			if err != nil || string(unchanged) != tt.source || listErr != nil || len(entries) != 1 {
				t.Fatal("preview changed filesystem contents")
			}
		})
	}
}
func TestPreviewAmbiguityTextAndOutputLimits(t *testing.T) {
	for _, tt := range []struct {
		name, source, old, new string
		opts                   PreviewEditOptions
		fail                   bool
	}{
		{name: "not found", source: "original", old: "missing", new: "new", fail: true},
		{name: "overlap", source: "aaa", old: "aa", new: "b", fail: true},
		{name: "multiple", source: "old old", old: "old", new: "new", fail: true},
		{name: "no newline normalization", source: "a\r\nb", old: "a\nb", new: "x", fail: true},
		{name: "empty source", old: "x", new: "y", fail: true},
		{name: "late invalid UTF8", source: "old\xff", old: "old", new: "new", fail: true},
		{name: "late NUL", source: "old\x00", old: "old", new: "new", fail: true},
		{name: "source limit", source: "1234", old: "1", new: "2", opts: PreviewEditOptions{MaxBytes: 3}, fail: true},
		{name: "candidate limit", source: "abc", old: "a", new: "zz", opts: PreviewEditOptions{MaxBytes: 3}, fail: true},
		{name: "exact candidate limit", source: "ab", old: "a", new: "zz", opts: PreviewEditOptions{MaxBytes: 3}},
		{name: "encoded escapes", source: "old", old: "old", new: strings.Repeat("<", 100), opts: PreviewEditOptions{MaxResultBytes: 512}, fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, _, tool := previewFixture(t, tt.source, tt.opts)
			r, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: previewArgs("file", tt.old, tt.new)})
			if (err != nil) != tt.fail {
				t.Fatalf("%+v %v", r, err)
			}
			if tt.fail && (len(r.Content) > 0 || len(r.StructuredContent) > 0) {
				t.Fatal("partial preview leaked")
			}
			got, _ := os.ReadFile(filepath.Join(dir, "file"))
			if string(got) != tt.source {
				t.Fatal("source changed")
			}
		})
	}
}
func TestPreviewArgumentsOptionsAndOptIn(t *testing.T) {
	if !supported {
		if _, err := PreviewEditDefinition(nil, PreviewEditOptions{}); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatal(err)
		}
		return
	}
	if _, err := PreviewEditDefinition(nil, PreviewEditOptions{}); err == nil {
		t.Fatal("nil root")
	}
	_, root, tool := previewFixture(t, "old", PreviewEditOptions{MaxBytes: 3})
	for _, o := range []PreviewEditOptions{{MaxBytes: -1}, {MaxBytes: 1<<20 + 1}, {MaxResultBytes: 511}, {MaxResultBytes: 8<<20 + 1}} {
		if _, err := PreviewEditDefinition(root, o); err == nil {
			t.Fatal(o)
		}
	}
	for _, args := range []map[string]any{previewArgs("../file", "old", "new"), previewArgs("file", "", "new"), previewArgs("file", "old", "old"), previewArgs("file", "old", "long"), previewArgs("file", "long", "x"), previewArgs("file", "old", "a\x00"), {"path": "file", "old_text": "old"}, {"path": "file", "old_text": "old", "new_text": nil}, {"path": "file", "old_text": "old", "new_text": "new", "apply": true}} {
		checks := 0
		_, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: args, CheckPermission: func(context.Context) error { checks++; return nil }})
		if err == nil || checks != 0 {
			t.Fatalf("bad args reached I/O: %v %d %v", args, checks, err)
		}
	}
	for _, raw := range []string{`{"path":"file","old_text":"old","new_text":"new"} {}`, `{"path":"file","old_text":"\ud800","new_text":"new"}`} {
		if _, err := tool.ParseArguments(agent.ToolCall{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatal(raw)
		}
	}
	definitions, err := Definitions(root, Options{})
	if err != nil || len(definitions) != 1 || definitions[0].Name != "ls" {
		t.Fatal("implicit authority")
	}
	root.Close()
	if _, err := PreviewEditDefinition(root, PreviewEditOptions{}); err != nil {
		t.Fatal("factory did I/O")
	}
	if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: previewArgs("file", "old", "new")}); err == nil {
		t.Fatal("closed root")
	}
}
func TestPreviewRevocationCancellationAndStaleFingerprint(t *testing.T) {
	dir, _, tool := previewFixture(t, "old", PreviewEditOptions{})
	args := previewArgs("file", "old", "new")
	checks := 0
	if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: args, CheckPermission: func(context.Context) error { checks++; return nil }}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"deny", "cancel", "change source"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			n := 0
			r, err := tool.Execute(ctx, agent.ToolExecutionContext{Args: args, CheckPermission: func(context.Context) error {
				n++
				if n == checks {
					switch mode {
					case "deny":
						return errors.New("revoked")
					case "cancel":
						cancel()
					case "change source":
						return os.WriteFile(filepath.Join(dir, "file"), []byte("external change"), 0600)
					}
				}
				return nil
			}})
			if mode != "change source" {
				if err == nil || len(r.Content) > 0 || len(r.StructuredContent) > 0 {
					t.Fatal("published unauthorized preview")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var p previewOutput
			if err := json.Unmarshal(r.StructuredContent, &p); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(filepath.Join(dir, "file"))
			if p.OriginalSHA256 != shaText("old") || p.Content != "new" || string(data) != "external change" {
				t.Fatal("preview pretended to be CAS or overwrote change")
			}
		})
	}
}
func TestPreviewCodemodePermissionAndRoot(t *testing.T) {
	for _, mode := range []string{"allowed", "denied", "escape"} {
		t.Run(mode, func(t *testing.T) {
			dir, _, leaf := previewFixture(t, "old", PreviewEditOptions{})
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
			p := "file"
			if mode == "escape" {
				p = "../file"
			}
			raw, _ := json.Marshal(map[string]any{"code": `text(await tools.` + binding.ExportedName() + `({path:` + fmtJSON(p) + `,old_text:"old",new_text:"new"}));`})
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
			text := ""
			for _, part := range out.Result.Content {
				text += part.Text
			}
			if mode == "allowed" {
				if out.Err != nil || checks == 0 || !strings.Contains(text, "preview_only") {
					t.Fatalf("%s %v", text, out.Err)
				}
			} else {
				if out.Err == nil && !out.IsError {
					t.Fatal("unauthorized preview succeeded")
				}
				if strings.Contains(text, "candidate_sha256") {
					t.Fatal("candidate leaked")
				}
			}
			got, _ := os.ReadFile(filepath.Join(dir, "file"))
			if string(got) != "old" {
				t.Fatal("preview wrote source")
			}
		})
	}
}
