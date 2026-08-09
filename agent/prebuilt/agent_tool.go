package prebuilt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	core "github.com/Icatme/pi-go/agent"
)

// AgentToolLimits bounds one child-agent invocation. Timeout is a cooperative
// cancellation deadline; child models, tools, and streams must honor context
// cancellation.
type AgentToolLimits struct {
	MaxDepth int
	MaxTurns int
	Timeout  time.Duration
}

// AgentToolConfig configures a child agent exposed as a tool.
type AgentToolConfig struct {
	Definition  core.AgentDefinition
	Description string
	Limits      AgentToolLimits
}

// AgentToolEventDetails carries one transient event from a child run.
type AgentToolEventDetails struct {
	Depth int             `json:"depth"`
	Event core.AgentEvent `json:"event"`
	Error string          `json:"error,omitempty"`
}

// AgentToolResultDetails identifies the child run that produced a final result.
type AgentToolResultDetails struct {
	RunID       string          `json:"run_id"`
	ParentRunID string          `json:"parent_run_id,omitempty"`
	AgentName   string          `json:"agent_name"`
	Depth       int             `json:"depth"`
	Turns       int             `json:"turns"`
	StopReason  core.StopReason `json:"stop_reason,omitempty"`
}

type agentToolLedger struct {
	depth    int
	maxDepth int
}

type agentToolLedgerContextKey struct{}

// NewAgentTool creates a strict task-only tool backed by an isolated Runner.
func NewAgentTool(config AgentToolConfig) (core.ToolDefinition, error) {
	if strings.TrimSpace(config.Definition.Name) == "" {
		return core.ToolDefinition{}, errors.New("prebuilt: agent tool definition name is required")
	}
	if strings.TrimSpace(config.Description) == "" {
		return core.ToolDefinition{}, errors.New("prebuilt: agent tool description is required")
	}
	if config.Definition.MaxTurns < 0 {
		return core.ToolDefinition{}, errors.New("prebuilt: agent tool definition max turns cannot be negative")
	}
	if config.Limits.MaxDepth < 0 {
		return core.ToolDefinition{}, errors.New("prebuilt: agent tool max depth cannot be negative")
	}
	if config.Limits.MaxTurns < 0 {
		return core.ToolDefinition{}, errors.New("prebuilt: agent tool max turns cannot be negative")
	}
	if config.Limits.Timeout < 0 {
		return core.ToolDefinition{}, errors.New("prebuilt: agent tool timeout cannot be negative")
	}

	definition := config.Definition
	if limit := config.Limits.MaxTurns; limit > 0 && (definition.MaxTurns == 0 || definition.MaxTurns > limit) {
		definition.MaxTurns = limit
	}
	runner, err := core.NewRunner(definition)
	if err != nil {
		return core.ToolDefinition{}, fmt.Errorf("prebuilt: create runner for agent tool %q: %w", definition.Name, err)
	}

	name := definition.Name
	maxDepth := config.Limits.MaxDepth
	timeout := config.Limits.Timeout
	return core.ToolDefinition{
		Name:        name,
		Description: config.Description,
		Parameters:  cloneAgentToolParameters(),
		Execute: func(ctx context.Context, _ string, args any, update core.ToolUpdateFunc) (core.ToolResult, error) {
			task, err := agentToolTask(args)
			if err != nil {
				return core.ToolResult{}, err
			}
			if err := agentToolContextError(ctx); err != nil {
				return core.ToolResult{}, err
			}

			ledger, err := nextAgentToolLedger(ctx, maxDepth)
			if err != nil {
				return core.ToolResult{}, fmt.Errorf("prebuilt: agent tool %q: %w", name, err)
			}
			childCtx := context.WithValue(ctx, agentToolLedgerContextKey{}, ledger)
			if timeout > 0 {
				var cancel context.CancelFunc
				childCtx, cancel = context.WithTimeout(childCtx, timeout)
				defer cancel()
			}

			stream := runner.Query(childCtx, task)
			var (
				primaryErr             error
				runID                  string
				parentRunID            string
				turns                  int
				terminalToolCallCounts = make(map[string]int)
			)
			for event := range stream.Events() {
				if runID == "" {
					runID = event.RunID
					parentRunID = event.ParentRunID
				}
				if event.Type == core.EventTurnStart {
					turns++
					terminalToolCallCounts = make(map[string]int)
				}
				if event.Type == core.EventToolExecutionEnd && event.ToolCallID != "" && event.ToolResult != nil && event.ToolResult.Terminate && !event.IsError {
					terminalToolCallCounts[event.ToolCallID]++
				}

				eventError := ""
				if event.Err != nil {
					if primaryErr == nil {
						primaryErr = event.Err
					}
					eventError = event.Err.Error()
					event.Err = nil
				}
				if update != nil {
					update(core.ToolResult{Details: AgentToolEventDetails{
						Depth: ledger.depth,
						Event: event,
						Error: eventError,
					}})
				}
			}

			snapshot, waitErr := stream.Wait()
			if primaryErr != nil {
				return core.ToolResult{}, agentToolErrorWithContext(primaryErr, childCtx)
			}
			if waitErr != nil {
				return core.ToolResult{}, agentToolErrorWithContext(waitErr, childCtx)
			}
			if snapshot.Error != "" {
				if err := agentToolContextError(childCtx); err != nil && agentToolErrorTextMatchesContext(snapshot.Error, childCtx) {
					return core.ToolResult{}, err
				}
				return core.ToolResult{}, errors.New(snapshot.Error)
			}

			content, stopReason, err := finalAgentToolContent(snapshot.Messages, name, terminalToolCallCounts)
			if err != nil {
				return core.ToolResult{}, err
			}
			if runID == "" {
				return core.ToolResult{}, fmt.Errorf("prebuilt: agent tool %q child protocol error: run emitted no run metadata", name)
			}
			return core.ToolResult{
				Content: content,
				Details: AgentToolResultDetails{
					RunID:       runID,
					ParentRunID: parentRunID,
					AgentName:   name,
					Depth:       ledger.depth,
					Turns:       turns,
					StopReason:  stopReason,
				},
				Terminate: false,
			}, nil
		},
	}, nil
}

func agentToolTask(args any) (string, error) {
	object, ok := args.(map[string]any)
	if !ok {
		return "", errors.New("prebuilt: agent tool arguments must be an object containing only task")
	}
	if len(object) != 1 {
		return "", errors.New("prebuilt: agent tool arguments must contain only task")
	}
	task, ok := object["task"].(string)
	if !ok {
		return "", errors.New("prebuilt: agent tool task must be a string")
	}
	if strings.TrimSpace(task) == "" {
		return "", errors.New("prebuilt: agent tool task cannot be empty")
	}
	return task, nil
}

func nextAgentToolLedger(ctx context.Context, configuredMax int) (agentToolLedger, error) {
	inherited, inheritedOK := ctx.Value(agentToolLedgerContextKey{}).(agentToolLedger)
	if !inheritedOK {
		effectiveMax := configuredMax
		if effectiveMax == 0 {
			effectiveMax = 1
		}
		return agentToolLedger{depth: 1, maxDepth: effectiveMax}, nil
	}

	effectiveMax := inherited.maxDepth
	if configuredMax > 0 && configuredMax < effectiveMax {
		effectiveMax = configuredMax
	}
	depth := inherited.depth + 1
	if depth > effectiveMax {
		return agentToolLedger{}, fmt.Errorf("maximum depth %d exceeded at depth %d", effectiveMax, depth)
	}
	return agentToolLedger{depth: depth, maxDepth: effectiveMax}, nil
}

func finalAgentToolContent(messages []core.Message, name string, terminalToolCallCounts map[string]int) ([]core.Part, core.StopReason, error) {
	if len(messages) == 0 {
		return nil, "", fmt.Errorf("prebuilt: agent tool %q child protocol error: run produced no output", name)
	}

	last := messages[len(messages)-1]
	switch last.Role {
	case core.RoleAssistant:
		if strings.TrimSpace(last.ErrorMessage) != "" || last.StopReason != core.StopReasonStop {
			return nil, last.StopReason, agentToolAssistantError(name, last)
		}
		if len(last.Parts) == 0 {
			return nil, last.StopReason, fmt.Errorf("prebuilt: agent tool %q child protocol error: final assistant produced no output", name)
		}
		return append([]core.Part(nil), last.Parts...), last.StopReason, nil
	case core.RoleTool:
		if last.ToolResult == nil {
			return nil, nearestAgentToolStopReason(messages[:len(messages)-1]), fmt.Errorf("prebuilt: agent tool %q child protocol error: final tool message has no result", name)
		}
		stopReason := nearestAgentToolStopReason(messages[:len(messages)-1])
		if last.ToolResult.IsError {
			return nil, stopReason, agentToolResultError(name, *last.ToolResult)
		}
		assistantIndex := -1
		for i := len(messages) - 2; i >= 0; i-- {
			if messages[i].Role == core.RoleAssistant {
				assistantIndex = i
				break
			}
		}
		if assistantIndex < 0 || stopReason != core.StopReasonToolUse {
			return nil, stopReason, fmt.Errorf("prebuilt: agent tool %q child protocol error: non-terminal tool result cannot be final output", name)
		}
		remainingTerminalCalls := make(map[string]int, len(terminalToolCallCounts))
		for id, count := range terminalToolCallCounts {
			remainingTerminalCalls[id] = count
		}
		for _, message := range messages[assistantIndex+1:] {
			if message.Role != core.RoleTool || message.ToolResult == nil {
				return nil, stopReason, fmt.Errorf("prebuilt: agent tool %q child protocol error: invalid terminal tool batch", name)
			}
			if message.ToolResult.IsError {
				return nil, stopReason, agentToolResultError(name, *message.ToolResult)
			}
			if remainingTerminalCalls[message.ToolResult.ToolCallID] == 0 {
				return nil, stopReason, fmt.Errorf("prebuilt: agent tool %q child protocol error: non-terminal tool result cannot be final output", name)
			}
			remainingTerminalCalls[message.ToolResult.ToolCallID]--
		}
		if len(last.ToolResult.Content) == 0 {
			return nil, stopReason, fmt.Errorf("prebuilt: agent tool %q child protocol error: terminal tool result produced no output", name)
		}
		return append([]core.Part(nil), last.ToolResult.Content...), stopReason, nil
	default:
		return nil, "", fmt.Errorf("prebuilt: agent tool %q child protocol error: run produced no final assistant or terminal tool output", name)
	}
}

func agentToolContextError(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	cause := context.Cause(ctx)
	if cause == nil || errors.Is(ctx.Err(), cause) || errors.Is(cause, ctx.Err()) {
		if cause != nil {
			return cause
		}
		return ctx.Err()
	}
	return errors.Join(ctx.Err(), cause)
}

func agentToolErrorWithContext(err error, ctx context.Context) error {
	if err == nil || ctx == nil || ctx.Err() == nil {
		return err
	}
	cause := context.Cause(ctx)
	if !errors.Is(err, ctx.Err()) && (cause == nil || !errors.Is(err, cause)) {
		return err
	}
	return errors.Join(err, agentToolContextError(ctx))
}

func agentToolErrorTextMatchesContext(text string, ctx context.Context) bool {
	if ctx == nil || ctx.Err() == nil {
		return false
	}
	if strings.Contains(text, ctx.Err().Error()) {
		return true
	}
	cause := context.Cause(ctx)
	return cause != nil && strings.Contains(text, cause.Error())
}

func nearestAgentToolStopReason(messages []core.Message) core.StopReason {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == core.RoleAssistant {
			return messages[i].StopReason
		}
	}
	return ""
}

func agentToolAssistantError(name string, message core.Message) error {
	if text := strings.TrimSpace(message.ErrorMessage); text != "" {
		return errors.New(text)
	}
	return fmt.Errorf("prebuilt: agent tool %q child assistant stopped with reason %q", name, message.StopReason)
}

func agentToolResultError(name string, result core.ToolResultPayload) error {
	var text []string
	for _, part := range result.Content {
		if part.Type == core.PartTypeText && strings.TrimSpace(part.Text) != "" {
			text = append(text, part.Text)
		}
	}
	if len(text) > 0 {
		return errors.New(strings.Join(text, "\n"))
	}
	return fmt.Errorf("prebuilt: agent tool %q child tool result reported an error", name)
}

func cloneAgentToolParameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
		},
		"required":             []string{"task"},
		"additionalProperties": false,
	}
}
