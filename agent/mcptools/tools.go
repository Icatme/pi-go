// Package mcptools maps tools from a caller-owned MCP session to Agent tools.
// It provides no authorization, transport setup, credentials or session ownership.
package mcptools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"reflect"

	"github.com/Icatme/pi-go/agent"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ResultError means a response was received after remote tool execution, but
// could not be mapped safely. It is not evidence that the operation failed or
// may be retried. Result retains supported content when available within bounds.
type ResultError struct {
	Result agent.ToolResult
	Err    error
}

func (e *ResultError) Error() string {
	return "mcptools: result rejected after remote tool execution; do not retry automatically: " + e.Err.Error()
}
func (e *ResultError) Unwrap() error { return e.Err }

// Options selects names explicitly. Nil or empty Names selects nothing and
// performs no I/O. Limits apply after SDK decoding; transport byte limits and
// deadlines are the caller's responsibility. Zero limits use bounded defaults.
type Options struct {
	Names            []string
	MaxPages         int
	MaxTools         int
	MaxSchemaBytes   int
	MaxResultBytes   int
	MaxArgumentBytes int
}

func (o Options) defaults() (Options, error) {
	limits := []*int{&o.MaxPages, &o.MaxTools, &o.MaxSchemaBytes, &o.MaxResultBytes, &o.MaxArgumentBytes}
	defaults := []int{16, 256, 64 << 10, 1 << 20, 64 << 10}
	for i, value := range limits {
		if *value < 0 {
			return o, fmt.Errorf("mcptools: negative limit")
		}
		if *value == 0 {
			*value = defaults[i]
		}
	}
	return o, nil
}

// Discover returns tools in Names order. Missing/duplicate names, invalid schemas
// and exceeded limits fail the whole discovery. Descriptions are untrusted data.
// Each Execute re-lists through the SDK and rejects changed schemas before the
// call. This observes the SDK's cache/invalidation contract, not atomic server
// versioning: callers must handle tool-change notifications and trust their peer.
// Execute invokes CallTool once and adds no retry. The caller must disable the
// SDK client's default MultiRoundTrip middleware to prevent SDK-level retries.
// No at-most-once guarantee is possible for an arbitrary supplied session.
func Discover(ctx context.Context, session *mcp.ClientSession, options Options) ([]agent.ToolDefinition, error) {
	o, err := options.defaults()
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(o.Names))
	for _, name := range o.Names {
		if name == "" || wanted[name] {
			return nil, fmt.Errorf("mcptools: empty or duplicate selected name")
		}
		wanted[name] = true
	}
	if len(wanted) == 0 {
		return []agent.ToolDefinition{}, nil
	}
	if session == nil {
		return nil, fmt.Errorf("mcptools: nil session")
	}
	if len(wanted) > o.MaxTools {
		return nil, fmt.Errorf("mcptools: selected tool limit exceeded")
	}
	listed, err := list(ctx, session, o)
	if err != nil {
		return nil, err
	}
	definitions := make([]agent.ToolDefinition, 0, len(o.Names))
	for _, name := range o.Names {
		tool, ok := listed[name]
		if !ok {
			return nil, fmt.Errorf("mcptools: selected tool %q is missing", name)
		}
		input, inputValidator, err := schema(tool.InputSchema, o.MaxSchemaBytes, true)
		if err != nil {
			return nil, fmt.Errorf("mcptools: input schema for %q: %w", name, err)
		}
		output, outputValidator, err := schema(tool.OutputSchema, o.MaxSchemaBytes, false)
		if err != nil {
			return nil, fmt.Errorf("mcptools: output schema for %q: %w", name, err)
		}
		// Freeze closure state independently from the caller-visible maps.
		inputSnapshot, _ := json.Marshal(input)
		outputSnapshot, _ := json.Marshal(output)
		definitions = append(definitions, agent.ToolDefinition{
			Name: name, Description: tool.Description, Parameters: input, OutputSchema: output,
			Execute: func(callCtx context.Context, _ string, args any, _ agent.ToolUpdateFunc) (agent.ToolResult, error) {
				if err := callCtx.Err(); err != nil {
					return agent.ToolResult{}, err
				}
				encoded, err := json.Marshal(args)
				if err != nil || len(encoded) > o.MaxArgumentBytes {
					return agent.ToolResult{}, fmt.Errorf("mcptools: invalid or oversized arguments")
				}
				var arguments map[string]any
				decoder := json.NewDecoder(bytes.NewReader(encoded))
				decoder.UseNumber()
				if err := decoder.Decode(&arguments); err != nil {
					return agent.ToolResult{}, fmt.Errorf("mcptools: arguments must be an object")
				}
				// Agent cloning normalizes an empty argument map to nil; the SDK
				// likewise sends an empty object for an argument-less call.
				if arguments == nil {
					arguments = map[string]any{}
				}
				if err := inputValidator.Validate(arguments); err != nil {
					return agent.ToolResult{}, fmt.Errorf("mcptools: arguments do not match schema: %w", err)
				}
				current, err := list(callCtx, session, o)
				if err != nil {
					return agent.ToolResult{}, err
				}
				fresh, ok := current[name]
				if !ok {
					return agent.ToolResult{}, fmt.Errorf("mcptools: selected tool disappeared")
				}
				in, errIn := json.Marshal(fresh.InputSchema)
				out, errOut := json.Marshal(fresh.OutputSchema)
				if errIn != nil || errOut != nil || string(in) != string(inputSnapshot) || string(out) != string(outputSnapshot) {
					return agent.ToolResult{}, fmt.Errorf("mcptools: tool schema changed; rediscover and reapprove")
				}
				result, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: name, Arguments: arguments})
				if err != nil {
					return agent.ToolResult{}, fmt.Errorf("mcptools: call transport/protocol failure; execution may have occurred, do not retry automatically: %w", err)
				}
				mapped, err := convertResult(result, outputValidator, o.MaxResultBytes)
				if err != nil {
					return mapped, &ResultError{Result: mapped, Err: err}
				}
				return mapped, nil
			},
		})
	}
	return definitions, nil
}

func list(ctx context.Context, session *mcp.ClientSession, o Options) (map[string]*mcp.Tool, error) {
	result := make(map[string]*mcp.Tool)
	seen := make(map[string]bool)
	cursor := ""
	for page := 0; page < o.MaxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		response, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcptools: list tools: %w", err)
		}
		if response == nil {
			return nil, fmt.Errorf("mcptools: nil tool list")
		}
		for _, tool := range response.Tools {
			if tool == nil || tool.Name == "" || result[tool.Name] != nil {
				return nil, fmt.Errorf("mcptools: invalid or duplicate discovered tool")
			}
			if len(result) >= o.MaxTools {
				return nil, fmt.Errorf("mcptools: discovered tool limit exceeded")
			}
			result[tool.Name] = tool
		}
		cursor = response.NextCursor
		if cursor == "" {
			return result, nil
		}
		if seen[cursor] {
			return nil, fmt.Errorf("mcptools: repeated pagination cursor")
		}
		seen[cursor] = true
	}
	return nil, fmt.Errorf("mcptools: page limit exceeded")
}

func schema(value any, limit int, required bool) (map[string]any, *jsonschema.Resolved, error) {
	if value == nil && !required {
		return nil, nil, nil
	}
	if err := rejectUnsafeNumbers(value); err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > limit {
		return nil, nil, fmt.Errorf("invalid or oversized schema")
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, nil, fmt.Errorf("schema must be an object")
	}
	if required && object["type"] != "object" {
		return nil, nil, fmt.Errorf("input schema must declare object type")
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, nil, err
	}
	// No Loader: external references fail; discovery never fetches schema URLs.
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return nil, nil, err
	}
	return object, resolved, nil
}

func convertResult(result *mcp.CallToolResult, output *jsonschema.Resolved, limit int) (agent.ToolResult, error) {
	if result == nil {
		return agent.ToolResult{}, fmt.Errorf("mcptools: nil call result")
	}
	if result.NeedsInput() {
		return agent.ToolResult{}, fmt.Errorf("mcptools: interactive input is unsupported")
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > limit {
		return agent.ToolResult{}, fmt.Errorf("mcptools: invalid or oversized result")
	}
	converted := agent.ToolResult{IsError: result.IsError}
	if result.StructuredContent != nil {
		converted.StructuredContent, err = json.Marshal(result.StructuredContent)
		if err != nil {
			return agent.ToolResult{}, err
		}
	}
	var contentErr error
	for _, content := range result.Content {
		if content == nil || (reflect.ValueOf(content).Kind() == reflect.Pointer && reflect.ValueOf(content).IsNil()) {
			return agent.ToolResult{}, fmt.Errorf("mcptools: nil content")
		}
		switch c := content.(type) {
		case *mcp.TextContent:
			converted.Content = append(converted.Content, agent.Part{Type: agent.PartTypeText, Text: c.Text})
		case *mcp.ImageContent:
			converted.Content = append(converted.Content, agent.Part{Type: agent.PartTypeImage, Data: base64.StdEncoding.EncodeToString(c.Data), MIMEType: c.MIMEType})
		default:
			contentErr = fmt.Errorf("mcptools: unsupported content type %T", content)
		}
	}
	if err := rejectUnsafeNumbers(result.StructuredContent); err != nil {
		// The SDK has already decoded these values; never expose rounded data
		// as valid structured output. Exact text blocks remain available.
		converted.StructuredContent = nil
		return converted, err
	}
	if contentErr != nil {
		return converted, contentErr
	}
	// Error results need not satisfy the successful output schema.
	if output != nil && !result.IsError {
		if result.StructuredContent == nil {
			return converted, fmt.Errorf("mcptools: missing structured output")
		}
		if err := output.Validate(result.StructuredContent); err != nil {
			return converted, fmt.Errorf("mcptools: output does not match schema: %w", err)
		}
	}
	return converted, nil
}

// SDK interface-valued numbers have already passed through float64. At and
// above 2^53, an adjacent integer may have rounded to this value. Reject even
// genuinely representable values conservatively; their source is unknowable.
func rejectUnsafeNumbers(value any) error {
	switch v := value.(type) {
	case float64:
		if math.Abs(v) >= 1<<53 {
			return fmt.Errorf("mcptools: unsafe SDK-decoded numeric precision")
		}
	case map[string]any:
		for _, child := range v {
			if err := rejectUnsafeNumbers(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := rejectUnsafeNumbers(child); err != nil {
				return err
			}
		}
	}
	return nil
}
