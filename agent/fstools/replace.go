package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/internal/jsontext"
)

// ReplaceOptions bounds explicit ordinary workspace replacement writes.
type ReplaceOptions struct{ MaxBytes int } // Default 64 KiB; ceiling 1 MiB.
// AtomicReplaceDefinition opts into create-or-replace publication. It is not CAS,
// a transaction, or isolation from malicious directory writers. Existing names
// observed as symlinks or nonregular are rejected; later name changes may be replaced.
// Replacing an inode does not preserve ownership, mode, ACLs, xattrs or hard links.
// Unsupported platforms/filesystems fail closed. No tool is enabled by default.
func AtomicReplaceDefinition(root *os.Root, options ReplaceOptions) (agent.ToolDefinition, error) {
	if !supported {
		return agent.ToolDefinition{}, ErrUnsupportedPlatform
	}
	if root == nil {
		return agent.ToolDefinition{}, errors.New("fstools: root is required")
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 64 << 10
	}
	if options.MaxBytes < 1 || options.MaxBytes > 1<<20 {
		return agent.ToolDefinition{}, errors.New("fstools: unsupported replace limit")
	}
	maxArgs := 6*options.MaxBytes + (32 << 10)
	parseArgs := func(raw []byte) (createArguments, error) { return parseCreate(raw, options.MaxBytes, maxArgs) }
	return agent.ToolDefinition{Name: "atomic_replace", Description: "Publish complete UTF-8 content at one workspace file path, creating or replacing the name atomically. Explicit overwrite authority: concurrent external edits may be lost (last writer wins), not CAS. Parent must exist. New inode has at most 0600 permissions; old metadata/hard-link identity is not preserved. Host must control the directory against malicious writers. Inspect execution facts after uncertain/committed failures; no rollback.",
		Parameters:       map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "maxLength": 4096}, "content": map[string]any{"type": "string", "maxLength": options.MaxBytes}}, "required": []string{"path", "content"}, "additionalProperties": false},
		OutputSchema:     map[string]any{"type": "object", "properties": map[string]any{"published": map[string]any{"type": "boolean"}, "durability": map[string]any{"type": "string", "enum": []string{"synced", "unknown"}}}, "required": []string{"published", "durability"}, "additionalProperties": false},
		MaxArgumentBytes: maxArgs,
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
			return parseArgs(raw)
		},
		Execute: func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
			if err := jsontext.ValidateStrings(in.Args); err != nil {
				return agent.ToolResult{}, invalid(err)
			}
			raw, err := json.Marshal(in.Args)
			if err != nil {
				return agent.ToolResult{}, invalid(err)
			}
			args, err := parseArgs(raw)
			if err != nil {
				return agent.ToolResult{}, err
			}
			if err := permission(ctx, in); err != nil {
				return agent.ToolResult{}, err
			}
			pending, err := newPendingReplace(root, args.Path, func() error { return permission(ctx, in) })
			if err != nil {
				return agent.ToolResult{}, publicationFailure("replace", false, false, "fstools: cannot prepare replacement; new content was not published", err)
			}
			return publishFile(ctx, in, pending, []byte(*args.Content), "replace")
		},
	}, nil
}
