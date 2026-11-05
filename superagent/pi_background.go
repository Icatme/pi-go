package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"

	"github.com/Icatme/pi-go/agent"
)

// PiBackgroundWorker explicitly adds start/query/stop tools to PiWorker.
// Existing native permission hooks are preserved. Every model tool call also
// obeys the coordinator task review barrier.
// The background tools exist only for the current coordinator-owned Job.
func PiBackgroundWorker(def agent.AgentDefinition) (Worker, error) {
	if _, err := PiWorker(def); err != nil {
		return nil, err
	}
	return func(ctx context.Context, job Job) (Outcome, error) {
		if job.Background == nil {
			return Outcome{}, errors.New("pi background: coordinator capability required")
		}
		owned := def
		owned.CheckToolPermission = func(ctx context.Context, in agent.BeforeToolCallContext) error {
			if err := job.Background.checkToolPermission(ctx); err != nil {
				return err
			}
			if def.CheckToolPermission != nil {
				if err := def.CheckToolPermission(ctx, in); err != nil {
					return err
				}
			}
			return job.Background.checkToolPermission(ctx)
		}
		owned.ToolResolver = func(ctx context.Context, snapshot agent.AgentSnapshot) ([]agent.ToolDefinition, error) {
			tools, err := def.ResolveTools(ctx, snapshot)
			if err != nil {
				return nil, err
			}
			for _, tool := range tools {
				if tool.Name == "background_start" || tool.Name == "background_query" || tool.Name == "background_stop" {
					return nil, errors.New("pi background: reserved tool name collision")
				}
			}
			return append(slices.Clone(tools), piBackgroundTools(job.Background)...), nil
		}
		work, err := PiWorker(owned)
		if err != nil {
			return Outcome{}, err
		}
		return work(ctx, job)
	}, nil
}

func piBackgroundTools(control *BackgroundControl) []agent.ToolDefinition {
	type request struct {
		Tool      string          `json:"tool,omitempty"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
		ID        string          `json:"id,omitempty"`
	}
	makeTool := func(name string) agent.ToolDefinition {
		description := "Query an operation owned by this task. Status and receipt facts, not prose, determine whether it exited."
		props := map[string]any{"id": map[string]any{"type": "string"}}
		required := []string{"id"}
		if name == "background_start" {
			description = "Start one explicitly registered background tool. Started acknowledges durable ownership and quota, not completion. A later user-role event carries final results for the original call. Do not retry an uncertain launch."
			props = map[string]any{"tool": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}}
			required = []string{"tool", "arguments"}
			if control != nil && control.a != nil {
				control.a.mu.Lock()
				names := make([]string, 0, len(control.a.background.tools))
				for n := range control.a.background.tools {
					names = append(names, n)
				}
				control.a.mu.Unlock()
				sort.Strings(names)
				props["tool"].(map[string]any)["enum"] = names
			}
		}
		if name == "background_stop" {
			description = "Request stop of this task's operation. StopRequested is not proof of exit or rollback; a late committed effect is still possible."
		}
		return agent.ToolDefinition{Name: name, Description: description, Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props}, ParseArguments: func(call agent.ToolCall) (any, error) {
			raw := call.Arguments
			if len(raw) == 0 {
				var err error
				raw, err = json.Marshal(call.ParsedArgs)
				if err != nil {
					return nil, err
				}
			}
			fields, err := historyExactObject(raw, required...)
			if err != nil || len(fields) != len(required) {
				return nil, errors.New("pi background: invalid arguments")
			}
			var r request
			if err = json.Unmarshal(raw, &r); err != nil {
				return nil, err
			}
			if name == "background_start" {
				if !validBackgroundStart(BackgroundStart{Tool: r.Tool, CallID: call.ID, Arguments: r.Arguments}) {
					return nil, errors.New("pi background: invalid launch")
				}
			} else if !validIdentity(r.ID) {
				return nil, errors.New("pi background: invalid operation ID")
			}
			return r, nil
		}, Execute: func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
			r, ok := in.Args.(request)
			if !ok {
				return agent.ToolResult{}, errors.New("pi background: invalid arguments")
			}
			if in.CheckPermission != nil {
				if err := in.CheckPermission(ctx); err != nil {
					return agent.ToolResult{}, err
				}
			}
			var o BackgroundOperation
			var err error
			switch name {
			case "background_start":
				o, err = control.Start(ctx, BackgroundStart{Tool: r.Tool, CallID: in.ToolCall.ID, Arguments: r.Arguments})
			case "background_query":
				o, err = control.Inspect(ctx, r.ID)
			case "background_stop":
				o, err = control.Stop(ctx, r.ID)
			}
			if err != nil {
				return agent.ToolResult{}, err
			}
			raw, err := json.Marshal(o)
			if err != nil {
				return agent.ToolResult{}, err
			}
			return agent.ToolResult{Content: []agent.Part{agent.NewTextPart(string(raw))}}, nil
		}}
	}
	return []agent.ToolDefinition{makeTool("background_start"), makeTool("background_query"), makeTool("background_stop")}
}
