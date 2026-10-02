package agent

import "context"

// ToolCallOutcome is a tool's final result after argument validation and hooks.
type ToolCallOutcome struct {
	ToolCall  ToolCall          `json:"tool_call"`
	Result    ToolResult        `json:"result"`
	IsError   bool              `json:"is_error"`
	Err       error             `json:"-"`
	Failure   *ToolFailure      `json:"failure,omitempty"`
	Execution ToolExecutionInfo `json:"execution"`
}

// RunToolCallOptions selects tools and hooks for a programmatic tool call.
type RunToolCallOptions struct {
	Tools               []ToolDefinition
	AssistantMessage    Message
	Context             AgentContext
	BeforeToolCall      BeforeToolCallHook
	AfterToolCall       AfterToolCallHook
	OnUpdate            ToolUpdateFunc
	ToolGate            ToolGateHook
	CheckToolPermission ToolPermissionHook
	PolicyRevision      string
	ToolExecution       ToolExecutionMode
}

// RunToolCall uses the same preparation, validation and hooks as a model-issued
// call. It emits no agent events and appends no messages. Tool failures are
// returned as error results, retaining structured results from explicit IsError.
func RunToolCall(ctx context.Context, call ToolCall, options RunToolCallOptions) ToolCallOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = newToolInvocationContext(ctx)
	definition := AgentDefinition{Tools: cloneTools(options.Tools), BeforeToolCall: options.BeforeToolCall,
		AfterToolCall: options.AfterToolCall, CheckToolPermission: options.CheckToolPermission, PolicyRevision: options.PolicyRevision,
		ToolExecution: options.ToolExecution}
	if err := validateToolDefinitions(definition.Tools); err != nil {
		return publicToolOutcome(rejectedToolOutcome(call, err, ToolFailureArgumentInvalid))
	}
	tools := make(map[string]ToolDefinition, len(definition.Tools))
	for _, tool := range definition.Tools {
		tools[tool.Name] = tool
	}
	engine := NewEngine()
	prepared, suspension, err := engine.prepareToolInvocation(ctx, definition, options.AssistantMessage,
		cloneAgentContext(options.Context), tools, call, options.ToolGate, nil)
	if err != nil {
		return publicToolOutcome(rejectedToolOutcome(call, err, ToolFailureHook))
	}
	if suspension != nil {
		return publicToolOutcome(rejectedToolOutcome(call, &ToolCallsSuspendedError{Calls: []SuspendedToolCall{*suspension}}, ToolFailurePolicyDenied))
	}
	outcome, err := engine.executePreparedTool(ctx, definition, options.AssistantMessage, prepared, func(event AgentEvent) {
		if event.Type == EventToolExecutionUpdate && options.OnUpdate != nil && event.PartialToolResult != nil {
			options.OnUpdate(cloneToolResult(*event.PartialToolResult))
		}
	})
	if err != nil {
		return publicToolOutcome(rejectedToolOutcome(call, err, ToolFailureHook))
	}
	return publicToolOutcome(outcome)
}
