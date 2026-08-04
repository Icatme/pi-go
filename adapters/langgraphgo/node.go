// Package langgraphgo adapts agent runtimes into langgraphgo nodes.
//
// This package is intentionally limited to bridge concerns at the graph
// boundary. It may map thread/session identifiers, state shapes, and runtime
// callbacks into langgraphgo constructs, but it must not redefine agent
// core runtime semantics.
package langgraphgo

import (
	"context"

	"github.com/Icatme/pi-go/agent"
	"github.com/smallnest/langgraphgo/graph"
)

// RunMode controls how a graph-bound agent node should execute.
type RunMode string

const (
	// RunModePrompt appends prompts and executes the runtime.
	RunModePrompt RunMode = "prompt"
	// RunModeContinue resumes execution from existing snapshot state.
	RunModeContinue RunMode = "continue"
	// RunModeSkip leaves state unchanged.
	RunModeSkip RunMode = "skip"
)

// Binder maps graph state into and out of a agent snapshot.
//
// Binder is a graph-boundary helper. It should only describe how outer graph
// state is projected into agent runtime state and back.
type Binder[S any] struct {
	GetSnapshot         func(S) agent.AgentSnapshot
	SetSnapshot         func(S, agent.AgentSnapshot) S
	GetPrompts          func(S) []agent.Message
	SetPrompts          func(S, []agent.Message) S
	GetMode             func(S) RunMode
	SetMode             func(S, RunMode) S
	SelectMode          func(S, agent.AgentSnapshot, []agent.Message) RunMode
	ResolveDefinition   func(context.Context, S, agent.AgentSnapshot, agent.AgentDefinition) (agent.AgentDefinition, error)
	GetSteeringMessages func(context.Context, S, agent.AgentSnapshot) ([]agent.Message, error)
	SetSteeringMessages func(S, []agent.Message) S
	GetFollowUpMessages func(context.Context, S, agent.AgentSnapshot) ([]agent.Message, error)
	SetFollowUpMessages func(S, []agent.Message) S
}

// NewTurnNode wraps a agent engine into a single langgraphgo node.
func NewTurnNode[S any](engine *agent.Engine, definition agent.AgentDefinition, binder Binder[S]) func(context.Context, S) (S, error) {
	binder.SelectMode = func(_ S, _ agent.AgentSnapshot, _ []agent.Message) RunMode {
		return RunModePrompt
	}
	return NewSessionNode(engine, definition, binder)
}

// NewSessionNode wraps a agent engine into a stateful graph node that can prompt, continue, or skip.
func NewSessionNode[S any](engine *agent.Engine, definition agent.AgentDefinition, binder Binder[S]) func(context.Context, S) (S, error) {
	if engine == nil {
		engine = agent.NewEngine()
	}

	return func(ctx context.Context, state S) (S, error) {
		threadID := threadIDFromContext(ctx)
		snapshot := normalizeSnapshotSessionID(binder.GetSnapshot(state), threadID)
		prompts := binder.GetPrompts(state)
		mode := defaultRunMode(state, binder, snapshot, prompts)

		var (
			nextSnapshot *agent.AgentSnapshot
			err          error
		)
		switch mode {
		case RunModeSkip:
			nextSnapshot = &snapshot
		case RunModeContinue:
			pending, nextState, pendingErr := consumeContinueMessages(ctx, state, snapshot, binder)
			if pendingErr != nil {
				return state, pendingErr
			}
			state = nextState
			hooks := loopHooks(state, snapshot, binder)
			if len(pending) > 0 {
				nextSnapshot, err = engine.RunWithHooks(ctx, definition, &snapshot, pending, nil, hooks)
			} else {
				nextSnapshot, err = engine.ContinueWithHooks(ctx, definition, &snapshot, nil, hooks)
			}
		default:
			hooks := loopHooks(state, snapshot, binder)
			nextSnapshot, err = engine.RunWithHooks(ctx, definition, &snapshot, prompts, nil, hooks)
		}
		if err != nil {
			return state, err
		}
		normalizedSnapshot := normalizeSnapshotSessionID(*nextSnapshot, threadID)

		if binder.SetPrompts != nil {
			state = binder.SetPrompts(state, nil)
		}
		if binder.SetSteeringMessages != nil {
			state = binder.SetSteeringMessages(state, nil)
		}
		if binder.SetFollowUpMessages != nil {
			state = binder.SetFollowUpMessages(state, nil)
		}
		if binder.SetMode != nil {
			state = binder.SetMode(state, "")
		}
		return binder.SetSnapshot(state, normalizedSnapshot), nil
	}
}

// NewTurnGraph creates a minimal graph with a single agent-backed turn node.
func NewTurnGraph[S any](engine *agent.Engine, definition agent.AgentDefinition, binder Binder[S]) *graph.StateGraph[S] {
	binder.SelectMode = func(_ S, _ agent.AgentSnapshot, _ []agent.Message) RunMode {
		return RunModePrompt
	}
	return NewSessionGraph(engine, definition, binder)
}

// NewSessionGraph creates a minimal graph with a single agent-backed session node.
func NewSessionGraph[S any](engine *agent.Engine, definition agent.AgentDefinition, binder Binder[S]) *graph.StateGraph[S] {
	g := graph.NewStateGraph[S]()
	g.AddNode(SessionNodeName, SessionNodeDescription, NewSessionNode(engine, definition, binder))
	g.SetEntryPoint(SessionNodeName)
	g.AddEdge(SessionNodeName, graph.END)
	return g
}

func defaultRunMode[S any](state S, binder Binder[S], snapshot agent.AgentSnapshot, prompts []agent.Message) RunMode {
	if binder.SelectMode != nil {
		return binder.SelectMode(state, snapshot, prompts)
	}
	if binder.GetMode != nil {
		if mode := binder.GetMode(state); mode != "" {
			return mode
		}
	}
	if len(prompts) > 0 {
		return RunModePrompt
	}
	if len(snapshot.Messages) == 0 {
		return RunModeSkip
	}
	if snapshot.Messages[len(snapshot.Messages)-1].Role == agent.RoleAssistant {
		return RunModeSkip
	}
	return RunModeContinue
}

func loopHooks[S any](state S, snapshot agent.AgentSnapshot, binder Binder[S]) agent.LoopHooks {
	return agent.LoopHooks{
		ResolveDefinition: func(ctx context.Context, current agent.AgentDefinition, snapshot agent.AgentSnapshot) (agent.AgentDefinition, error) {
			if binder.ResolveDefinition == nil {
				return current, nil
			}
			return binder.ResolveDefinition(ctx, state, snapshot, current)
		},
		GetSteeringMessages: func(ctx context.Context) ([]agent.Message, error) {
			if binder.GetSteeringMessages == nil {
				return nil, nil
			}
			return binder.GetSteeringMessages(ctx, state, snapshot)
		},
		GetFollowUpMessages: func(ctx context.Context) ([]agent.Message, error) {
			if binder.GetFollowUpMessages == nil {
				return nil, nil
			}
			return binder.GetFollowUpMessages(ctx, state, snapshot)
		},
	}
}

func consumeContinueMessages[S any](ctx context.Context, state S, snapshot agent.AgentSnapshot, binder Binder[S]) ([]agent.Message, S, error) {
	if binder.GetSteeringMessages != nil && binder.SetSteeringMessages != nil {
		messages, err := binder.GetSteeringMessages(ctx, state, snapshot)
		if err != nil {
			return nil, state, err
		}
		if len(messages) > 0 {
			return messages, binder.SetSteeringMessages(state, nil), nil
		}
	}

	if binder.GetFollowUpMessages != nil && binder.SetFollowUpMessages != nil {
		messages, err := binder.GetFollowUpMessages(ctx, state, snapshot)
		if err != nil {
			return nil, state, err
		}
		if len(messages) > 0 {
			return messages, binder.SetFollowUpMessages(state, nil), nil
		}
	}

	return nil, state, nil
}

func threadIDFromContext(ctx context.Context) string {
	return threadIDFromConfig(graph.GetConfig(ctx))
}

func threadIDFromConfig(config *graph.Config) string {
	if config == nil || config.Configurable == nil {
		return ""
	}
	threadID, _ := config.Configurable["thread_id"].(string)
	return threadID
}

func normalizeSnapshotSessionID(snapshot agent.AgentSnapshot, threadID string) agent.AgentSnapshot {
	if threadID == "" {
		return snapshot
	}
	snapshot.SessionID = threadID
	return snapshot
}
