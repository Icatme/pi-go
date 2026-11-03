package exectools_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/exectools"
	"github.com/Icatme/pi-go/executionreceipt"
)

type executionOutput struct {
	ExitCode         int    `json:"exit_code"`
	Stdout           string `json:"stdout"`
	Stderr           string `json:"stderr"`
	Truncated        bool   `json:"truncated"`
	EncodingReplaced bool   `json:"encoding_replaced"`
}

func commandTool(t *testing.T, execute exectools.CommandExecutor, options exectools.Options) agent.ToolDefinition {
	t.Helper()
	tool, err := exectools.CommandDefinition(execute, options)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func pythonTool(t *testing.T, execute exectools.PythonExecutor, options exectools.Options) agent.ToolDefinition {
	t.Helper()
	tool, err := exectools.PythonDefinition(execute, options)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func receiptCall(t *testing.T, ctx context.Context, tool agent.ToolDefinition, args map[string]any) (agent.ToolCallOutcome, *executionreceipt.Handle) {
	t.Helper()
	return receiptCallOptions(t, ctx, agent.ToolCall{ID: "fixture-call", Name: tool.Name, ParsedArgs: args}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}, 1)
}

func receiptCallOptions(t *testing.T, ctx context.Context, call agent.ToolCall, options agent.RunToolCallOptions, capacity int) (agent.ToolCallOutcome, *executionreceipt.Handle) {
	t.Helper()
	out, h, err := agent.RunToolCallWithReceipts(ctx, call, options, capacity)
	if err != nil {
		t.Fatal(err)
	}
	return out, h
}

func decodedOutput(t *testing.T, out agent.ToolCallOutcome) executionOutput {
	t.Helper()
	if out.Err != nil {
		t.Fatalf("unexpected execution error: %v; result=%+v", out.Err, out.Result)
	}
	var value executionOutput
	if err := json.Unmarshal(out.Result.StructuredContent, &value); err != nil {
		t.Fatalf("decode result: %v; result=%+v", err, out.Result)
	}
	if len(out.Result.Content) != 1 || out.Result.Content[0].Type != agent.PartTypeText || out.Result.Content[0].Text != string(out.Result.StructuredContent) {
		t.Fatalf("text and structured output differ: %+v", out.Result)
	}
	return value
}

func assertFailure(t *testing.T, out agent.ToolCallOutcome, code agent.ToolFailureCode) {
	t.Helper()
	if out.Err == nil || !out.IsError || !out.Result.IsError || out.Failure == nil || out.Failure.Code != code {
		t.Fatalf("want %s failure, got %+v", code, out)
	}
}

func TestFactoriesAreExplicitInertAndBounded(t *testing.T) {
	if _, err := exectools.CommandDefinition(nil, exectools.Options{}); err == nil {
		t.Fatal("nil command executor accepted")
	}
	if _, err := exectools.PythonDefinition(nil, exectools.Options{}); err == nil {
		t.Fatal("nil Python executor accepted")
	}
	var entered atomic.Int32
	command := func(context.Context, []string, io.Writer, io.Writer) (int, error) { entered.Add(1); return 0, nil }
	python := func(context.Context, string, io.Writer, io.Writer) (int, error) { entered.Add(1); return 0, nil }
	for _, options := range []exectools.Options{
		{MaxInputBytes: -1}, {MaxInputBytes: (256 << 10) + 1},
		{MaxOutputBytes: -1}, {MaxOutputBytes: (64 << 10) + 1},
		{Timeout: -time.Nanosecond}, {Timeout: 10*time.Minute + time.Nanosecond},
	} {
		if _, err := exectools.CommandDefinition(command, options); err == nil {
			t.Errorf("command accepted invalid limits %+v", options)
		}
		if _, err := exectools.PythonDefinition(python, options); err == nil {
			t.Errorf("Python accepted invalid limits %+v", options)
		}
	}
	for _, options := range []exectools.Options{{}, {MaxInputBytes: 1, MaxOutputBytes: 1, Timeout: time.Nanosecond}, {MaxInputBytes: 256 << 10, MaxOutputBytes: 64 << 10, Timeout: 10 * time.Minute}} {
		c := commandTool(t, command, options)
		p := pythonTool(t, python, options)
		if c.Name != "run_command" || p.Name != "run_python" || c.ParseArguments == nil || p.ParseArguments == nil || c.MaxArgumentBytes <= 0 || p.MaxArgumentBytes <= 0 {
			t.Fatalf("invalid definitions: command=%+v Python=%+v", c, p)
		}
	}
	if entered.Load() != 0 {
		t.Fatal("factory executed a backend")
	}
}

func TestLegacyAndDirectExecutionRequireCapture(t *testing.T) {
	var entered atomic.Int32
	tools := []agent.ToolDefinition{
		commandTool(t, func(context.Context, []string, io.Writer, io.Writer) (int, error) { entered.Add(1); return 0, nil }, exectools.Options{}),
		pythonTool(t, func(context.Context, string, io.Writer, io.Writer) (int, error) { entered.Add(1); return 0, nil }, exectools.Options{}),
	}
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			args := map[string]any{"args": []string{}}
			if tool.Name == "run_python" {
				args = map[string]any{"code": "pass"}
			}
			out := agent.RunToolCall(t.Context(), agent.ToolCall{Name: tool.Name, ParsedArgs: args}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
			assertFailure(t, out, agent.ToolFailurePolicyDenied)
			if out.Failure.Reason != "capture_required" {
				t.Fatalf("capture failure=%+v", out.Failure)
			}
			if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: args}); err == nil {
				t.Fatal("direct Execute accepted absent receipt capture")
			}
		})
	}
	if entered.Load() != 0 {
		t.Fatal("backend ran without capture")
	}
}

func TestStrictArgumentsBeforeBackend(t *testing.T) {
	var entered atomic.Int32
	command := commandTool(t, func(context.Context, []string, io.Writer, io.Writer) (int, error) { entered.Add(1); return 0, nil }, exectools.Options{MaxInputBytes: 4})
	python := pythonTool(t, func(context.Context, string, io.Writer, io.Writer) (int, error) { entered.Add(1); return 0, nil }, exectools.Options{MaxInputBytes: 4})
	for _, test := range []struct {
		tool agent.ToolDefinition
		raws []string
	}{
		{command, []string{`null`, `[]`, `{}`, `{"args":null}`, `{"args":"x"}`, `{"args":[1]}`, `{"args":[null]}`, `{"args":[],"extra":true}`, `{"Args":[]}`, `{"args":["a\u0000b"]}`, `{"args":["\ud800"]}`, `{"args":["\udc00"]}`, `{"args":["雪雪"]}`, `{"args":["abc","de"]}`, `{"args":[]} {}`, `{"args":[]`}},
		{python, []string{`null`, `[]`, `{}`, `{"code":null}`, `{"code":1}`, `{"code":[]}`, `{"code":""}`, `{"code":"x","extra":true}`, `{"Code":"x"}`, `{"code":"a\u0000b"}`, `{"code":"\ud800"}`, `{"code":"\udc00"}`, `{"code":"雪雪"}`, `{"code":"abcde"}`, `{"code":"x"} {}`, `{"code":"x"`}},
	} {
		for _, raw := range test.raws {
			t.Run(test.tool.Name+"/"+raw, func(t *testing.T) {
				out, h := receiptCallOptions(t, t.Context(), agent.ToolCall{Name: test.tool.Name, Arguments: json.RawMessage(raw)}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{test.tool}}, 1)
				assertFailure(t, out, agent.ToolFailureArgumentInvalid)
				if len(h.Snapshot()) != 0 {
					t.Fatal("invalid preflight consumed an executor receipt")
				}
			})
		}
	}
	for _, tool := range []agent.ToolDefinition{command, python} {
		field, value := "args", any([]string{string([]byte{255})})
		if tool.Name == "run_python" {
			field, value = "code", string([]byte{255})
		}
		out, _ := receiptCall(t, t.Context(), tool, map[string]any{field: value})
		assertFailure(t, out, agent.ToolFailureArgumentInvalid)
	}
	if entered.Load() != 0 {
		t.Fatal("invalid arguments reached backend")
	}
}

func TestExecuteRevalidatesHostArguments(t *testing.T) {
	var entered atomic.Int32
	command := commandTool(t, func(context.Context, []string, io.Writer, io.Writer) (int, error) { entered.Add(1); return 0, nil }, exectools.Options{MaxInputBytes: 4})
	python := pythonTool(t, func(context.Context, string, io.Writer, io.Writer) (int, error) { entered.Add(1); return 0, nil }, exectools.Options{MaxInputBytes: 4})
	for _, test := range []struct {
		tool agent.ToolDefinition
		args any
	}{
		{command, nil}, {command, map[string]any{"args": []any{nil}}},
		{command, map[string]any{"Args": []string{}}}, {command, map[string]any{"args": []string{"abcde"}}},
		{command, map[string]any{"args": []string{"\x00"}}}, {command, map[string]any{"args": []string{string([]byte{255})}}},
		{command, map[string]any{"args": []string{}, "program": "untrusted"}},
		{python, map[string]any{"Code": "x"}}, {python, map[string]any{"code": "abcde"}},
		{python, map[string]any{"code": "\x00"}}, {python, map[string]any{"code": string([]byte{255})}},
		{python, map[string]any{"code": "x", "interpreter": "untrusted"}},
	} {
		t.Run(fmt.Sprintf("%s/%#v", test.tool.Name, test.args), func(t *testing.T) {
			// A public receipt-aware invocation grants capture, not input validity.
			wrapper := agent.ToolDefinition{Name: "invoke_adapter", Execute: func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
				in.Args = test.args
				return test.tool.Execute(ctx, in)
			}}
			out, _ := receiptCall(t, t.Context(), wrapper, nil)
			assertFailure(t, out, agent.ToolFailureArgumentInvalid)
		})
	}
	if entered.Load() != 0 {
		t.Fatal("invalid direct arguments reached backend")
	}
}

func TestDuplicateFieldsAndRawArgumentBudget(t *testing.T) {
	var entered atomic.Int32
	command := commandTool(t, func(context.Context, []string, io.Writer, io.Writer) (int, error) { entered.Add(1); return 0, nil }, exectools.Options{MaxInputBytes: 4})
	python := pythonTool(t, func(context.Context, string, io.Writer, io.Writer) (int, error) { entered.Add(1); return 0, nil }, exectools.Options{MaxInputBytes: 4})
	for _, test := range []struct {
		tool agent.ToolDefinition
		raw  string
	}{
		{command, `{"args":[],"args":["x"]}`},
		{python, `{"code":"x","code":"y"}`},
	} {
		out, _ := receiptCallOptions(t, t.Context(), agent.ToolCall{Name: test.tool.Name, Arguments: json.RawMessage(test.raw)}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{test.tool}}, 1)
		assertFailure(t, out, agent.ToolFailureArgumentInvalid)
		// A valid tiny value cannot use arbitrary whitespace to bypass wire limits.
		raw := strings.Repeat(" ", test.tool.MaxArgumentBytes+1) + test.raw
		out, h := receiptCallOptions(t, t.Context(), agent.ToolCall{Name: test.tool.Name, Arguments: json.RawMessage(raw)}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{test.tool}}, 1)
		assertFailure(t, out, agent.ToolFailureResource)
		if len(h.Snapshot()) != 0 {
			t.Fatal("oversized wire arguments reached execution")
		}
	}
	if entered.Load() != 0 {
		t.Fatal("duplicate or oversized arguments reached backend")
	}
}

func TestInputBudgetAndExactArgumentDelivery(t *testing.T) {
	for _, args := range [][]string{{}, {""}, {"雪", "a"}, {";not a shell", "$(not-expanded)", "a b", "--option"}, make([]string, 256)} {
		tool := commandTool(t, func(_ context.Context, got []string, _, _ io.Writer) (int, error) {
			if !reflect.DeepEqual(got, args) {
				t.Errorf("arguments changed: got=%q want=%q", got, args)
			}
			return 0, nil
		}, exectools.Options{})
		out, _ := receiptCall(t, t.Context(), tool, map[string]any{"args": args})
		if got := decodedOutput(t, out); got.ExitCode != 0 || out.IsError {
			t.Fatal(got)
		}
	}
	var entered atomic.Int32
	tool := commandTool(t, func(context.Context, []string, io.Writer, io.Writer) (int, error) { entered.Add(1); return 0, nil }, exectools.Options{MaxInputBytes: 4})
	out, _ := receiptCall(t, t.Context(), tool, map[string]any{"args": []string{"雪", "a"}})
	decodedOutput(t, out)
	for _, args := range [][]string{{"雪", "ab"}, make([]string, 257)} {
		out, _ := receiptCall(t, t.Context(), tool, map[string]any{"args": args})
		assertFailure(t, out, agent.ToolFailureArgumentInvalid)
	}
	if entered.Load() != 1 {
		t.Fatalf("backend calls=%d", entered.Load())
	}
	code := "print('雪')\n"
	python := pythonTool(t, func(_ context.Context, got string, _, _ io.Writer) (int, error) {
		if got != code {
			t.Errorf("source changed: %q", got)
		}
		return 0, nil
	}, exectools.Options{MaxInputBytes: len(code)})
	out, _ = receiptCall(t, t.Context(), python, map[string]any{"code": code})
	decodedOutput(t, out)
	out, _ = receiptCall(t, t.Context(), python, map[string]any{"code": code + " "})
	assertFailure(t, out, agent.ToolFailureArgumentInvalid)
	// JSON's six-byte escape form must fit when the decoded input fits.
	raw := `{"code":"` + strings.Repeat(`\u0061`, len(code)) + `"}`
	python = pythonTool(t, func(_ context.Context, got string, _, _ io.Writer) (int, error) {
		if got != strings.Repeat("a", len(code)) {
			t.Errorf("escaped source=%q", got)
		}
		return 0, nil
	}, exectools.Options{MaxInputBytes: len(code)})
	out, _ = receiptCallOptions(t, t.Context(), agent.ToolCall{Name: python.Name, Arguments: json.RawMessage(raw)}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{python}}, 1)
	decodedOutput(t, out)
}

func TestDefaultInputOutputAndDeadlineBounds(t *testing.T) {
	var entered atomic.Int32
	tool := pythonTool(t, func(ctx context.Context, _ string, stdout, _ io.Writer) (int, error) {
		entered.Add(1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
			t.Errorf("default deadline=%v present=%v", deadline, ok)
		}
		_, err := io.WriteString(stdout, strings.Repeat("x", (16<<10)+1))
		return 0, err
	}, exectools.Options{})
	out, _ := receiptCall(t, t.Context(), tool, map[string]any{"code": strings.Repeat("x", 64<<10)})
	value := decodedOutput(t, out)
	if len(value.Stdout) != 16<<10 || !value.Truncated || value.EncodingReplaced {
		t.Fatalf("default output bound: %+v", value)
	}
	out, _ = receiptCall(t, t.Context(), tool, map[string]any{"code": strings.Repeat("x", (64<<10)+1)})
	assertFailure(t, out, agent.ToolFailureArgumentInvalid)
	if entered.Load() != 1 {
		t.Fatalf("backend calls=%d", entered.Load())
	}
}

func TestOutputCombinedBudgetAndEncoding(t *testing.T) {
	for _, test := range []struct {
		name           string
		limit          int
		stdout, stderr []byte
		want           executionOutput
	}{
		{"exact", 4, []byte("ab"), []byte("cd"), executionOutput{Stdout: "ab", Stderr: "cd"}},
		{"shared cap", 4, []byte("abc"), []byte("def"), executionOutput{Stdout: "abc", Stderr: "d", Truncated: true}},
		{"empty after full", 2, []byte("ab"), nil, executionOutput{Stdout: "ab"}},
		{"invalid utf8", 8, []byte{'a', 255, 'b'}, []byte{254}, executionOutput{Stdout: "a\uFFFDb", Stderr: "\uFFFD", EncodingReplaced: true}},
		{"split rune", 2, []byte("雪"), nil, executionOutput{Stdout: "\uFFFD", Truncated: true, EncodingReplaced: true}},
		{"JSON escaping", 3, []byte("<\n\x00"), nil, executionOutput{Stdout: "<\n\x00"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool := commandTool(t, func(_ context.Context, _ []string, stdout, stderr io.Writer) (int, error) {
				for _, w := range []struct {
					writer io.Writer
					data   []byte
				}{{stdout, test.stdout}, {stderr, test.stderr}} {
					if n, err := w.writer.Write(w.data); n != len(w.data) || err != nil {
						t.Errorf("Write returned n=%d err=%v for %d bytes", n, err, len(w.data))
					}
				}
				return 0, nil
			}, exectools.Options{MaxOutputBytes: test.limit})
			out, _ := receiptCall(t, t.Context(), tool, map[string]any{"args": []string{}})
			if got := decodedOutput(t, out); got != test.want {
				t.Fatalf("got=%+v want=%+v", got, test.want)
			}
			if !utf8.Valid(out.Result.StructuredContent) || len(out.Result.StructuredContent) > 6*test.limit+256 {
				t.Fatalf("unbounded or invalid output: %q", out.Result.StructuredContent)
			}
		})
	}
}

func TestConcurrentOutputAndClosedLateWriters(t *testing.T) {
	var savedOut, savedErr io.Writer
	tool := commandTool(t, func(_ context.Context, _ []string, stdout, stderr io.Writer) (int, error) {
		savedOut, savedErr = stdout, stderr
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Go(func() {
				writer := stdout
				if i%2 != 0 {
					writer = stderr
				}
				for range 64 {
					if n, err := io.WriteString(writer, "abc"); n != 3 || err != nil {
						t.Errorf("write=%d, %v", n, err)
					}
				}
			})
		}
		wg.Wait()
		return 0, nil
	}, exectools.Options{MaxOutputBytes: 127})
	out, _ := receiptCall(t, t.Context(), tool, map[string]any{"args": []string{}})
	value := decodedOutput(t, out)
	if len(value.Stdout)+len(value.Stderr) != 127 || !value.Truncated || value.EncodingReplaced {
		t.Fatalf("combined concurrent output=%+v", value)
	}
	before := string(out.Result.StructuredContent)
	var wg sync.WaitGroup
	for _, writer := range []io.Writer{savedOut, savedErr} {
		wg.Go(func() {
			if n, err := writer.Write([]byte("late")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
				t.Errorf("late write=%d, %v", n, err)
			}
		})
	}
	wg.Wait()
	if string(out.Result.StructuredContent) != before {
		t.Fatal("late write changed returned result")
	}
}

func TestWritersRacingBackendReturnAreClosedSafely(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan error, 1)
	tool := commandTool(t, func(_ context.Context, _ []string, stdout, _ io.Writer) (int, error) {
		go func() {
			close(started)
			for {
				if n, err := stdout.Write([]byte("x")); err != nil {
					if n != 0 || !errors.Is(err, io.ErrClosedPipe) {
						stopped <- fmt.Errorf("closed write=%d, %w", n, err)
					} else {
						stopped <- nil
					}
					return
				}
			}
		}()
		<-started
		return 0, nil
	}, exectools.Options{MaxOutputBytes: 16})
	out, _ := receiptCall(t, t.Context(), tool, map[string]any{"args": []string{}})
	value := decodedOutput(t, out)
	if len(value.Stdout) > 16 || value.Stderr != "" || value.EncodingReplaced {
		t.Fatalf("racing output=%+v", value)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer remained open after return")
	}
}

func TestExitStatusAndSafeBackendError(t *testing.T) {
	for _, code := range []int{-1, 0, 1, 255, 256, 10000} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			tool := commandTool(t, func(_ context.Context, _ []string, stdout, stderr io.Writer) (int, error) {
				io.WriteString(stdout, "partial stdout")
				io.WriteString(stderr, "partial stderr")
				return code, nil
			}, exectools.Options{})
			out, _ := receiptCall(t, t.Context(), tool, map[string]any{"args": []string{}})
			value := decodedOutput(t, out)
			if value.ExitCode != code || value.Stdout != "partial stdout" || value.Stderr != "partial stderr" || out.IsError != (code != 0) || out.Result.IsError != (code != 0) {
				t.Fatalf("status/result lost: outcome=%+v value=%+v", out, value)
			}
			if code != 0 && (out.Failure == nil || out.Failure.Code != agent.ToolFailureToolReportedError) {
				t.Fatalf("failure=%+v", out.Failure)
			}
		})
	}
	cause := errors.New("SECRET_BACKEND_CONFIG")
	for _, mode := range []string{"backend error", "invalid exit"} {
		t.Run(mode, func(t *testing.T) {
			tool := commandTool(t, func(_ context.Context, _ []string, stdout, _ io.Writer) (int, error) {
				io.WriteString(stdout, "SECRET_PARTIAL_OUTPUT")
				if mode == "backend error" {
					return 0, cause
				}
				return -2, nil
			}, exectools.Options{})
			out, _ := receiptCall(t, t.Context(), tool, map[string]any{"args": []string{}})
			assertFailure(t, out, agent.ToolFailureResource)
			if mode == "backend error" && !errors.Is(out.Err, cause) {
				t.Fatal("backend error chain lost")
			}
			encoded, err := json.Marshal(out)
			if err != nil || strings.Contains(string(encoded), "SECRET_") {
				t.Fatalf("unsafe model-facing failure: %s; %v", encoded, err)
			}
		})
	}
}

func TestPermissionBeforeAndAfterBackend(t *testing.T) {
	for _, denyAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(denyAt), func(t *testing.T) {
			var checks, entered int
			denied := errors.New("revoked")
			tool := commandTool(t, func(_ context.Context, _ []string, stdout, _ io.Writer) (int, error) {
				entered++
				io.WriteString(stdout, "SECRET_OUTPUT")
				return 0, nil
			}, exectools.Options{})
			out, h := receiptCallOptions(t, t.Context(), agent.ToolCall{Name: tool.Name, ParsedArgs: map[string]any{"args": []string{}}}, agent.RunToolCallOptions{
				Tools: []agent.ToolDefinition{tool},
				CheckToolPermission: func(context.Context, agent.BeforeToolCallContext) error {
					checks++
					if checks == denyAt {
						return denied
					}
					return nil
				},
			}, 1)
			assertFailure(t, out, agent.ToolFailurePolicyDenied)
			if !errors.Is(out.Err, denied) || checks != denyAt || (denyAt < 3 && entered != 0) || (denyAt == 3 && entered != 1) {
				t.Fatalf("checks=%d entered=%d error=%v", checks, entered, out.Err)
			}
			encoded, _ := json.Marshal(out.Result)
			if strings.Contains(string(encoded), "SECRET_OUTPUT") {
				t.Fatal("output leaked after revocation")
			}
			for _, fact := range h.Snapshot() {
				if fact.Committed() || !fact.Completed() {
					t.Fatalf("receipt=%+v", fact)
				}
			}
		})
	}
}

func TestCancellationAndTimeout(t *testing.T) {
	for _, mode := range []string{"before", "during permission", "backend returns nil", "cooperative timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered := 0
			options := exectools.Options{}
			if mode == "cooperative timeout" {
				options.Timeout = 10 * time.Millisecond
			}
			tool := commandTool(t, func(ctx context.Context, _ []string, stdout, _ io.Writer) (int, error) {
				entered++
				if mode == "cooperative timeout" {
					<-ctx.Done()
					return -1, ctx.Err()
				}
				io.WriteString(stdout, "SECRET_AFTER_CANCEL")
				cancel()
				return 0, nil
			}, options)
			callOptions := agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}
			if mode == "before" {
				cancel()
			}
			if mode == "during permission" {
				callOptions.CheckToolPermission = func(context.Context, agent.BeforeToolCallContext) error { cancel(); return nil }
			}
			out, _ := receiptCallOptions(t, ctx, agent.ToolCall{Name: tool.Name, ParsedArgs: map[string]any{"args": []string{}}}, callOptions, 1)
			cause := error(context.Canceled)
			failureCode := agent.ToolFailureCanceled
			if mode == "cooperative timeout" {
				cause = context.DeadlineExceeded
				failureCode = agent.ToolFailureDeadline
			}
			assertFailure(t, out, failureCode)
			if !errors.Is(out.Err, cause) || !out.IsError {
				t.Fatalf("cancel/timeout result=%+v", out)
			}
			if (mode == "before" || mode == "during permission") && entered != 0 {
				t.Fatal("backend entered after cancellation")
			}
			if (mode == "backend returns nil" || mode == "cooperative timeout") && entered != 1 {
				t.Fatalf("backend calls=%d", entered)
			}
			if mode == "cooperative timeout" && out.Failure.Reason != "execution_deadline" {
				t.Fatalf("deadline reason=%+v", out.Failure)
			}
			encoded, _ := json.Marshal(out.Result)
			if strings.Contains(string(encoded), "SECRET_AFTER_CANCEL") {
				t.Fatal("output leaked after cancellation")
			}
		})
	}
}

func TestConcurrentTenantClosuresDoNotShareExecutorsOrOutput(t *testing.T) {
	var wg sync.WaitGroup
	for tenant := range 24 {
		name := fmt.Sprintf("tenant-%d", tenant)
		tool := commandTool(t, func(_ context.Context, args []string, stdout, _ io.Writer) (int, error) {
			if len(args) != 1 || args[0] != name {
				return -1, fmt.Errorf("cross-tenant args: %q", args)
			}
			_, err := io.WriteString(stdout, name)
			return 0, err
		}, exectools.Options{})
		wg.Go(func() {
			out, h, err := agent.RunToolCallWithReceipts(t.Context(), agent.ToolCall{ID: name, Name: tool.Name, ParsedArgs: map[string]any{"args": []string{name}}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}, 1)
			if err != nil || out.Err != nil {
				t.Errorf("%s wrapper=%v outcome=%+v", name, err, out)
				return
			}
			var value executionOutput
			if err := json.Unmarshal(out.Result.StructuredContent, &value); err != nil || value.Stdout != name || value.Stderr != "" || value.Truncated {
				t.Errorf("%s result=%+v err=%v", name, value, err)
			}
			facts := h.Snapshot()
			if len(facts) != 1 || facts[0].CallID() != name || facts[0].Committed() || !facts[0].Completed() {
				t.Errorf("%s facts=%+v", name, facts)
			}
		})
	}
	wg.Wait()
}
