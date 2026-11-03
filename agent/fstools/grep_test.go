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

func grepFixture(t *testing.T, data string, options GrepOptions) (string, *os.Root, agent.ToolDefinition) {
	t.Helper()
	dir, root, _ := fixture(t, Options{})
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	tool, err := GrepDefinition(root, options)
	if err != nil {
		t.Fatal(err)
	}
	return dir, root, tool
}
func grepCall(t *testing.T, tool agent.ToolDefinition, pattern string) grepOutput {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"path": "file", "pattern": pattern})
	outcome := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "grep", Name: "grep", Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
	if outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	var output grepOutput
	if err := json.Unmarshal(outcome.Result.StructuredContent, &output); err != nil {
		t.Fatal(err)
	}
	if len(outcome.Result.Content) != 1 || outcome.Result.Content[0].Text != string(outcome.Result.StructuredContent) {
		t.Fatal("inconsistent output")
	}
	return output
}
func TestGrepLines(t *testing.T) {
	for _, tt := range []struct {
		name, data, pattern string
		want                []grepMatch
		lines               int
	}{
		{"empty", "", "^$", []grepMatch{}, 0},
		{"blank", "\n", "^$", []grepMatch{{1, ""}}, 1},
		{"end LF", "first\nlast\n", "last", []grepMatch{{2, "last"}}, 2},
		{"no end LF", "first\nlast", "last", []grepMatch{{2, "last"}}, 2},
		{"CRLF", "first\r\nlast\r\n", "last", []grepMatch{{2, "last\r"}}, 2},
		{"bare CR", "first\rlast", "first.*last", []grepMatch{{1, "first\rlast"}}, 1},
		{"unicode", "雪\n雪雪", "雪", []grepMatch{{1, "雪"}, {2, "雪雪"}}, 2},
		{"multiple occurrences", "aba aba", "aba", []grepMatch{{1, "aba aba"}}, 1},
		{"inline flags", "UP\ndown", "(?i)^up$", []grepMatch{{1, "UP"}}, 2},
		{"no match", "a\nb", "z", []grepMatch{}, 2},
		{"leading blanks", "\n\nmatch", "match", []grepMatch{{3, "match"}}, 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, tool := grepFixture(t, tt.data, GrepOptions{})
			got := grepCall(t, tool, tt.pattern)
			if !reflect.DeepEqual(got.Matches, tt.want) || got.LinesScanned != tt.lines || got.BytesRead != len(tt.data) || got.Truncated {
				t.Fatalf("%+v", got)
			}
		})
	}
}
func TestGrepLimitsAndWholeFileValidation(t *testing.T) {
	for _, tt := range []struct {
		name, data     string
		opts           GrepOptions
		fail           bool
		reason         string
		matches, lines int
	}{
		{name: "exact bytes", data: "a\na", opts: GrepOptions{MaxScanBytes: 3}, matches: 2, lines: 2},
		{name: "above bytes", data: "a\na", opts: GrepOptions{MaxScanBytes: 2}, fail: true},
		{name: "exact unicode line", data: "雪", opts: GrepOptions{MaxLineBytes: 3}, matches: 1, lines: 1},
		{name: "above unicode line", data: "雪", opts: GrepOptions{MaxLineBytes: 2}, fail: true},
		{name: "late NUL", data: "a\n\x00", opts: GrepOptions{MaxMatches: 1}, fail: true},
		{name: "late invalid UTF8", data: "a\n\xff", opts: GrepOptions{MaxMatches: 1}, fail: true},
		{name: "late long nonmatch line", data: "a\nzzzz", opts: GrepOptions{MaxMatches: 1, MaxLineBytes: 3}, fail: true},
		{name: "match cap", data: "a\na\na", opts: GrepOptions{MaxMatches: 1}, reason: "max_matches", matches: 1, lines: 1},
		{name: "exact match cap conservative", data: "a", opts: GrepOptions{MaxMatches: 1}, reason: "max_matches", matches: 1, lines: 1},
		{name: "exact scan cap EOF", data: "a", opts: GrepOptions{MaxScannedLines: 1}, matches: 1, lines: 1},
		{name: "scan cap", data: "z\na", opts: GrepOptions{MaxScannedLines: 1}, reason: "max_scanned_lines", lines: 1},
		{name: "escaped JSON", data: strings.Repeat("<", 30), opts: GrepOptions{MaxResultBytes: 256}, reason: "max_result_bytes", lines: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, tool := grepFixture(t, tt.data, tt.opts)
			pattern := "[a雪<]"
			r, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "file", "pattern": pattern}})
			if tt.fail {
				if err == nil || len(r.StructuredContent) > 0 || len(r.Content) > 0 {
					t.Fatalf("partial content on failure: %+v %v", r, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got grepOutput
			if err := json.Unmarshal(r.StructuredContent, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Matches) != tt.matches || got.LinesScanned != tt.lines || got.Truncated != (tt.reason != "") || got.BytesRead != len(tt.data) {
				t.Fatalf("%+v", got)
			}
			if tt.reason != "" && !reflect.DeepEqual(got.Limits, []string{tt.reason}) {
				t.Fatalf("%+v", got)
			}
			if tt.opts.MaxResultBytes > 0 && len(r.StructuredContent) > tt.opts.MaxResultBytes {
				t.Fatal("JSON budget exceeded")
			}
		})
	}
}
func TestGrepArgumentsAndOptions(t *testing.T) {
	if !supported {
		if _, err := GrepDefinition(nil, GrepOptions{}); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatal(err)
		}
		return
	}
	if _, err := GrepDefinition(nil, GrepOptions{}); err == nil {
		t.Fatal("nil root")
	}
	_, root, tool := grepFixture(t, "secret", GrepOptions{})
	for _, o := range []GrepOptions{{MaxScanBytes: -1}, {MaxScanBytes: 1<<20 + 1}, {MaxLineBytes: -1}, {MaxLineBytes: 256<<10 + 1}, {MaxScannedLines: -1}, {MaxScannedLines: 100001}, {MaxMatches: -1}, {MaxMatches: 4097}, {MaxResultBytes: 255}, {MaxResultBytes: 1<<20 + 1}} {
		if _, err := GrepDefinition(root, o); err == nil {
			t.Fatal(o)
		}
	}
	for _, raw := range []string{`{"path":"file","pattern":"["}`, `{"path":"file","pattern":""}`, `{"path":"../file","pattern":"x"}`, `{"path":"file","pattern":"x","extra":1}`, `{"path":"file","pattern":"x"} {}`, `{"path":"file","pattern":"\\1"}`, `{"path":"file","pattern":"\uD800"}`} {
		if _, err := tool.ParseArguments(agent.ToolCall{Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatal(raw)
		}
	}
	for _, pattern := range []string{strings.Repeat("x", 257), "(" + strings.Repeat("a", 100) + "){1000}", "(abcdefghijklmnop){1000}", "a\x00b"} {
		checks := 0
		_, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "file", "pattern": pattern}, CheckPermission: func(context.Context) error { checks++; return nil }})
		if err == nil || checks != 0 {
			t.Fatalf("pattern accepted or reached permission: %q %d %v", pattern, checks, err)
		}
	}
	definitions, err := Definitions(root, Options{})
	if err != nil || len(definitions) != 1 || definitions[0].Name != "ls" {
		t.Fatal("implicit authority expansion")
	}
}
func TestGrepPermissionAndCancelDiscardResults(t *testing.T) {
	_, root, tool := grepFixture(t, "secret\nsecret", GrepOptions{})
	args := map[string]any{"path": "file", "pattern": "secret"}
	checks := 0
	if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: args, CheckPermission: func(context.Context) error { checks++; return nil }}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"deny first", "revoke before publish", "cancel before publish", "revoke while scanning", "cancel while scanning"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			n := 0
			r, err := tool.Execute(ctx, agent.ToolExecutionContext{Args: args, CheckPermission: func(context.Context) error {
				n++
				target := checks
				if strings.HasSuffix(mode, "while scanning") {
					target--
				}
				if mode == "deny first" || n == target {
					if strings.HasPrefix(mode, "cancel") {
						cancel()
						return nil
					}
					return errors.New("private policy reason")
				}
				return nil
			}})
			if err == nil || len(r.Content) > 0 || len(r.StructuredContent) > 0 {
				t.Fatalf("leaked: %+v %v", r, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tool.Execute(ctx, agent.ToolExecutionContext{Args: args}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	root.Close()
	if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: args}); err == nil {
		t.Fatal("closed root")
	}
}
func TestGrepCodemodePermissionAndPath(t *testing.T) {
	_, _, leaf := grepFixture(t, "secret", GrepOptions{})
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
	for _, mode := range []string{"allowed", "denied", "escape"} {
		t.Run(mode, func(t *testing.T) {
			p := "file"
			if mode == "escape" {
				p = "../file"
			}
			source := `text(await tools.` + binding.ExportedName() + `({path:` + fmtJSON(p) + `,pattern:"secret"}));`
			raw, _ := json.Marshal(map[string]any{"code": source})
			checks := 0
			out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: code.Name, Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{code}, CheckToolPermission: func(_ context.Context, c agent.BeforeToolCallContext) error {
				if c.ToolCall.Name != code.Name {
					checks++
					if mode == "denied" {
						return errors.New("revoked")
					}
				}
				return nil
			}})
			text := ""
			for _, part := range out.Result.Content {
				text += part.Text
			}
			if mode == "allowed" {
				if out.Err != nil || checks == 0 || !strings.Contains(text, "secret") {
					t.Fatalf("%s %v", text, out.Err)
				}
			} else if (out.Err == nil && !out.IsError) || strings.Contains(text, "secret") {
				t.Fatalf("%s %v", text, out.Err)
			}
		})
	}
}
