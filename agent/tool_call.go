package agent

import "context"

// ToolCallOutcome is a tool's final result after argument validation and hooks.
type ToolCallOutcome struct {
	ToolCall ToolCall   `json:"tool_call"`
	Result   ToolResult `json:"result"`
	IsError  bool       `json:"is_error"`
}

// RunToolCallOptions selects tools and hooks for a programmatic tool call.
type RunToolCallOptions struct {
	Tools            []ToolDefinition
	AssistantMessage Message
	Context          AgentContext
	BeforeToolCall   BeforeToolCallHook
	AfterToolCall    AfterToolCallHook
	OnUpdate         ToolUpdateFunc
}

// RunToolCall uses the same preparation, validation and hooks as a model-issued
// call. It emits no agent events and appends no messages. Tool failures are
// returned as error results, retaining structured results from explicit IsError.
func RunToolCall(ctx context.Context, call ToolCall, options RunToolCallOptions) ToolCallOutcome {
	definition := AgentDefinition{Tools: cloneTools(options.Tools), BeforeToolCall: options.BeforeToolCall, AfterToolCall: options.AfterToolCall}
	if err := validateToolDefinitions(definition.Tools); err != nil {
		return ToolCallOutcome{ToolCall: cloneToolCall(call), Result: errorToolResult(err.Error()), IsError: true}
	}
	tools := make(map[string]ToolDefinition, len(definition.Tools))
	for _, tool := range definition.Tools {
		tools[tool.Name] = tool
	}
	engine := NewEngine()
	prepared, _, err := engine.prepareToolCall(ctx, definition, &AgentSnapshot{}, options.AssistantMessage, cloneAgentContext(options.Context), tools, call, nil)
	if err != nil {
		return ToolCallOutcome{ToolCall: cloneToolCall(call), Result: errorToolResult(err.Error()), IsError: true}
	}
	outcome, err := engine.executePreparedTool(ctx, definition, options.AssistantMessage, prepared, func(event AgentEvent) {
		if event.Type == EventToolExecutionUpdate && options.OnUpdate != nil && event.PartialToolResult != nil {
			options.OnUpdate(cloneToolResult(*event.PartialToolResult))
		}
	})
	if err != nil {
		return ToolCallOutcome{ToolCall: cloneToolCall(call), Result: errorToolResult(err.Error()), IsError: true}
	}
	return ToolCallOutcome{ToolCall: cloneToolCall(outcome.call), Result: cloneToolResult(outcome.result), IsError: outcome.isError}
}
