package codemodetool

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/codemode"
)

// BindingResolver captures a host-authorized directory at the start of each
// script. It receives no script-controlled identity, policy or tool selection.
type BindingResolver func(context.Context) ([]Binding, []codemode.Namespace, error)

type dynamicState struct {
	mu   sync.Mutex
	id   string
	tool agent.ToolDefinition
}

// NewDynamic adds lazy discovery without changing the model-visible schema or
// storing mutable executors in history. The Agent freezes the resolved children
// before entering the script. Ordinary New remains a static allowlist binding.
func NewDynamic(sandbox *codemode.Sandbox, resolver BindingResolver, options Options) (agent.ToolDefinition, error) {
	if resolver == nil {
		return agent.ToolDefinition{}, errors.New("codemodetool: binding resolver is required")
	}
	options.storeKey = new(byte)
	if options.MaxOutputTokens == 0 {
		options.MaxOutputTokens = 2000
	}
	initial := options
	initial.Namespaces = nil
	tool, err := New(sandbox, nil, initial)
	if err != nil {
		return agent.ToolDefinition{}, err
	}
	tool.ChildTools = nil
	key := new(byte)
	stateFor := func(execution agent.ToolExecutionContext) (*dynamicState, error) {
		if execution.Invocation == nil {
			return nil, errors.New("codemodetool: parent invocation is required")
		}
		value, err := execution.Invocation.LoadOrCreate(key, func() (any, error) { return &dynamicState{}, nil })
		if err != nil {
			return nil, err
		}
		state, ok := value.(*dynamicState)
		if !ok {
			return nil, errors.New("codemodetool: invalid dynamic state")
		}
		return state, nil
	}
	tool.ResolveChildTools = func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ChildToolResolution, error) {
		args, ok := execution.Args.(map[string]any)
		if !ok {
			return agent.ChildToolResolution{}, errors.New("codemodetool: expected code arguments")
		}
		code, ok := args["code"].(string)
		if !ok {
			return agent.ChildToolResolution{}, errors.New("codemodetool: invalid code")
		}
		timeout, _, err := sandbox.SourceLimits(code, codemode.RunOptions{Timeout: options.Timeout, MaxOutputTokens: options.MaxOutputTokens})
		if err != nil {
			return agent.ChildToolResolution{}, &agent.ToolExecutionError{Code: agent.ToolFailureArgumentInvalid, Reason: "options", Message: "Codemode source header or execution limit is invalid", Err: err, Execution: agent.ToolExecutionInfo{Remote: agent.ToolRemoteNotDispatched}}
		}
		var cancel context.CancelFunc
		var deadline time.Time
		if timeout > 0 {
			deadline = time.Now().Add(timeout)
			ctx, cancel = context.WithDeadline(ctx, deadline)
		} else {
			ctx, cancel = context.WithCancel(ctx)
		}
		defer cancel()
		state, err := stateFor(execution)
		if err != nil {
			return agent.ChildToolResolution{}, err
		}
		bindings, namespaces, err := resolver(ctx)
		if err != nil {
			code := agent.ToolFailurePolicyDenied
			if errors.Is(err, context.DeadlineExceeded) {
				code = agent.ToolFailureDeadline
			} else if errors.Is(err, context.Canceled) {
				code = agent.ToolFailureCanceled
			}
			return agent.ChildToolResolution{}, &agent.ToolExecutionError{Code: code, Reason: "directory_unavailable", Message: "Codemode tool directory unavailable; reconnect or reauthenticate through the host", Err: err, Execution: agent.ToolExecutionInfo{Local: agent.ToolLocalNotStarted, Remote: agent.ToolRemoteNotDispatched}}
		}
		resolvedOptions := options
		resolvedOptions.Namespaces = namespaces
		resolved, err := New(sandbox, bindings, resolvedOptions)
		if err != nil {
			return agent.ChildToolResolution{}, &agent.ToolExecutionError{Code: agent.ToolFailureArgumentInvalid, Reason: "catalog", Message: "Codemode directory does not satisfy its schema or limits", Err: err, Execution: agent.ToolExecutionInfo{Local: agent.ToolLocalNotStarted, Remote: agent.ToolRemoteNotDispatched}}
		}
		state.mu.Lock()
		state.id = execution.ToolCall.ID
		state.tool = resolved
		state.mu.Unlock()
		return agent.ChildToolResolution{Tools: resolved.ChildTools, Deadline: deadline}, nil
	}
	tool.Execute = func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
		state, err := stateFor(execution)
		if err != nil {
			return agent.ToolResult{}, err
		}
		state.mu.Lock()
		resolved := state.tool
		matches := state.id == execution.ToolCall.ID && resolved.Execute != nil
		state.mu.Unlock()
		if !matches {
			return agent.ToolResult{}, &agent.ToolExecutionError{Code: agent.ToolFailurePolicyDenied, Message: "Codemode directory was not resolved for this invocation", Execution: agent.ToolExecutionInfo{Local: agent.ToolLocalNotStarted, Remote: agent.ToolRemoteNotDispatched}}
		}
		defer func() {
			state.mu.Lock()
			if state.id == execution.ToolCall.ID {
				state.id = ""
				state.tool = agent.ToolDefinition{}
			}
			state.mu.Unlock()
		}()
		return resolved.Execute(ctx, execution)
	}
	return tool, nil
}
