package fstools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"regexp/syntax"
	"strings"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/internal/jsontext"
)

// GrepOptions bounds one explicitly selected file, not a recursive search.
// Zero values select defaults; limits are host-controlled.
type GrepOptions struct {
	MaxScanBytes    int // Default 64 KiB; ceiling 1 MiB. Whole file must fit.
	MaxLineBytes    int // Default 16 KiB; ceiling 256 KiB. LF excluded, CR retained.
	MaxScannedLines int // Default 10000; ceiling 100000 regex-tested lines.
	MaxMatches      int // Default 100; ceiling 4096 matching lines.
	MaxResultBytes  int // Default 64 KiB; range 256 bytes to 1 MiB.
}

// GrepDefinition explicitly opts into single-file text search. It grants content
// access and is not a secret-redaction facility. Other factories are unchanged.
// The caller owns root; Linux/procfs/workspace trust requirements match read.
func GrepDefinition(root *os.Root, options GrepOptions) (agent.ToolDefinition, error) {
	if !supported {
		return agent.ToolDefinition{}, ErrUnsupportedPlatform
	}
	if root == nil {
		return agent.ToolDefinition{}, errors.New("fstools: root is required")
	}
	if options.MaxScanBytes == 0 {
		options.MaxScanBytes = 64 << 10
	}
	if options.MaxLineBytes == 0 {
		options.MaxLineBytes = 16 << 10
	}
	if options.MaxScannedLines == 0 {
		options.MaxScannedLines = 10000
	}
	if options.MaxMatches == 0 {
		options.MaxMatches = 100
	}
	if options.MaxResultBytes == 0 {
		options.MaxResultBytes = 64 << 10
	}
	if options.MaxScanBytes < 1 || options.MaxScanBytes > 1<<20 || options.MaxLineBytes < 1 || options.MaxLineBytes > 256<<10 || options.MaxScannedLines < 1 || options.MaxScannedLines > 100000 || options.MaxMatches < 1 || options.MaxMatches > 4096 || options.MaxResultBytes < 256 || options.MaxResultBytes > 1<<20 {
		return agent.ToolDefinition{}, errors.New("fstools: unsupported grep limit")
	}
	return agent.ToolDefinition{
		Name: "grep", Description: "Search one bounded UTF-8 file, not a directory, with a case-sensitive Go regexp. Returns one full line per matching line, with 1-based line numbers and explicit scan/result limits. Matching lines may contain sensitive data; no automatic redaction. Oversize/nontext files or overlong lines fail without partial content.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "maxLength": 4096}, "pattern": map[string]any{"type": "string", "maxLength": maxGrepPatternBytes}}, "required": []string{"path", "pattern"}, "additionalProperties": false},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"matches":    map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"line": map[string]any{"type": "integer"}, "text": map[string]any{"type": "string"}}, "required": []string{"line", "text"}, "additionalProperties": false}},
			"bytes_read": map[string]any{"type": "integer"}, "lines_scanned": map[string]any{"type": "integer"}, "truncated": map[string]any{"type": "boolean"}, "limits": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": grepLimits()}},
		}, "required": []string{"matches", "bytes_read", "lines_scanned", "truncated", "limits"}, "additionalProperties": false},
		MaxArgumentBytes: 32 << 10,
		ParseArguments: parseCallArguments(func(raw []byte) (grepArguments, error) {
			args, _, err := parseGrep(raw)
			return args, err
		}),
		Execute: func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
			if err := jsontext.ValidateStrings(execution.Args); err != nil {
				return agent.ToolResult{}, invalid(err)
			}
			raw, err := json.Marshal(execution.Args)
			if err != nil {
				return agent.ToolResult{}, invalid(err)
			}
			args, ast, err := parseGrep(raw)
			if err != nil {
				return agent.ToolResult{}, err
			}
			if err := permission(ctx, execution); err != nil {
				return agent.ToolResult{}, err
			}
			pattern, err := compileGrepPattern(args.Pattern, ast)
			if err != nil {
				return agent.ToolResult{}, failure(agent.ToolFailureArgumentInvalid, "fstools: unsupported bounded Go regexp", err)
			}
			return grep(ctx, execution, root, args, pattern, options)
		},
	}, nil
}

func grepLimits() []string { return []string{"max_scanned_lines", "max_matches", "max_result_bytes"} }

type grepArguments struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
}
type grepMatch struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}
type grepOutput struct {
	Matches      []grepMatch `json:"matches"`
	BytesRead    int         `json:"bytes_read"`
	LinesScanned int         `json:"lines_scanned"`
	Truncated    bool        `json:"truncated"`
	Limits       []string    `json:"limits"`
}

func parseGrep(raw []byte) (grepArguments, *syntax.Regexp, error) {
	var args grepArguments
	bad := func(err error) (grepArguments, *syntax.Regexp, error) {
		return args, nil, failure(agent.ToolFailureArgumentInvalid, "fstools: expected a relative file path and supported bounded Go regexp", err)
	}
	if len(raw) > 32<<10 {
		return bad(errors.New("argument limit"))
	}
	if err := decodeStringArguments(raw, map[string]*string{"path": &args.Path, "pattern": &args.Pattern}); err != nil {
		return bad(err)
	}
	encoded, _ := json.Marshal(arguments{Path: args.Path})
	normalized, err := parse(encoded)
	if err != nil {
		return bad(err)
	}
	args.Path = normalized.Path
	if strings.ContainsRune(args.Pattern, 0) {
		return bad(errors.New("NUL regexp is not supported"))
	}
	pattern, err := validateGrepPattern(args.Pattern)
	if err != nil {
		return bad(err)
	}
	return args, pattern, nil
}

func grep(ctx context.Context, execution agent.ToolExecutionContext, root *os.Root, args grepArguments, pattern *regexp.Regexp, options GrepOptions) (agent.ToolResult, error) {
	data, err := readTextFile(ctx, execution, root, args.Path, options.MaxScanBytes)
	if err != nil {
		return agent.ToolResult{}, err
	}
	// Validate every line before matching, even if an early match later exhausts
	// output limits. No Scanner token ceiling or silently skipped long line.
	length := 0
	for i, b := range data {
		if i%8192 == 0 {
			if err := permission(ctx, execution); err != nil {
				return agent.ToolResult{}, err
			}
		}
		if b == '\n' {
			length = 0
			continue
		}
		length++
		if length > options.MaxLineBytes {
			return agent.ToolResult{}, failure(agent.ToolFailureResource, "fstools: file contains a line exceeding the grep limit", nil)
		}
	}
	result := grepOutput{Matches: []grepMatch{}, BytesRead: len(data), Limits: []string{}}
	reserve, _ := json.Marshal(grepOutput{Matches: []grepMatch{}, BytesRead: options.MaxScanBytes, LinesScanned: options.MaxScannedLines, Limits: grepLimits()})
	resultBytes := len(reserve)
	limit := func(reason string) { result.Truncated = true; result.Limits = append(result.Limits, reason) }
	for offset := 0; offset < len(data); {
		if err := permission(ctx, execution); err != nil {
			return agent.ToolResult{}, err
		}
		if result.LinesScanned == options.MaxScannedLines {
			limit("max_scanned_lines")
			break
		}
		end := bytes.IndexByte(data[offset:], '\n')
		next := len(data)
		if end < 0 {
			end = len(data)
		} else {
			end += offset
			next = end + 1
		}
		line := data[offset:end]
		offset = next
		result.LinesScanned++
		if !pattern.Match(line) {
			continue
		}
		match := grepMatch{Line: result.LinesScanned, Text: string(line)}
		encoded, _ := json.Marshal(match)
		added := len(encoded)
		if len(result.Matches) > 0 {
			added++
		}
		if resultBytes+added > options.MaxResultBytes {
			limit("max_result_bytes")
			break
		}
		resultBytes += added
		result.Matches = append(result.Matches, match)
		if len(result.Matches) == options.MaxMatches {
			limit("max_matches")
			break
		}
	}
	encoded, _ := json.Marshal(result)
	if err := permission(ctx, execution); err != nil {
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: string(encoded)}}, StructuredContent: encoded}, nil
}
