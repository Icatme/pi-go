package fstools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/internal/jsontext"
)

// PreviewEditOptions bounds source, replacement, candidate and encoded output.
// These limits are host-selected, not model-controlled.
type PreviewEditOptions struct {
	MaxBytes       int // Default 64 KiB; ceiling 1 MiB, for source/old/new/candidate each.
	MaxResultBytes int // Default 128 KiB; range 512 bytes to 8 MiB.
}

// PreviewEditDefinition opts into read-only exact-replacement previews. It never
// writes a file or grants apply authority. Fingerprints describe the bytes read,
// not an atomic snapshot, a durable revision or a compare-and-swap guarantee.
func PreviewEditDefinition(root *os.Root, options PreviewEditOptions) (agent.ToolDefinition, error) {
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
	if options.MaxBytes < 1 || options.MaxBytes > 1<<20 || options.MaxResultBytes < 512 || options.MaxResultBytes > 8<<20 {
		return agent.ToolDefinition{}, errors.New("fstools: unsupported preview limit")
	}
	maxArgs := 12*options.MaxBytes + (32 << 10)
	return agent.ToolDefinition{Name: "preview_edit", Description: "Preview one unique exact UTF-8 byte-string replacement in one file; never writes. old_text must occur exactly once, counting overlapping occurrences; new_text must differ. Returns full bounded candidate text, match line and SHA-256 fingerprints of bytes read/candidate; these are not CAS tokens or snapshot guarantees. No BOM, whitespace or newline normalization; full candidate may contain sensitive data.",
		Parameters:       map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "maxLength": 4096}, "old_text": map[string]any{"type": "string", "minLength": 1, "maxLength": options.MaxBytes}, "new_text": map[string]any{"type": "string", "description": "Replacement text; may be empty, must differ from old_text.", "maxLength": options.MaxBytes}}, "required": []string{"path", "old_text", "new_text"}, "additionalProperties": false},
		OutputSchema:     map[string]any{"type": "object", "properties": map[string]any{"preview_only": map[string]any{"type": "boolean"}, "original_sha256": map[string]any{"type": "string"}, "candidate_sha256": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}, "match_line": map[string]any{"type": "integer"}}, "required": []string{"preview_only", "original_sha256", "candidate_sha256", "content", "match_line"}, "additionalProperties": false},
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
			return parsePreview(raw, options.MaxBytes, maxArgs)
		},
		Execute: func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
			if err := jsontext.ValidateStrings(in.Args); err != nil {
				return agent.ToolResult{}, invalid(err)
			}
			raw, err := json.Marshal(in.Args)
			if err != nil {
				return agent.ToolResult{}, invalid(err)
			}
			args, err := parsePreview(raw, options.MaxBytes, maxArgs)
			if err != nil {
				return agent.ToolResult{}, err
			}
			return previewEdit(ctx, in, root, args, options)
		},
	}, nil
}

type previewArguments struct {
	Path    string  `json:"path"`
	OldText string  `json:"old_text"`
	NewText *string `json:"new_text"`
}
type previewOutput struct {
	PreviewOnly     bool   `json:"preview_only"`
	OriginalSHA256  string `json:"original_sha256"`
	CandidateSHA256 string `json:"candidate_sha256"`
	Content         string `json:"content"`
	MatchLine       int    `json:"match_line"`
}

func parsePreview(raw []byte, maxBytes, maxArgs int) (previewArguments, error) {
	var args previewArguments
	bad := func(err error) (previewArguments, error) {
		return args, failure(agent.ToolFailureArgumentInvalid, "fstools: expected relative path, nonempty old_text and different bounded new_text", err)
	}
	if len(raw) > maxArgs {
		return bad(errors.New("argument limit"))
	}
	if err := jsontext.ValidateUnicode(raw); err != nil {
		return bad(err)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&args); err != nil {
		return bad(err)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return bad(errors.New("expected one object"))
	}
	if args.OldText == "" || args.NewText == nil || args.OldText == *args.NewText || len(args.OldText) > maxBytes || len(*args.NewText) > maxBytes || strings.ContainsRune(args.OldText, 0) || strings.ContainsRune(*args.NewText, 0) {
		return bad(errors.New("invalid replacement"))
	}
	pathRaw, _ := json.Marshal(arguments{Path: args.Path})
	p, err := parse(pathRaw)
	if err != nil {
		return bad(err)
	}
	args.Path = p.Path
	return args, nil
}
func previewEdit(ctx context.Context, in agent.ToolExecutionContext, root *os.Root, args previewArguments, options PreviewEditOptions) (agent.ToolResult, error) {
	data, err := readTextFile(ctx, in, root, args.Path, options.MaxBytes)
	if err != nil {
		return agent.ToolResult{}, err
	}
	candidate, matchLine, err := exactEditCandidate(ctx, in, data, args, options.MaxBytes)
	if err != nil {
		return agent.ToolResult{}, err
	}
	originalHash, candidateHash := sha256.Sum256(data), sha256.Sum256([]byte(candidate))
	result := previewOutput{PreviewOnly: true, OriginalSHA256: hex.EncodeToString(originalHash[:]), CandidateSHA256: hex.EncodeToString(candidateHash[:]), Content: candidate, MatchLine: matchLine}
	if err := permission(ctx, in); err != nil {
		return agent.ToolResult{}, err
	}
	encoded, _ := json.Marshal(result)
	if len(encoded) > options.MaxResultBytes {
		return agent.ToolResult{}, failure(agent.ToolFailureResource, "fstools: encoded preview exceeds result limit", nil)
	}
	if err := permission(ctx, in); err != nil {
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: string(encoded)}}, StructuredContent: encoded}, nil
}

// exactEditCandidate is shared by preview and explicitly enabled edit. It does
// not write, normalize text, or establish a file-version precondition.
func exactEditCandidate(ctx context.Context, in agent.ToolExecutionContext, data []byte, args previewArguments, maxBytes int) (string, int, error) {
	if err := permission(ctx, in); err != nil {
		return "", 0, err
	}
	source := string(data)
	first := strings.Index(source, args.OldText)
	if first < 0 {
		return "", 0, failure(agent.ToolFailureResource, "fstools: old_text was not found", nil)
	}
	// Start one byte later, not len(old_text) later: overlapping occurrences also
	// make a proposed replacement ambiguous. All strings are valid bounded UTF-8.
	if strings.Index(source[first+1:], args.OldText) >= 0 {
		return "", 0, failure(agent.ToolFailureResource, "fstools: old_text is not unique", nil)
	}
	candidateBytes := len(source) - len(args.OldText) + len(*args.NewText)
	if candidateBytes > maxBytes {
		return "", 0, failure(agent.ToolFailureResource, "fstools: edit candidate exceeds byte limit", nil)
	}
	if err := permission(ctx, in); err != nil {
		return "", 0, err
	}
	candidate := source[:first] + *args.NewText + source[first+len(args.OldText):]
	return candidate, strings.Count(source[:first], "\n") + 1, nil
}
