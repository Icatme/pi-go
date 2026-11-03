package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/internal/jsontext"
)

// EditOptions bounds the source, old/new text and resulting candidate independently.
type EditOptions struct{ MaxBytes int } // Default 64 KiB; ceiling 1 MiB.
// EditDefinition explicitly enables one unique exact UTF-8 replacement followed
// by ordinary atomic publication. It can overwrite concurrent updates: not CAS.
// Metadata, root, platform and trusted-directory restrictions match atomic_replace.
func EditDefinition(root *os.Root, options EditOptions) (agent.ToolDefinition, error) {
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
		return agent.ToolDefinition{}, errors.New("fstools: unsupported edit limit")
	}
	maxArgs := 12*options.MaxBytes + (32 << 10)
	parseArgs := func(raw []byte) (previewArguments, error) { return parseEdit(raw, options.MaxBytes, maxArgs) }
	return agent.ToolDefinition{Name: "edit", Description: "Edit one file by replacing exactly one occurrence of old_text with different new_text, counting overlapping occurrences as ambiguous. Matches raw UTF-8 bytes; no BOM/newline normalization. Reads the file anew, then publishes complete replacement content atomically. Concurrent updates may be lost: last writer wins, not CAS or guarded preview application. New inode at most 0600; old metadata/hard-link identity is not preserved. Requires host-controlled workspace against malicious writers. Inspect execution facts after uncertain or committed failures; no rollback.",
		Parameters:       map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "maxLength": 4096}, "old_text": map[string]any{"type": "string", "minLength": 1, "maxLength": options.MaxBytes}, "new_text": map[string]any{"type": "string", "description": "May be empty; must differ from old_text.", "maxLength": options.MaxBytes}}, "required": []string{"path", "old_text", "new_text"}, "additionalProperties": false},
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
			if err := executionreceipt.RequireCapture(ctx); err != nil {
				return agent.ToolResult{}, failure(agent.ToolFailurePolicyDenied, "fstools: receipt-aware execution is required before writes", err)
			}
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
			data, err := readTextFile(ctx, in, root, args.Path, options.MaxBytes)
			if err != nil {
				return agent.ToolResult{}, err
			}
			candidate, _, err := exactEditCandidate(ctx, in, data, args, options.MaxBytes)
			if err != nil {
				return agent.ToolResult{}, err
			}
			if err := permission(ctx, in); err != nil {
				return agent.ToolResult{}, err
			}
			pending, err := newPendingReplace(root, args.Path, func() error { return permission(ctx, in) })
			if err != nil {
				return agent.ToolResult{}, publicationFailure("edit", false, false, "fstools: cannot prepare replacement; new content was not published", err)
			}
			return publishFile(ctx, in, pending, []byte(candidate), "edit")
		},
	}, nil
}

func parseEdit(raw []byte, maxBytes, maxArgs int) (previewArguments, error) {
	args, err := parsePreview(raw, maxBytes, maxArgs)
	if err != nil {
		return args, err
	}
	// Inspect the original path spelling: cleaning must not turn a directory
	// request such as file/. into an overwrite of file.
	var original struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &original); err != nil {
		return args, invalid(err)
	}
	if original.Path == "." || strings.HasSuffix(original.Path, "/") || strings.HasSuffix(original.Path, "/.") || args.Path == "." {
		return args, failure(agent.ToolFailureArgumentInvalid, "fstools: expected a relative file path", nil)
	}
	return args, nil
}
