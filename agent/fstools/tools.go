// Package fstools provides opt-in, bounded filesystem tools. It registers
// nothing in the Agent core and is not an operating-system sandbox.
package fstools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/internal/jsontext"
)

// ErrUnsupportedPlatform indicates that directory-only opens have not been
// implemented and verified for this platform. The first release supports Linux.
var ErrUnsupportedPlatform = errors.New("fstools: unsupported platform")

// Options contains host-controlled limits, never model-controlled authority.
// Zero selects defaults. Negative values and values above ceilings fail.
type Options struct {
	MaxEntries     int // Default 256; ceiling 4096.
	MaxResultBytes int // Default 64 KiB; range 256 bytes to 1 MiB.
}

// Definitions creates only the ls tool. It does no filesystem I/O. The caller
// owns root, must keep it open during calls, and must close it afterward. Use a
// distinct root/tool set per trusted authorization scope. A nil permission hook
// means that the host has authorized all reads allowed by this root.
func Definitions(root *os.Root, options Options) ([]agent.ToolDefinition, error) {
	if !supported {
		return nil, ErrUnsupportedPlatform
	}
	if root == nil {
		return nil, errors.New("fstools: root is required")
	}
	if options.MaxEntries == 0 {
		options.MaxEntries = 256
	}
	if options.MaxResultBytes == 0 {
		options.MaxResultBytes = 64 << 10
	}
	if options.MaxEntries < 1 || options.MaxEntries > 4096 || options.MaxResultBytes < 256 || options.MaxResultBytes > 1<<20 {
		return nil, errors.New("fstools: unsupported limit")
	}
	return []agent.ToolDefinition{{
		Name: "ls", Description: "List one directory under the host-authorized root. Paths are relative; use . for the root. Results are bounded, nonrecursive, in filesystem order, and may be truncated.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "maxLength": 4096}}, "required": []string{"path"}, "additionalProperties": false},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"entries":   map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "type": map[string]any{"type": "string", "enum": []string{"directory", "symlink", "file", "other"}}}, "required": []string{"name", "type"}, "additionalProperties": false}},
			"truncated": map[string]any{"type": "boolean"}}, "required": []string{"entries", "truncated"}, "additionalProperties": false},
		MaxArgumentBytes: 32 << 10,
		ParseArguments: func(call agent.ToolCall) (any, error) {
			raw := call.Arguments
			if call.ParsedArgs != nil {
				if err := jsontext.ValidateStrings(call.ParsedArgs); err != nil {
					return nil, invalid(err)
				}
				var err error
				raw, err = json.Marshal(call.ParsedArgs)
				if err != nil {
					return nil, invalid(err)
				}
			}
			return parse(raw)
		},
		Execute: func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
			if err := jsontext.ValidateStrings(execution.Args); err != nil {
				return agent.ToolResult{}, invalid(err)
			}
			raw, err := json.Marshal(execution.Args)
			if err != nil {
				return agent.ToolResult{}, invalid(err)
			}
			args, err := parse(raw)
			if err != nil {
				return agent.ToolResult{}, err
			}
			return list(ctx, execution, root, args, options)
		},
	}}, nil
}

type arguments struct {
	Path string `json:"path"`
}
type entry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
type output struct {
	Entries   []entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

func parse(raw []byte) (arguments, error) {
	var args arguments
	if len(raw) > 32<<10 {
		return args, invalid(errors.New("argument limit"))
	}
	if err := jsontext.ValidateUnicode(raw); err != nil {
		return args, invalid(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return args, invalid(err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return args, invalid(errors.New("expected one argument object"))
	}
	p := args.Path
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) || strings.ContainsAny(p, "\\:\x00") || strings.HasPrefix(p, "/") {
		return args, invalid(errors.New("invalid relative path"))
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return args, invalid(errors.New("parent path component"))
		}
	}
	args.Path = path.Clean(p)
	return args, nil
}

func invalid(err error) error {
	return failure(agent.ToolFailureArgumentInvalid, "fstools: expected a relative path without parent components", err)
}
func failure(code agent.ToolFailureCode, message string, err error) error {
	return &agent.ToolExecutionError{Code: code, Message: message, Err: err, Execution: agent.ToolExecutionInfo{Remote: agent.ToolRemoteNotApplicable}}
}
func permission(ctx context.Context, execution agent.ToolExecutionContext) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if execution.CheckPermission != nil {
		if err := execution.CheckPermission(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return failure(agent.ToolFailurePolicyDenied, "fstools: permission denied", err)
		}
	}
	return ctx.Err()
}

func list(ctx context.Context, execution agent.ToolExecutionContext, root *os.Root, args arguments, options Options) (agent.ToolResult, error) {
	if err := permission(ctx, execution); err != nil {
		return agent.ToolResult{}, err
	}
	directory, err := openDirectory(root, args.Path)
	if err != nil {
		return agent.ToolResult{}, failure(agent.ToolFailureResource, "fstools: cannot open directory", err)
	}
	defer directory.Close()
	result := output{Entries: make([]entry, 0), Truncated: false}
	envelope, _ := json.Marshal(result)
	resultBytes := len(envelope)
	for {
		if err := permission(ctx, execution); err != nil {
			return agent.ToolResult{}, err
		}
		items, err := directory.ReadDir(1)
		if err != nil && !errors.Is(err, io.EOF) {
			return agent.ToolResult{}, failure(agent.ToolFailureResource, "fstools: cannot list directory", err)
		}
		if len(items) == 0 {
			break
		}
		if len(result.Entries) == options.MaxEntries {
			result.Truncated = true
			break
		}
		item := items[0]
		if !utf8.ValidString(item.Name()) {
			return agent.ToolResult{}, failure(agent.ToolFailureResource, "fstools: directory contains a non-UTF-8 name", nil)
		}
		kind := "other"
		switch {
		case item.IsDir():
			kind = "directory"
		case item.Type()&os.ModeSymlink != 0:
			kind = "symlink"
		case item.Type().IsRegular():
			kind = "file"
		}
		next := entry{Name: item.Name(), Type: kind}
		// Count encoded names, commas and the envelope, not raw filename bytes.
		// The false spelling is longer than true, so truncation fits too.
		encoded, _ := json.Marshal(next)
		added := len(encoded)
		if len(result.Entries) > 0 {
			added++
		}
		if resultBytes+added > options.MaxResultBytes {
			result.Truncated = true
			break
		}
		resultBytes += added
		result.Entries = append(result.Entries, next)
	}
	if err := permission(ctx, execution); err != nil {
		return agent.ToolResult{}, err
	}
	encoded, _ := json.Marshal(result)
	return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: string(encoded)}}, StructuredContent: encoded}, nil
}
