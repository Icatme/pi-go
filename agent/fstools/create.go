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

// CreateOptions bounds explicitly selected create-only UTF-8 file writes.
// The host chooses the root; the model cannot select modes or overwrite policy.
type CreateOptions struct{ MaxBytes int } // Default 64 KiB; ceiling 1 MiB.

// CreateFileDefinition creates only new files. Existing names are never replaced;
// parent directories must exist. Linux O_TMPFILE and trusted procfs are required.
// Nothing is enabled by default. This is not a filesystem transaction or sandbox.
func CreateFileDefinition(root *os.Root, options CreateOptions) (agent.ToolDefinition, error) {
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
		return agent.ToolDefinition{}, errors.New("fstools: unsupported create limit")
	}
	maxArgs := 6*options.MaxBytes + (32 << 10)
	parseArgs := func(raw []byte) (createArguments, error) { return parseCreate(raw, options.MaxBytes, maxArgs) }
	return agent.ToolDefinition{Name: "create_file", Description: "Create one new UTF-8 text file under the host-authorized root. Never overwrites any existing name and never creates parent directories. Existing, unsupported or oversized targets fail. After an uncertain or committed failure, inspect host execution facts and destination before retrying; no automatic rollback.",
		Parameters:       map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "maxLength": 4096}, "content": map[string]any{"type": "string", "maxLength": options.MaxBytes}}, "required": []string{"path", "content"}, "additionalProperties": false},
		OutputSchema:     map[string]any{"type": "object", "properties": map[string]any{"created": map[string]any{"type": "boolean"}, "durability": map[string]any{"type": "string", "enum": []string{"synced", "unknown"}}}, "required": []string{"created", "durability"}, "additionalProperties": false},
		MaxArgumentBytes: maxArgs,
		ParseArguments:   parseCallArguments(parseArgs),
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
			if err := permission(ctx, in); err != nil {
				return agent.ToolResult{}, err
			}
			pending, err := newPendingCreate(root, args.Path)
			if err != nil {
				return agent.ToolResult{}, createFailure(false, false, "fstools: cannot prepare new file; destination was not published", err)
			}
			return publishCreate(ctx, in, pending, []byte(*args.Content))
		},
	}, nil
}

type createArguments struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

func parseCreate(raw []byte, maxBytes, maxArgs int) (createArguments, error) {
	var args createArguments
	bad := func(err error) (createArguments, error) {
		return args, failure(agent.ToolFailureArgumentInvalid, "fstools: expected a relative file path and bounded UTF-8 content without NUL", err)
	}
	if len(raw) > maxArgs {
		return bad(errors.New("argument limit"))
	}
	args.Content = new(string)
	if err := decodeStringArguments(raw, map[string]*string{"path": &args.Path, "content": args.Content}); err != nil {
		return bad(err)
	}
	if len(*args.Content) > maxBytes || strings.ContainsRune(*args.Content, 0) {
		return bad(errors.New("invalid content"))
	}
	if args.Path == "." || strings.HasSuffix(args.Path, "/") || strings.HasSuffix(args.Path, "/.") {
		return bad(errors.New("expected file basename"))
	}
	encoded, _ := json.Marshal(arguments{Path: args.Path})
	p, err := parse(encoded)
	if err != nil {
		return bad(err)
	}
	if p.Path == "." {
		return bad(errors.New("expected file basename"))
	}
	args.Path = p.Path
	return args, nil
}

// pendingCreation is a private, per-call staging lifecycle, also used for fault
// injection. It is not an application backend or a pluggable authority boundary.
type pendingCreation interface {
	Write([]byte) (int, error)
	Sync() error
	// Mark the attempt immediately before the syscall that publishes to the destination.
	Publish(markAttempt func()) error
	SyncDirectory() error
	Close() error
}

func createFailure(committed, attempted bool, message string, cause error) error {
	return publicationFailure("create", committed, attempted, message, cause)
}
func publishCreate(ctx context.Context, in agent.ToolExecutionContext, pending pendingCreation, data []byte) (agent.ToolResult, error) {
	return publishFile(ctx, in, pending, data, "create")
}
