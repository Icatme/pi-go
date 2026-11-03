package agent

// These positional literals intentionally enforce the existing public struct
// shapes. Adding receipt fields would break downstream unkeyed literals.
var (
	_ = ToolExecutionInfo{"", "", nil}
	_ = ToolResult{nil, nil, nil, false, false, nil, nil, nil}
	_ = ToolCallOutcome{ToolCall{}, ToolResult{}, false, nil, nil, ToolExecutionInfo{}}
	_ = RunToolCallOptions{nil, Message{}, AgentContext{}, nil, nil, nil, nil, nil, "", ""}
	_ = ToolExecutionContext{nil, ToolCall{}, nil, AgentContext{}, nil, nil, nil, false, nil}
	_ = ToolDefinition{nil, "", "", "", "", nil, nil, "", nil, 0, nil, nil, nil, ChildCallLimits{}, nil}
)
