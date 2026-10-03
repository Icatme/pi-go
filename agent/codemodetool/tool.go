// Package codemodetool binds the standalone Codemode sandbox to an Agent's
// invocation-local tool lifecycle. It owns neither MCP sessions nor credentials.
package codemodetool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/codemode"
	"github.com/Icatme/pi-go/internal/jsontext"
	"github.com/google/jsonschema-go/jsonschema"
)

// Projection specifies the value a leaf returns to JavaScript. It is chosen by
// the application, never inferred from an untrusted name or description.
type Projection string

const (
	NativeValue Projection = "native"
	MCPEnvelope Projection = "mcp"
)

// Binding explicitly selects one callable leaf. Namespace identifies a native
// namespace or an MCP server; Tool contains the already authorized definition.
// Tools omitted from bindings are absent from discovery and execution.
type Binding struct {
	Tool       agent.ToolDefinition
	Namespace  string
	Projection Projection
}

func Native(tool agent.ToolDefinition, namespace string) Binding {
	return Binding{Tool: tool, Namespace: namespace, Projection: NativeValue}
}

func MCP(tool agent.ToolDefinition, server string) Binding {
	return Binding{Tool: tool, Namespace: server, Projection: MCPEnvelope}
}

// ExportedName is the deterministic JavaScript/Agent alias for this binding.
// It does not validate or grant the tool; New validates the actual allowlist.
func (binding Binding) ExportedName() string { return exportedName(binding) }

// NamespacePrefix identifies this MCP namespace before the tool-specific
// suffix. Exceptional namespace names include a stable identity hash, allowing
// saved aliases to reconnect only their original namespace without guessing.
func (binding Binding) NamespacePrefix() string {
	if binding.Projection != MCPEnvelope {
		return ""
	}
	return mcpNamespacePrefix(binding.Namespace)
}

// Options limits this container invocation. Zero values use bounded defaults.
// ChildLimits applies to the Agent's complete basic ledger and optional details.
type Options struct {
	// StateNamespace is a stable host binding name, required for journaled runs.
	// Change it when replacing the binding with an unrelated tool environment.
	StateNamespace  string
	storeKey        *byte
	Name            string
	Description     string
	Timeout         time.Duration
	MaxOutputTokens int
	ChildLimits     agent.ChildCallLimits
	// Namespace documentation is exposed only by describeNamespace. Each entry
	// must name a namespace represented by an allowed binding.
	Namespaces []codemode.Namespace
}

// Report retains bounded Go-side observations without exposing leaf schemas or
// responses to the model. Children is authoritative for tool execution facts;
// Sandbox.Calls describes the bridge and can include still-running host work.
type Report struct {
	Sandbox  codemode.Result       `json:"sandbox"`
	Children agent.ChildCallReport `json:"children"`
}

// New creates one sequential, model-visible code tool with an invocation-local
// leaf allowlist. It is usable through an Agent invocation, not by directly
// calling its Execute function without the bound ChildCaller capability.
func New(sandbox *codemode.Sandbox, bindings []Binding, options Options) (agent.ToolDefinition, error) {
	if sandbox == nil {
		return agent.ToolDefinition{}, errors.New("codemodetool: nil sandbox")
	}
	if options.Name == "" {
		options.Name = "code"
	}
	if !identifier(options.Name) || len(options.Name) > 64 {
		return agent.ToolDefinition{}, errors.New("codemodetool: code tool name must be a JavaScript identifier of at most 64 bytes")
	}
	if len(options.StateNamespace) > 128 || !utf8.ValidString(options.StateNamespace) {
		return agent.ToolDefinition{}, errors.New("codemodetool: invalid state namespace")
	}
	if options.Timeout < 0 || options.MaxOutputTokens < 0 {
		return agent.ToolDefinition{}, errors.New("codemodetool: invalid timeout or output limit")
	}
	if options.MaxOutputTokens == 0 {
		options.MaxOutputTokens = 2000
	}
	if len(bindings) > 256 {
		return agent.ToolDefinition{}, errors.New("codemodetool: leaf allowlist exceeds 256 tools")
	}
	definitions := make([]agent.ToolDefinition, 0, len(bindings))
	templates := make([]codemode.Tool, 0, len(bindings))
	projections := make(map[string]Projection, len(bindings))
	namespaces := make(map[string]Projection)
	sequential := false
	for _, binding := range bindings {
		if binding.Projection != NativeValue && binding.Projection != MCPEnvelope {
			return agent.ToolDefinition{}, errors.New("codemodetool: projection must be native or mcp")
		}
		if binding.Namespace == "" || len(binding.Namespace) > 128 || !utf8.ValidString(binding.Namespace) || binding.Tool.Name == "" || binding.Tool.Execute == nil {
			return agent.ToolDefinition{}, errors.New("codemodetool: namespace, leaf name and executor are required")
		}
		if binding.Tool.ChildTools != nil || binding.Tool.ResolveChildTools != nil {
			return agent.ToolDefinition{}, errors.New("codemodetool: recursive containers are unsupported")
		}
		if previous, exists := namespaces[binding.Namespace]; exists && previous != binding.Projection {
			return agent.ToolDefinition{}, fmt.Errorf("codemodetool: conflicting namespace %q", binding.Namespace)
		}
		namespaces[binding.Namespace] = binding.Projection
		name := exportedName(binding)
		if _, exists := projections[name]; exists || name == options.Name {
			return agent.ToolDefinition{}, fmt.Errorf("codemodetool: conflicting exported name %q", name)
		}
		projections[name] = binding.Projection
		tool, err := freezeLeaf(binding.Tool, name, binding.Projection)
		if err != nil {
			return agent.ToolDefinition{}, err
		}
		input, err := schemaJSON(tool.Parameters)
		if err != nil {
			return agent.ToolDefinition{}, fmt.Errorf("codemodetool: input schema %q: %w", name, err)
		}
		output, err := schemaJSON(tool.OutputSchema)
		if err != nil {
			return agent.ToolDefinition{}, fmt.Errorf("codemodetool: output schema %q: %w", name, err)
		}
		definitions = append(definitions, tool)
		resolution := "Resolves to joined text."
		if binding.Projection == MCPEnvelope {
			resolution = "Resolves to {content, structuredContent?, isError}; tool-reported business failures resolve with isError:true. Transport, permission and validation failures reject."
		} else if len(output) > 0 {
			resolution = "Resolves to the validated outputSchema value; failures reject."
		}
		templates = append(templates, codemode.Tool{Name: name, Namespace: binding.Namespace,
			Description: tool.Description, ResultDescription: resolution, Parameters: input, OutputSchema: output})
		sequential = sequential || tool.ExecutionMode == agent.ToolExecutionSequential
	}
	metadata := append([]codemode.Namespace(nil), options.Namespaces...)
	if len(metadata) > len(namespaces) {
		return agent.ToolDefinition{}, errors.New("codemodetool: too many namespace descriptions")
	}
	seen := make(map[string]bool)
	for _, value := range metadata {
		if _, ok := namespaces[value.Name]; !ok || seen[value.Name] || len(value.Description) > 4096 || len(value.Instructions) > 32<<10 || !utf8.ValidString(value.Description+value.Instructions) {
			return agent.ToolDefinition{}, errors.New("codemodetool: invalid or unavailable namespace description")
		}
		seen[value.Name] = true
	}
	if options.Description == "" {
		list := make([]string, 0, len(namespaces))
		for name := range namespaces {
			list = append(list, name)
		}
		sort.Strings(list)
		available := fmt.Sprintf("%d allowed namespaces", len(list))
		if len(list) > 0 && len(list) <= 12 && len(strings.Join(list, ", ")) <= 384 {
			available = strings.Join(list, ", ")
		}
		options.Description = "Run an async JavaScript body; await tools.<name>(args) or use Promise.all. " +
			"Discover with ALL_TOOLS, searchTools(query,{namespace?,limit?}), describeTool(name), describeNamespace(name). " +
			"Descriptions explain input and resolved values. Check availability with \"name\" in tools. " +
			"Globals: text/return and console.* emit bounded output; image emits inline images; store/load keep small host-scoped JSON values; exit succeeds. " +
			"Allowed namespaces: " + available + ". No filesystem, network or timers except through allowed tools."
	}
	// A non-zero-size allocation gives this binding a private comparable identity.
	// The invocation owns the resource; no session/run ID is retained globally.
	storeKey := options.storeKey
	if storeKey == nil {
		storeKey = new(byte)
	}
	return agent.ToolDefinition{
		Name: options.Name, Description: options.Description,
		Parameters:    map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string", "maxLength": 65536}}, "required": []string{"code"}, "additionalProperties": false},
		ExecutionMode: agent.ToolExecutionSequential, ChildTools: definitions, ChildLimits: options.ChildLimits,
		Execute: func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
			if execution.ChildCaller == nil || execution.Invocation == nil {
				return agent.ToolResult{}, &agent.ToolExecutionError{Code: agent.ToolFailurePolicyDenied,
					Message:   "codemodetool: a bound parent invocation is required",
					Execution: agent.ToolExecutionInfo{Local: agent.ToolLocalNotStarted, Remote: agent.ToolRemoteNotDispatched}}
			}
			args, ok := execution.Args.(map[string]any)
			if !ok {
				return agent.ToolResult{}, errors.New("codemodetool: expected code arguments")
			}
			code, ok := args["code"].(string)
			if !ok || len(code) > 64<<10 {
				return agent.ToolResult{}, errors.New("codemodetool: invalid or oversized code")
			}
			var store *codemode.Store
			var err error
			if execution.State != nil {
				if options.StateNamespace == "" {
					return agent.ToolResult{}, errors.New("codemodetool: journaled execution requires a host state namespace")
				}
				var raw json.RawMessage
				raw, err = execution.State.Load(options.StateNamespace)
				if err == nil {
					store, err = codemode.RestoreStore(1<<20, raw)
				}
			} else {
				var resource any
				resource, err = execution.Invocation.LoadOrCreate(storeKey, func() (any, error) { return codemode.NewStore(1 << 20) })
				if err == nil {
					var ok bool
					store, ok = resource.(*codemode.Store)
					if !ok {
						err = errors.New("invalid invocation store")
					}
				}
			}
			if err != nil {
				return agent.ToolResult{}, &agent.ToolExecutionError{Code: agent.ToolFailureResource, Message: "Codemode store is unavailable", Err: err}
			}
			tools := append([]codemode.Tool(nil), templates...)
			for i := range tools {
				projection := projections[tools[i].Name]
				tools[i].Invoke = func(callCtx context.Context, call codemode.HostCall) (json.RawMessage, error) {
					if err := codemode.ValidateJSON(call.Arguments); err != nil {
						return nil, &codemode.CallError{Code: string(agent.ToolFailureArgumentInvalid), ReasonCode: boundaryReason(err), Message: "arguments rejected at JavaScript boundary", Err: err,
							Execution: codemode.Execution{Local: string(agent.ToolLocalNotStarted), Remote: string(agent.ToolRemoteNotDispatched)}}
					}
					outcome := execution.ChildCaller.Call(callCtx, agent.ToolCall{Name: call.Name, Arguments: call.Arguments})
					facts := codemode.Execution{Local: string(outcome.Execution.Local), Remote: string(outcome.Execution.Remote)}
					if outcome.Err != nil {
						code := string(agent.ToolFailureProtocol)
						reason := ""
						if outcome.Failure != nil {
							code = string(outcome.Failure.Code)
							reason = outcome.Failure.Reason
						}
						return nil, &codemode.CallError{Code: code, ReasonCode: reason, Message: effectiveError(outcome.Result, code), CallID: outcome.ToolCall.ID, Execution: facts, Err: outcome.Err}
					}
					if projection == NativeValue && outcome.IsError {
						return nil, &codemode.CallError{Code: string(agent.ToolFailureToolReportedError), Message: effectiveError(outcome.Result, "tool reported an error"), CallID: outcome.ToolCall.ID, Execution: facts}
					}
					value, err := projectResult(outcome.Result, projection, len(tools[i].OutputSchema) > 0)
					if err != nil {
						return nil, &codemode.CallError{Code: string(agent.ToolFailureResultRejected), ReasonCode: boundaryReason(err), Message: "tool result rejected at JavaScript boundary", CallID: outcome.ToolCall.ID, Execution: facts, Err: err}
					}
					return value, nil
				}
			}
			result, err := sandbox.Run(ctx, code, codemode.RunOptions{Tools: tools, Namespaces: metadata, Timeout: options.Timeout, MaxOutputTokens: options.MaxOutputTokens, OutputReserveBytes: 1024, Sequential: sequential || execution.ChildSequential, Store: store})
			reportState := result
			// Output is retained in Content once. It cannot consume the failure
			// report's budget and evict the complete basic call ledger.
			reportState.Outputs = nil
			report := Report{Sandbox: reportState, Children: execution.ChildCaller.Report()}
			parts := make([]agent.Part, 0, len(result.Outputs)+1)
			for _, output := range result.Outputs {
				if output.Type == "image" {
					parts = append(parts, agent.Part{Type: agent.PartTypeImage, Data: output.Data, MIMEType: output.MimeType})
				} else {
					parts = append(parts, agent.Part{Type: agent.PartTypeText, Text: output.Text})
				}
			}
			presentation := fmt.Sprintf("Codemode completed; %d host calls.", len(result.Calls))
			if err != nil {
				presentation = fmt.Sprintf("Codemode failed; %d host calls. ", len(result.Calls))
				if len(result.Calls) > 0 {
					presentation += "Earlier calls may have effects; inspect execution facts before retrying. "
				}
				presentation += scriptDiagnostic(err)
			}
			if result.OutputReservedBytes > 0 && len(parts) < result.OutputLimitItems {
				parts = append(parts, agent.Part{Type: agent.PartTypeText, Text: boundedText(presentation, result.OutputReservedBytes)})
			}
			mapped := agent.ToolResult{Content: parts, Details: report, IsError: err != nil}
			if err != nil {
				code, reason := scriptFailure(err)
				return mapped, &agent.ToolExecutionError{Code: code, Reason: reason, Message: boundedText(presentation, result.OutputReservedBytes), Err: err,
					Execution: agent.ToolExecutionInfo{Remote: agent.ToolRemoteNotApplicable}}
			}
			if execution.State != nil {
				raw, stageErr := store.Export()
				if stageErr == nil {
					stageErr = execution.State.Stage(options.StateNamespace, raw)
				}
				if stageErr != nil {
					return mapped, &agent.ToolExecutionError{Code: agent.ToolFailureResource, Message: "Codemode state could not be staged", Err: stageErr, Execution: agent.ToolExecutionInfo{Remote: agent.ToolRemoteNotApplicable}}
				}
			}
			return mapped, nil
		},
	}, nil
}

func scriptDiagnostic(err error) string {
	var script *codemode.ScriptError
	if errors.As(err, &script) && script.Diagnostic != "" {
		return script.Diagnostic
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Deadline exceeded."
	}
	if errors.Is(err, context.Canceled) {
		return "Execution canceled."
	}
	if script != nil {
		return "Failure code: " + boundedText(script.Code, 128) + "; inspect the host report."
	}
	return "Script could not finish; inspect the host report."
}

func boundedText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= limit {
		return value
	}
	for !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}

func scriptFailure(err error) (agent.ToolFailureCode, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return agent.ToolFailureDeadline, "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return agent.ToolFailureCanceled, "canceled"
	}
	var script *codemode.ScriptError
	if errors.As(err, &script) {
		switch script.Code {
		case "sandbox", "closed":
			return agent.ToolFailureSandbox, script.Code
		case "options", "catalog":
			return agent.ToolFailureArgumentInvalid, script.Code
		case "store_conflict", "store_limit", "output_limit", "call_limit", "queue_limit":
			return agent.ToolFailureResource, script.Code
		default:
			return agent.ToolFailureScript, script.Code
		}
	}
	return agent.ToolFailureScript, "script"
}

func schemaJSON(value map[string]any) (json.RawMessage, error) {
	if len(value) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, errors.New("schema exceeds 64 KiB")
	}
	if err := codemode.ValidateJSON(data); err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, err
	}
	if _, err := schema.Resolve(nil); err != nil {
		return nil, err
	}
	return data, nil
}

func freezeLeaf(original agent.ToolDefinition, name string, projection Projection) (agent.ToolDefinition, error) {
	frozen := original
	frozen.Name = name
	clone := func(value map[string]any) (map[string]any, error) {
		if value == nil {
			return nil, nil
		}
		if err := jsontext.ValidateStrings(value); err != nil {
			return nil, err
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if err := codemode.ValidateJSON(data); err != nil {
			return nil, err
		}
		var result map[string]any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		err = decoder.Decode(&result)
		return result, err
	}
	var err error
	if frozen.Parameters, err = clone(original.Parameters); err != nil {
		return frozen, err
	}
	if frozen.OutputSchema, err = clone(original.OutputSchema); err != nil {
		return frozen, err
	}
	if original.ParseArguments != nil {
		frozen.ParseArguments = func(call agent.ToolCall) (any, error) {
			call.Name = original.Name
			args, err := original.ParseArguments(call)
			if err != nil {
				return nil, &agent.ToolExecutionError{Code: agent.ToolFailureArgumentInvalid, Message: "arguments rejected by the tool parser; check describeTool(name)", Err: err}
			}
			return args, nil
		}
	}
	frozen.ValidateResult = func(result agent.ToolResult) error {
		if original.ValidateResult != nil {
			if err := original.ValidateResult(result); err != nil {
				return &agent.ToolExecutionError{Code: agent.ToolFailureResultRejected, Message: "tool result rejected by its validator; inspect the host report", Err: err}
			}
		}
		structured := len(frozen.OutputSchema) > 0 && !(projection == NativeValue && result.IsError)
		if _, err := projectResult(result, projection, structured); err != nil {
			return &agent.ToolExecutionError{Code: agent.ToolFailureResultRejected, Reason: boundaryReason(err), Message: "tool result rejected at JavaScript boundary", Err: err}
		}
		return nil
	}
	frozen.Execute = func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
		err := jsontext.ValidateStrings(execution.Args)
		var encoded []byte
		if err == nil {
			encoded, err = json.Marshal(execution.Args)
		}
		if err == nil {
			err = codemode.ValidateJSON(encoded)
		}
		if err != nil {
			return agent.ToolResult{}, &agent.ToolExecutionError{Code: agent.ToolFailureArgumentInvalid, Reason: boundaryReason(err), Message: "arguments rejected at JavaScript boundary", Err: err,
				Execution: agent.ToolExecutionInfo{Remote: agent.ToolRemoteNotDispatched}}
		}
		execution.ToolCall.Name = original.Name
		result, err := original.Execute(ctx, execution)
		if err == nil {
			return result, nil
		}
		// Raw causes may contain credentials or payloads. Only an explicit safe
		// ToolExecutionError.Message may become the default lifecycle presentation;
		// after hooks can still replace it with their own processed public output.
		var typed *agent.ToolExecutionError
		if errors.As(err, &typed) && typed.Message != "" {
			return result, err
		}
		failure := &agent.ToolExecutionError{Message: "tool call failed; inspect the host report", Err: err}
		if typed != nil {
			failure.Code, failure.Reason, failure.Execution = typed.Code, typed.Reason, typed.Execution
		}
		return result, failure
	}
	return frozen, nil
}

func boundaryReason(err error) string {
	if errors.Is(err, jsontext.ErrInvalidUnicode) {
		return "invalid_unicode"
	}
	var precision *codemode.JSONError
	if errors.As(err, &precision) {
		return precision.Code
	}
	return "projection_rejected"
}

func projectResult(result agent.ToolResult, projection Projection, structured bool) (json.RawMessage, error) {
	if len(result.StructuredContent) > 0 && (projection == MCPEnvelope || structured) {
		if err := codemode.ValidateJSON(result.StructuredContent); err != nil {
			return nil, err
		}
	}
	if projection == NativeValue {
		if structured {
			if len(result.StructuredContent) == 0 {
				return nil, errors.New("structured result required by output schema")
			}
			return append(json.RawMessage(nil), result.StructuredContent...), nil
		}
		text := resultText(result)
		if err := jsontext.ValidateStrings(text); err != nil {
			return nil, err
		}
		return json.Marshal(text)
	}
	content := make([]map[string]any, 0, len(result.Content))
	for _, part := range result.Content {
		switch part.Type {
		case agent.PartTypeText:
			content = append(content, map[string]any{"type": "text", "text": part.Text})
		case agent.PartTypeImage:
			content = append(content, map[string]any{"type": "image", "data": part.Data, "mimeType": part.MIMEType})
		default:
			return nil, fmt.Errorf("unsupported MCP content %q", part.Type)
		}
	}
	envelope := map[string]any{"content": content, "isError": result.IsError}
	if len(result.StructuredContent) > 0 {
		envelope["structuredContent"] = result.StructuredContent
	}
	if err := jsontext.ValidateStrings(envelope); err != nil {
		return nil, err
	}
	data, err := json.Marshal(envelope)
	if err == nil {
		err = codemode.ValidateJSON(data)
	}
	return data, err
}

func resultText(result agent.ToolResult) string {
	texts := make([]string, 0, len(result.Content))
	for _, part := range result.Content {
		if part.Type == agent.PartTypeText {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func effectiveError(result agent.ToolResult, fallback string) string {
	text := resultText(result)
	if text == "" {
		return fallback
	}
	runes := []rune(text)
	if len(runes) > 2048 {
		return string(runes[:2048]) + "…"
	}
	return text
}

func identifier(value string) bool {
	if value == "" {
		return false
	}
	for i, c := range value {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func exportedName(binding Binding) string {
	value := binding.Tool.Name
	if binding.Projection == MCPEnvelope {
		prefix := mcpNamespacePrefix(binding.Namespace)
		value = prefix + value
		if identifier(value) && len(value) <= 64 {
			return value
		}
		tool := cleanIdentifier(binding.Tool.Name)
		if len(tool) > 64-len(prefix)-14 {
			tool = tool[:64-len(prefix)-14]
		}
		identity := sha256.Sum256([]byte(string(binding.Projection) + "\x00" + binding.Namespace + "\x00" + binding.Tool.Name))
		return prefix + tool + "__" + hex.EncodeToString(identity[:6])
	}
	if identifier(value) && len(value) <= 64 {
		return value
	}
	prefix := cleanIdentifier(value)
	if prefix == "" || prefix[0] >= '0' && prefix[0] <= '9' {
		prefix = "tool_" + prefix
	}
	if len(prefix) > 50 {
		prefix = prefix[:50]
	}
	identity := sha256.Sum256([]byte(string(binding.Projection) + "\x00" + binding.Namespace + "\x00" + binding.Tool.Name))
	return prefix + "__" + hex.EncodeToString(identity[:6])
}

func cleanIdentifier(value string) string {
	var clean strings.Builder
	for _, c := range value {
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			clean.WriteRune(c)
		} else {
			clean.WriteByte('_')
		}
	}
	return clean.String()
}

func mcpNamespacePrefix(namespace string) string {
	clean := cleanIdentifier(namespace)
	if clean == namespace && len(namespace) <= 24 && !strings.Contains(namespace, "__") {
		return "mcp__" + namespace + "__"
	}
	if clean == "" {
		clean = "server"
	}
	if len(clean) > 12 {
		clean = clean[:12]
	}
	identity := sha256.Sum256([]byte(namespace))
	return "mcp__" + clean + "_" + hex.EncodeToString(identity[:6]) + "__"
}
