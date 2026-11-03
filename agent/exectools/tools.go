// Package exectools adapts explicitly supplied host executors to Agent tools.
// It does not start processes, install interpreters or provide an OS sandbox.
package exectools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/internal/jsontext"
)

// CommandExecutor runs arguments against a host-selected program/backend.
// It must enforce its own filesystem, environment, network and resource policy,
// and own termination/reaping. Return only when its synchronous work is done;
// a returned error or canceled context does not prove no effects or job exit.
// Writers are bounded, concurrent-safe and invalid after return. Do not retry
// internally without an explicit host policy. Exit codes must be nonnegative or -1; -1
// means no normal exit status. ReportLocalCommit only for a confirmed local
// effect, never merely because execution started or exited successfully.
type CommandExecutor func(context.Context, []string, io.Writer, io.Writer) (int, error)

// PythonExecutor runs bounded source using a host-selected Python environment.
// It has the same lifecycle and policy obligations as CommandExecutor. The
// factory never locates, installs or starts Python, and grants no import policy.
type PythonExecutor func(context.Context, string, io.Writer, io.Writer) (int, error)

// Options bounds adapter input/output and requests a deadline. Timeout is not
// a kill guarantee: a noncooperative executor can remain active indefinitely.
// Zero values select 64 KiB input, 16 KiB combined output and 30 seconds.
type Options struct {
	MaxInputBytes  int           // 1..256 KiB, total decoded input bytes.
	MaxOutputBytes int           // 1..64 KiB, combined raw stdout/stderr prefix bytes.
	Timeout        time.Duration // Positive, at most ten minutes.
}

type commandArguments struct {
	Args []string `json:"args"`
}
type pythonArguments struct {
	Code *string `json:"code"`
}
type output struct {
	ExitCode         int    `json:"exit_code"`
	Stdout           string `json:"stdout"`
	Stderr           string `json:"stderr"`
	Truncated        bool   `json:"truncated"`
	EncodingReplaced bool   `json:"encoding_replaced"`
}

// CommandDefinition returns run_command. Only args are model-controlled; the
// executable, environment, working directory and backend are fixed by host.
// Empty args is valid. This tool is not added to any preset or core default.
func CommandDefinition(execute CommandExecutor, options Options) (agent.ToolDefinition, error) {
	if execute == nil {
		return agent.ToolDefinition{}, errors.New("exectools: executor required")
	}
	return definition("run_command", false, options, func(ctx context.Context, arg any, out, err io.Writer) (int, error) {
		return execute(ctx, arg.(commandArguments).Args, out, err)
	})
}

// PythonDefinition returns run_python for nonempty UTF-8 source. Python is not
// supplied by pi-go. Host code must explicitly supply and isolate its backend.
func PythonDefinition(execute PythonExecutor, options Options) (agent.ToolDefinition, error) {
	if execute == nil {
		return agent.ToolDefinition{}, errors.New("exectools: executor required")
	}
	return definition("run_python", true, options, func(ctx context.Context, arg any, out, err io.Writer) (int, error) {
		return execute(ctx, *arg.(pythonArguments).Code, out, err)
	})
}

func definition(name string, python bool, o Options, execute func(context.Context, any, io.Writer, io.Writer) (int, error)) (agent.ToolDefinition, error) {
	if o.MaxInputBytes == 0 {
		o.MaxInputBytes = 64 << 10
	}
	if o.MaxOutputBytes == 0 {
		o.MaxOutputBytes = 16 << 10
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.MaxInputBytes < 1 || o.MaxInputBytes > 256<<10 || o.MaxOutputBytes < 1 || o.MaxOutputBytes > 64<<10 || o.Timeout < 0 || o.Timeout > 10*time.Minute {
		return agent.ToolDefinition{}, errors.New("exectools: invalid limits")
	}
	maxArgs := 6*o.MaxInputBytes + 4096
	parse := func(raw []byte) (any, error) {
		if len(raw) > maxArgs {
			return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", errors.New("input limit"))
		}
		if err := jsontext.ValidateUnicode(raw); err != nil {
			return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", err)
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		key := "args"
		if python {
			key = "code"
		}
		start, err := d.Token()
		if err != nil || start != json.Delim('{') {
			return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", err)
		}
		field, err := d.Token()
		if err != nil || field != key {
			return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", err)
		}
		var a any
		if python {
			var value *string
			if err := d.Decode(&value); err != nil {
				return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", err)
			}
			if value == nil || len(*value) == 0 || len(*value) > o.MaxInputBytes || strings.ContainsRune(*value, 0) {
				return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", nil)
			}
			a = pythonArguments{Code: value}
		} else {
			var values []*string
			if err := d.Decode(&values); err != nil {
				return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", err)
			}
			if values == nil || len(values) > 256 {
				return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", nil)
			}
			args := make([]string, len(values))
			n := 0
			for i, v := range values {
				if v == nil {
					return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", nil)
				}
				n += len(*v)
				if n > o.MaxInputBytes || strings.ContainsRune(*v, 0) {
					return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", nil)
				}
				args[i] = *v
			}
			a = commandArguments{Args: args}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", err)
		}
		if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
			return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", err)
		}
		return a, nil
	}
	rawArgs := func(v any) ([]byte, error) {
		if err := jsontext.ValidateStrings(v); err != nil {
			return nil, err
		}
		return json.Marshal(v)
	}
	property := "args"
	schema := map[string]any{"type": "array", "maxItems": 256, "items": map[string]any{"type": "string", "maxLength": o.MaxInputBytes}}
	if python {
		property = "code"
		schema = map[string]any{"type": "string", "minLength": 1, "maxLength": o.MaxInputBytes}
	}
	return agent.ToolDefinition{
		Name: name, Description: "Invoke an explicitly configured host executor. Execution may have side effects; this adapter is not an OS sandbox. A deadline requests cancellation, not guaranteed termination. Output is a bounded prefix, not redacted. On failure inspect host receipts/backend status before any retry.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{property: schema}, "required": []string{property}, "additionalProperties": false}, MaxArgumentBytes: maxArgs,
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"exit_code": map[string]any{"type": "integer", "minimum": -1},
			"stdout":    map[string]any{"type": "string"}, "stderr": map[string]any{"type": "string"},
			"truncated": map[string]any{"type": "boolean"}, "encoding_replaced": map[string]any{"type": "boolean"},
		}, "required": []string{"exit_code", "stdout", "stderr", "truncated", "encoding_replaced"}, "additionalProperties": false},
		ParseArguments: func(call agent.ToolCall) (any, error) {
			raw := call.Arguments
			if call.ParsedArgs != nil {
				var err error
				raw, err = rawArgs(call.ParsedArgs)
				if err != nil {
					return nil, fail(agent.ToolFailureArgumentInvalid, "invalid_input", err)
				}
			}
			return parse(raw)
		},
		Execute: func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
			if err := executionreceipt.RequireCapture(ctx); err != nil {
				return agent.ToolResult{}, fail(agent.ToolFailurePolicyDenied, "capture_required", err)
			}
			raw, err := rawArgs(in.Args)
			if err != nil {
				return agent.ToolResult{}, fail(agent.ToolFailureArgumentInvalid, "invalid_input", err)
			}
			arg, err := parse(raw)
			if err != nil {
				return agent.ToolResult{}, err
			}
			ctx, cancel := context.WithTimeout(ctx, o.Timeout)
			defer cancel()
			if err := permission(ctx, in); err != nil {
				return agent.ToolResult{}, err
			}
			capture := &boundedOutput{left: o.MaxOutputBytes}
			defer capture.close()
			code, err := execute(ctx, arg, outputWriter{capture, false}, outputWriter{capture, true})
			result := capture.close()
			result.ExitCode = code
			if err != nil {
				if ctx.Err() != nil {
					return agent.ToolResult{}, canceled(errors.Join(ctx.Err(), err))
				}
				return agent.ToolResult{}, fail(agent.ToolFailureResource, "executor_failed", err)
			}
			if err := permission(ctx, in); err != nil {
				return agent.ToolResult{}, err
			}
			if code < -1 {
				return agent.ToolResult{}, fail(agent.ToolFailureResource, "invalid_exit_status", nil)
			}
			encoded, err := json.Marshal(result)
			if err != nil || len(encoded) > 6*o.MaxOutputBytes+256 {
				return agent.ToolResult{}, fail(agent.ToolFailureResource, "result_limit", err)
			}
			return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: string(encoded)}}, StructuredContent: encoded, IsError: code != 0}, nil
		},
	}, nil
}
func fail(code agent.ToolFailureCode, reason string, err error) error {
	return &agent.ToolExecutionError{Code: code, Reason: reason, Message: "exectools: " + reason, Err: err}
}
func canceled(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fail(agent.ToolFailureDeadline, "execution_deadline", err)
	}
	return fail(agent.ToolFailureCanceled, "execution_canceled", err)
}
func permission(ctx context.Context, in agent.ToolExecutionContext) error {
	if err := ctx.Err(); err != nil {
		return canceled(err)
	}
	if in.CheckPermission != nil {
		if err := in.CheckPermission(ctx); err != nil {
			return fail(agent.ToolFailurePolicyDenied, "permission_denied", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return canceled(err)
	}
	return nil
}

type boundedOutput struct {
	mu                sync.Mutex
	left              int
	closed, truncated bool
	stdout, stderr    []byte
}
type outputWriter struct {
	capture *boundedOutput
	stderr  bool
}

func (w outputWriter) Write(p []byte) (int, error) {
	c := w.capture
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	n := len(p)
	keep := min(n, c.left)
	c.left -= keep
	if keep < n {
		c.truncated = true
	}
	if w.stderr {
		c.stderr = append(c.stderr, p[:keep]...)
	} else {
		c.stdout = append(c.stdout, p[:keep]...)
	}
	return n, nil // Discard excess without accumulating or blocking pipe draining.
}
func (c *boundedOutput) close() output {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return output{Stdout: strings.ToValidUTF8(string(c.stdout), "\uFFFD"), Stderr: strings.ToValidUTF8(string(c.stderr), "\uFFFD"), Truncated: c.truncated, EncodingReplaced: !utf8.Valid(c.stdout) || !utf8.Valid(c.stderr)}
}
