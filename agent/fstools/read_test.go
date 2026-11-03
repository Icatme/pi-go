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

func readFixture(t *testing.T, options ReadOptions) (string, *os.Root, agent.ToolDefinition) {
	t.Helper()
	dir, root, _ := fixture(t, Options{})
	tool, err := ReadDefinition(root, options)
	if err != nil {
		t.Fatal(err)
	}
	return dir, root, tool
}
func readCall(ctx context.Context, tool agent.ToolDefinition, p string) agent.ToolCallOutcome {
	raw, _ := json.Marshal(map[string]any{"path": p})
	return agent.RunToolCall(ctx, agent.ToolCall{ID: "read", Name: "read", Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
}
func readDecoded(t *testing.T, outcome agent.ToolCallOutcome) string {
	t.Helper()
	if outcome.Err != nil {
		t.Fatal(outcome.Err)
	}
	var result readOutput
	if err := json.Unmarshal(outcome.Result.StructuredContent, &result); err != nil {
		t.Fatal(err)
	}
	if len(outcome.Result.Content) != 1 || outcome.Result.Content[0].Text != string(outcome.Result.StructuredContent) {
		t.Fatal("inconsistent text and structured result")
	}
	return result.Text
}
func TestReadExplicitOptInAndOptions(t *testing.T) {
	if !supported {
		if _, err := ReadDefinition(nil, ReadOptions{}); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatal(err)
		}
		return
	}
	if _, err := ReadDefinition(nil, ReadOptions{}); err == nil {
		t.Fatal("nil root accepted")
	}
	_, root, _ := readFixture(t, ReadOptions{})
	for _, options := range []ReadOptions{{MaxBytes: -1}, {MaxBytes: (1 << 20) + 1}, {MaxResultBytes: 255}, {MaxResultBytes: (8 << 20) + 1}} {
		if _, err := ReadDefinition(root, options); err == nil {
			t.Fatal("invalid options accepted", options)
		}
	}
	tools, err := Definitions(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "ls" {
		t.Fatal("existing callers gained read authority")
	}
	root.Close()
	tool, err := ReadDefinition(root, ReadOptions{})
	if err != nil {
		t.Fatal("factory performed I/O:", err)
	}
	if result := readCall(t.Context(), tool, "file"); result.Err == nil {
		t.Fatal("closed root accepted")
	}
}
func TestReadTextAndBounds(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		data    []byte
		options ReadOptions
		fail    bool
	}{
		{name: "empty"},
		{name: "unicode and escaping", data: []byte("雪\n\"<>&")},
		{name: "exact byte cap", data: []byte("1234"), options: ReadOptions{MaxBytes: 4}},
		{name: "above byte cap", data: []byte("12345"), options: ReadOptions{MaxBytes: 4}, fail: true},
		{name: "above encoded cap", data: []byte(strings.Repeat("<", 100)), options: ReadOptions{MaxResultBytes: 256}, fail: true},
		{name: "invalid UTF8", data: []byte{0xff}, fail: true},
		{name: "NUL", data: []byte("before\x00after"), fail: true},
		{name: "multibyte above cap", data: []byte("雪"), options: ReadOptions{MaxBytes: 2}, fail: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir, _, tool := readFixture(t, scenario.options)
			if err := os.WriteFile(filepath.Join(dir, "file"), scenario.data, 0600); err != nil {
				t.Fatal(err)
			}
			outcome := readCall(t.Context(), tool, "file")
			if scenario.fail {
				if outcome.Err == nil || len(outcome.Result.StructuredContent) != 0 {
					t.Fatalf("unexpected result: %+v", outcome)
				}
				return
			}
			if got := readDecoded(t, outcome); got != string(scenario.data) {
				t.Fatalf("read %q", got)
			}
		})
	}
}
func TestReadPathValidationAndSafeErrors(t *testing.T) {
	dir, _, tool := readFixture(t, ReadOptions{})
	if err := os.WriteFile(filepath.Join(dir, "visible"), []byte("OK"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", ".", "/etc/passwd", "../visible", `C:\secret`, "a\x00b", strings.Repeat("x", 4097), "missing"} {
		got := readCall(t.Context(), tool, p)
		if got.Err == nil {
			t.Fatal("accepted", p)
		}
		if strings.Contains(got.Err.Error(), dir) {
			t.Fatal("host root leaked")
		}
		for _, part := range got.Result.Content {
			if strings.Contains(part.Text, dir) {
				t.Fatal("host root leaked in result")
			}
		}
	}
	for _, args := range []any{nil, map[string]any{"path": "../"}, map[string]any{"path": "visible", "offset": 1}, map[string]any{"path": string([]byte{255})}} {
		if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: args}); err == nil {
			t.Fatal("invalid direct args accepted")
		}
	}
}
func TestReadPermissionCancellationAndGrowth(t *testing.T) {
	dir, _, tool := readFixture(t, ReadOptions{MaxBytes: 4})
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("text"), 0600); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("revoked")
	for at := 1; at <= 6; at++ {
		checks := 0
		result, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "file"}, CheckPermission: func(context.Context) error {
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
	if got := readCall(ctx, tool, "file"); !errors.Is(got.Err, context.Canceled) {
		t.Fatal(got.Err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	checks := 0
	result, err := tool.Execute(ctx, agent.ToolExecutionContext{Args: map[string]any{"path": "file"}, CheckPermission: func(context.Context) error {
		checks++
		if checks == 6 {
			cancel()
		}
		return nil
	}})
	if !errors.Is(err, context.Canceled) || len(result.Content) != 0 {
		t.Fatal("late cancellation returned content", err)
	}
	checks = 0
	result, err = tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "file"}, CheckPermission: func(context.Context) error {
		checks++
		if checks == 4 {
			return os.WriteFile(file, []byte("grown"), 0600)
		}
		return nil
	}})
	if err == nil || len(result.Content) != 0 {
		t.Fatal("growth exceeded limit", err)
	}
}
func TestReadCodemodePermissionAndContent(t *testing.T) {
	dir, _, leaf := readFixture(t, ReadOptions{})
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("bounded text 雪"), 0600); err != nil {
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
	for _, deny := range []bool{false, true} {
		checks := 0
		args, _ := json.Marshal(map[string]any{"code": `text(await tools.` + binding.ExportedName() + `({path:"file"}));`})
		got := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: code.Name, Arguments: args}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{code}, CheckToolPermission: func(_ context.Context, call agent.BeforeToolCallContext) error {
			if call.ToolCall.Name != code.Name {
				checks++
				if deny {
					return errors.New("denied")
				}
			}
			return nil
		}})
		output := ""
		for _, part := range got.Result.Content {
			output += part.Text
		}
		if checks == 0 {
			t.Fatal("leaf permission was bypassed")
		}
		if deny {
			if got.Err == nil && !got.IsError {
				t.Fatal("denied call succeeded")
			}
			if strings.Contains(output, "bounded text") {
				t.Fatal("denied content leaked")
			}
		} else if got.Err != nil || !strings.Contains(output, "bounded text 雪") {
			t.Fatalf("%s: %v", output, got.Err)
		}
	}
}

func TestReadConcurrentInvocations(t *testing.T) {
	dir, _, tool := readFixture(t, ReadOptions{})
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("concurrent"), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			outcome := readCall(t.Context(), tool, "file")
			if outcome.Err != nil {
				t.Error(outcome.Err)
				return
			}
			var result readOutput
			if err := json.Unmarshal(outcome.Result.StructuredContent, &result); err != nil {
				t.Error(err)
				return
			}
			if result.Text != "concurrent" {
				t.Errorf("read %q", result.Text)
			}
		})
	}
	wg.Wait()
}
