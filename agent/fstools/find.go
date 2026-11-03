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

// FindOptions bounds one filename search. Zero values select defaults.
// Limits belong to the host and cannot be widened by model arguments.
type FindOptions struct {
	MaxVisitedEntries int // Default 10000; ceiling 100000, including nonmatches.
	MaxDepth          int // Default 8; ceiling 64. The starting directory has depth 1.
	MaxMatches        int // Default 256; ceiling 4096.
	MaxResultBytes    int // Default 64 KiB; range 256 bytes to 1 MiB.
}

// FindDefinition explicitly opts into bounded filename discovery. It reads no
// file contents and does not change Definitions or ReadDefinition. The caller
// owns root; construction performs no filesystem I/O. Only Linux is supported.
func FindDefinition(root *os.Root, options FindOptions) (agent.ToolDefinition, error) {
	if !supported {
		return agent.ToolDefinition{}, ErrUnsupportedPlatform
	}
	if root == nil {
		return agent.ToolDefinition{}, errors.New("fstools: root is required")
	}
	if options.MaxVisitedEntries == 0 {
		options.MaxVisitedEntries = 10000
	}
	if options.MaxDepth == 0 {
		options.MaxDepth = 8
	}
	if options.MaxMatches == 0 {
		options.MaxMatches = 256
	}
	if options.MaxResultBytes == 0 {
		options.MaxResultBytes = 64 << 10
	}
	if options.MaxVisitedEntries < 1 || options.MaxVisitedEntries > 100000 || options.MaxDepth < 1 || options.MaxDepth > 64 || options.MaxMatches < 1 || options.MaxMatches > 4096 || options.MaxResultBytes < 256 || options.MaxResultBytes > 1<<20 {
		return agent.ToolDefinition{}, errors.New("fstools: unsupported find limit")
	}
	return agent.ToolDefinition{
		Name: "find", Description: "Find basenames matching a case-sensitive glob under a relative directory. Uses bounded depth-first filename discovery, without reading file contents or descending through child symlinks. Results report limits reached; paths are not a filesystem snapshot.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "maxLength": 4096}, "pattern": map[string]any{"type": "string", "maxLength": 256}}, "required": []string{"path", "pattern"}, "additionalProperties": false},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"entries": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "type": map[string]any{"type": "string", "enum": []string{"directory", "symlink", "file", "other"}}}, "required": []string{"path", "type"}, "additionalProperties": false}},
			"visited": map[string]any{"type": "integer"}, "truncated": map[string]any{"type": "boolean"}, "limits": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": findLimits()}},
		}, "required": []string{"entries", "visited", "truncated", "limits"}, "additionalProperties": false},
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
			return parseFind(raw)
		},
		Execute: func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
			if err := jsontext.ValidateStrings(execution.Args); err != nil {
				return agent.ToolResult{}, invalid(err)
			}
			raw, err := json.Marshal(execution.Args)
			if err != nil {
				return agent.ToolResult{}, invalid(err)
			}
			args, err := parseFind(raw)
			if err != nil {
				return agent.ToolResult{}, err
			}
			return find(ctx, execution, root, args, options)
		},
	}, nil
}

func findLimits() []string {
	return []string{"max_depth", "max_visited_entries", "max_matches", "max_result_bytes"}
}

type findArguments struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
}
type findEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
}
type findOutput struct {
	Entries   []findEntry `json:"entries"`
	Visited   int         `json:"visited"`
	Truncated bool        `json:"truncated"`
	Limits    []string    `json:"limits"`
}

func parseFind(raw []byte) (findArguments, error) {
	var args findArguments
	bad := func(err error) (findArguments, error) {
		return args, failure(agent.ToolFailureArgumentInvalid, "fstools: expected a relative path and valid basename glob", err)
	}
	if len(raw) > 32<<10 {
		return bad(errors.New("argument limit"))
	}
	if err := jsontext.ValidateUnicode(raw); err != nil {
		return bad(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return bad(err)
	}
	encoded, _ := json.Marshal(arguments{Path: args.Path})
	normalized, err := parse(encoded)
	if err != nil {
		return bad(err)
	}
	args.Path = normalized.Path
	if args.Pattern == "" || len(args.Pattern) > 256 || strings.ContainsAny(args.Pattern, "/\x00") {
		return bad(errors.New("invalid basename glob"))
	}
	if _, err := path.Match(args.Pattern, ""); err != nil {
		return bad(err)
	}
	return args, nil
}

func find(ctx context.Context, execution agent.ToolExecutionContext, root *os.Root, args findArguments, options FindOptions) (agent.ToolResult, error) {
	if err := permission(ctx, execution); err != nil {
		return agent.ToolResult{}, err
	}
	directory, err := openDirectory(root, args.Path)
	if err != nil {
		return agent.ToolResult{}, failure(agent.ToolFailureResource, "fstools: cannot open search directory", err)
	}
	result := findOutput{Entries: []findEntry{}, Limits: []string{}}
	// Reserve the complete possible envelope, including every limit reason and
	// the maximum visited count. This avoids unbounded repeated serialization.
	reserve, _ := json.Marshal(findOutput{Entries: []findEntry{}, Visited: options.MaxVisitedEntries, Limits: findLimits()})
	resultBytes := len(reserve)
	limit := func(reason string) {
		result.Truncated = true
		for _, s := range result.Limits {
			if s == reason {
				return
			}
		}
		result.Limits = append(result.Limits, reason)
	}
	var walk func(*os.File, string, int) (bool, error)
	walk = func(dir *os.File, prefix string, depth int) (bool, error) {
		defer dir.Close()
		for {
			if err := permission(ctx, execution); err != nil {
				return false, err
			}
			if result.Visited == options.MaxVisitedEntries {
				limit("max_visited_entries")
				return true, nil
			}
			entries, err := readOneEntry(dir)
			if err != nil && !errors.Is(err, io.EOF) {
				return false, failure(agent.ToolFailureResource, "fstools: cannot read search directory", err)
			}
			if len(entries) == 0 {
				return false, nil
			}
			result.Visited++
			item := entries[0]
			if !utf8.ValidString(item.Name()) {
				return false, failure(agent.ToolFailureResource, "fstools: directory contains a non-UTF-8 name", nil)
			}
			relative := path.Join(prefix, item.Name())
			matches, _ := path.Match(args.Pattern, item.Name())
			if matches {
				kind := "other"
				switch {
				case item.IsDir():
					kind = "directory"
				case item.Type()&os.ModeSymlink != 0:
					kind = "symlink"
				case item.Type().IsRegular():
					kind = "file"
				}
				next := findEntry{Path: relative, Type: kind}
				encoded, _ := json.Marshal(next)
				added := len(encoded)
				if len(result.Entries) > 0 {
					added++
				}
				if resultBytes+added > options.MaxResultBytes {
					limit("max_result_bytes")
					return true, nil
				}
				resultBytes += added
				result.Entries = append(result.Entries, next)
				if len(result.Entries) == options.MaxMatches {
					limit("max_matches")
					return true, nil
				}
			}
			if result.Visited == options.MaxVisitedEntries {
				limit("max_visited_entries")
				return true, nil
			}
			if !item.IsDir() {
				continue
			}
			if depth == options.MaxDepth {
				limit("max_depth")
				continue
			}
			if err := permission(ctx, execution); err != nil {
				return false, err
			}
			child, err := openChildDirectory(dir, item.Name())
			if err != nil {
				return false, failure(agent.ToolFailureResource, "fstools: cannot open child search directory", err)
			}
			stop, err := walk(child, relative, depth+1)
			if stop || err != nil {
				return stop, err
			}
		}
	}
	_, err = walk(directory, args.Path, 1)
	if err != nil {
		return agent.ToolResult{}, err
	}
	if err := permission(ctx, execution); err != nil {
		return agent.ToolResult{}, err
	}
	encoded, _ := json.Marshal(result)
	return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: string(encoded)}}, StructuredContent: encoded}, nil
}
