package fstools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/internal/jsontext"
)

// ReadOptions contains host-controlled limits for one complete text file.
// Zero selects defaults; unsupported limits fail construction.
type ReadOptions struct {
	MaxBytes       int // Default 64 KiB; ceiling 1 MiB.
	MaxResultBytes int // Default 128 KiB; range 256 bytes to 8 MiB.
}

// ReadDefinition explicitly opts into reading regular files from a trusted local
// workspace. Definitions still returns only ls; upgrading never adds read
// authority to existing callers. The caller owns root. See the package README
// for Linux/procfs, filesystem trust, final-symlink and cancellation limitations.
// Construction performs no filesystem I/O.
func ReadDefinition(root *os.Root, options ReadOptions) (agent.ToolDefinition, error) {
	if !supported {
		return agent.ToolDefinition{}, ErrUnsupportedPlatform
	}
	if root == nil {
		return agent.ToolDefinition{}, errors.New("fstools: root is required")
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 64 << 10
	}
	if options.MaxResultBytes == 0 {
		options.MaxResultBytes = 128 << 10
	}
	if options.MaxBytes < 1 || options.MaxBytes > 1<<20 || options.MaxResultBytes < 256 || options.MaxResultBytes > 8<<20 {
		return agent.ToolDefinition{}, errors.New("fstools: unsupported read limit")
	}
	return agent.ToolDefinition{
		Name: "read", Description: "Read one complete, bounded UTF-8 text file from the host-authorized local workspace. Paths are relative; final symlinks, nonregular files, NUL bytes or invalid UTF-8, and files exceeding the host limits are rejected. No partial content is returned.",
		Parameters:       map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "maxLength": 4096}}, "required": []string{"path"}, "additionalProperties": false},
		OutputSchema:     map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}, "additionalProperties": false},
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
			return read(ctx, execution, root, args, options)
		},
	}, nil
}

type readOutput struct {
	Text string `json:"text"`
}

func read(ctx context.Context, execution agent.ToolExecutionContext, root *os.Root, args arguments, options ReadOptions) (agent.ToolResult, error) {
	data, err := readTextFile(ctx, execution, root, args.Path, options.MaxBytes)
	if err != nil {
		return agent.ToolResult{}, err
	}
	encoded, _ := json.Marshal(readOutput{Text: string(data)})
	if len(encoded) > options.MaxResultBytes {
		return agent.ToolResult{}, failure(agent.ToolFailureResource, "fstools: encoded text exceeds result limit", nil)
	}
	if err := permission(ctx, execution); err != nil {
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: string(encoded)}}, StructuredContent: encoded}, nil
}

// readTextFile is shared by read, grep, preview and explicitly selected edit.
// It validates the entire bounded file before a caller uses its content.
func readTextFile(ctx context.Context, execution agent.ToolExecutionContext, root *os.Root, filename string, maxBytes int) ([]byte, error) {
	if err := permission(ctx, execution); err != nil {
		return nil, err
	}
	file, err := openRegular(ctx, execution, root, filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// The extra byte detects growth beyond the cap without trusting Stat.Size.
	data := make([]byte, maxBytes+1)
	used := 0
	for used < len(data) {
		if err := permission(ctx, execution); err != nil {
			return nil, err
		}
		end := min(used+8192, len(data))
		n, err := file.Read(data[used:end])
		used += n
		if used > maxBytes {
			return nil, failure(agent.ToolFailureResource, "fstools: file exceeds read limit", nil)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, failure(agent.ToolFailureResource, "fstools: cannot read file", err)
		}
		if n == 0 {
			return nil, failure(agent.ToolFailureResource, "fstools: file read made no progress", io.ErrNoProgress)
		}
	}
	data = data[:used]
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, failure(agent.ToolFailureResource, "fstools: file is not supported UTF-8 text", nil)
	}
	return data, nil
}
