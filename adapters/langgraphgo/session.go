package langgraphgo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Icatme/pi-go/agent"
	"github.com/smallnest/langgraphgo/graph"
)

const (
	// SessionNodeName is the default node name for the built-in session graph.
	SessionNodeName = "agent_session"
	// SessionNodeDescription is the default node description for the built-in session graph.
	SessionNodeDescription = "agent session"
)

// SessionState is the standard langgraphgo state shape for a single agent session.
//
// Snapshot stores durable runtime state, while Prompts, Steering, and FollowUps
// are queued inputs waiting to be consumed by the next session-node run.
//
// This type exists only as a graph-facing adapter shape. It must not become a
// second core runtime state model parallel to agent.AgentSnapshot.
type SessionState struct {
	Snapshot  agent.AgentSnapshot `json:"snapshot,omitempty"`
	Prompts   []agent.Message     `json:"prompts,omitempty"`
	Steering  []agent.Message     `json:"steering,omitempty"`
	FollowUps []agent.Message     `json:"follow_ups,omitempty"`
	Mode      RunMode             `json:"mode,omitempty"`
}

// WithMode returns a copy of the state with the requested run mode.
func (s SessionState) WithMode(mode RunMode) SessionState {
	s.Mode = mode
	return s
}

// Continue returns a copy of the state configured to resume the session node.
func (s SessionState) Continue() SessionState {
	return s.WithMode(RunModeContinue)
}

// Skip returns a copy of the state configured to skip the session node.
func (s SessionState) Skip() SessionState {
	return s.WithMode(RunModeSkip)
}

// AppendPrompts queues prompt messages for the next session run.
func (s SessionState) AppendPrompts(messages ...agent.Message) SessionState {
	if len(messages) == 0 {
		return s
	}
	s.Prompts = append(s.Prompts, cloneMessages(messages)...)
	s.Mode = RunModePrompt
	return s
}

// AppendSteering queues steering messages for the next assistant turn.
func (s SessionState) AppendSteering(messages ...agent.Message) SessionState {
	if len(messages) == 0 {
		return s
	}
	s.Steering = append(s.Steering, cloneMessages(messages)...)
	if s.Mode == "" {
		s.Mode = RunModeContinue
	}
	return s
}

// AppendFollowUps queues follow-up messages for the next assistant turn.
func (s SessionState) AppendFollowUps(messages ...agent.Message) SessionState {
	if len(messages) == 0 {
		return s
	}
	s.FollowUps = append(s.FollowUps, cloneMessages(messages)...)
	if s.Mode == "" {
		s.Mode = RunModeContinue
	}
	return s
}

// PromptUpdate creates a partial state update that appends prompt messages.
func PromptUpdate(messages ...agent.Message) SessionState {
	return SessionState{}.AppendPrompts(messages...)
}

// SteeringUpdate creates a partial state update that appends steering messages.
func SteeringUpdate(messages ...agent.Message) SessionState {
	return SessionState{}.AppendSteering(messages...)
}

// FollowUpUpdate creates a partial state update that appends follow-up messages.
func FollowUpUpdate(messages ...agent.Message) SessionState {
	return SessionState{}.AppendFollowUps(messages...)
}

// ContinueUpdate creates a partial state update that resumes the session node.
func ContinueUpdate() SessionState {
	return SessionState{Mode: RunModeContinue}
}

// SessionStateBinder returns a binder for the standard SessionState shape.
func SessionStateBinder() Binder[SessionState] {
	return Binder[SessionState]{
		GetSnapshot: func(state SessionState) agent.AgentSnapshot {
			return cloneSnapshot(state.Snapshot)
		},
		SetSnapshot: func(state SessionState, snapshot agent.AgentSnapshot) SessionState {
			state.Snapshot = cloneSnapshot(snapshot)
			return state
		},
		GetPrompts: func(state SessionState) []agent.Message {
			return cloneMessages(state.Prompts)
		},
		SetPrompts: func(state SessionState, prompts []agent.Message) SessionState {
			state.Prompts = cloneMessages(prompts)
			return state
		},
		GetMode: func(state SessionState) RunMode {
			return state.Mode
		},
		SetMode: func(state SessionState, mode RunMode) SessionState {
			state.Mode = mode
			return state
		},
		SelectMode: func(state SessionState, snapshot agent.AgentSnapshot, prompts []agent.Message) RunMode {
			if state.Mode != "" {
				return state.Mode
			}
			if len(prompts) > 0 {
				return RunModePrompt
			}
			if len(state.Steering) > 0 || len(state.FollowUps) > 0 {
				return RunModeContinue
			}
			if len(snapshot.Messages) == 0 {
				return RunModeSkip
			}
			if snapshot.Messages[len(snapshot.Messages)-1].Role == agent.RoleAssistant {
				return RunModeSkip
			}
			return RunModeContinue
		},
		GetSteeringMessages: func(_ context.Context, state SessionState, _ agent.AgentSnapshot) ([]agent.Message, error) {
			return cloneMessages(state.Steering), nil
		},
		SetSteeringMessages: func(state SessionState, messages []agent.Message) SessionState {
			state.Steering = cloneMessages(messages)
			return state
		},
		GetFollowUpMessages: func(_ context.Context, state SessionState, _ agent.AgentSnapshot) ([]agent.Message, error) {
			return cloneMessages(state.FollowUps), nil
		},
		SetFollowUpMessages: func(state SessionState, messages []agent.Message) SessionState {
			state.FollowUps = cloneMessages(messages)
			return state
		},
	}
}

// SessionStateSchema returns a schema that preserves durable snapshot state while
// allowing partial prompt or HITL updates to merge cleanly during checkpoint resume.
func SessionStateSchema() *graph.StructSchema[SessionState] {
	return graph.NewStructSchema(SessionState{}, mergeSessionState)
}

// NewSessionStateNode wraps the standard SessionState with a session-aware binder.
func NewSessionStateNode(engine *agent.Engine, definition agent.AgentDefinition) func(context.Context, SessionState) (SessionState, error) {
	return NewSessionNode(engine, definition, SessionStateBinder())
}

// NewSessionStateGraph creates a single-node StateGraph using SessionState and its schema.
func NewSessionStateGraph(engine *agent.Engine, definition agent.AgentDefinition) *graph.StateGraph[SessionState] {
	g := graph.NewStateGraph[SessionState]()
	g.SetSchema(SessionStateSchema())
	g.AddNode(SessionNodeName, SessionNodeDescription, NewSessionStateNode(engine, definition))
	g.SetEntryPoint(SessionNodeName)
	g.AddEdge(SessionNodeName, graph.END)
	return g
}

// NewCheckpointableSessionStateGraph creates a checkpointable session graph using SessionState.
func NewCheckpointableSessionStateGraph(engine *agent.Engine, definition agent.AgentDefinition) *graph.CheckpointableStateGraph[SessionState] {
	g := graph.NewCheckpointableStateGraph[SessionState]()
	g.SetSchema(SessionStateSchema())
	g.AddNode(SessionNodeName, SessionNodeDescription, NewSessionStateNode(engine, definition))
	g.SetEntryPoint(SessionNodeName)
	g.AddEdge(SessionNodeName, graph.END)
	return g
}

// UpdateSessionState merges a partial SessionState update and checkpoints it at the session node.
//
// Using SessionNodeName here guarantees that auto-resume continues from a real
// graph node instead of an out-of-band checkpoint label. Use ResumeSession with
// the returned config to continue from the newly updated checkpoint.
func UpdateSessionState(ctx context.Context, runnable *graph.CheckpointableRunnable[SessionState], config *graph.Config, update SessionState) (*graph.Config, error) {
	if hasSnapshot(update.Snapshot) {
		update.Snapshot = normalizeSnapshotSessionID(update.Snapshot, threadIDFromConfig(config))
	}
	return runnable.UpdateState(ctx, config, SessionNodeName, update)
}

// ResumeSession loads a checkpointed SessionState and resumes execution from the session node.
//
// This helper is the recommended counterpart to UpdateSessionState because the
// underlying graph checkpoint API does not auto-resume from out-of-band updates
// by thread ID alone.
func ResumeSession(ctx context.Context, runnable *graph.CheckpointableRunnable[SessionState], config *graph.Config) (SessionState, error) {
	state, resumeConfig, err := LoadSessionState(ctx, runnable, config)
	if err != nil {
		return SessionState{}, err
	}
	resumeConfig.ResumeFrom = []string{SessionNodeName}
	return runnable.InvokeWithConfig(ctx, state, resumeConfig)
}

// LoadSessionState resolves a typed SessionState and config from a checkpoint config.
func LoadSessionState(ctx context.Context, runnable *graph.CheckpointableRunnable[SessionState], config *graph.Config) (SessionState, *graph.Config, error) {
	snapshot, err := runnable.GetState(ctx, config)
	if err != nil {
		return SessionState{}, nil, err
	}

	state, ok := snapshot.Values.(SessionState)
	if !ok {
		return SessionState{}, nil, fmt.Errorf("unexpected session state type %T", snapshot.Values)
	}

	threadID := threadIDFromConfig(config)
	if threadID == "" {
		threadID = threadIDFromConfig(&snapshot.Config)
	}

	return normalizeSessionStateThreadID(cloneSessionState(state), threadID), cloneConfig(&snapshot.Config), nil
}

func mergeSessionState(current, update SessionState) (SessionState, error) {
	if hasSnapshot(update.Snapshot) {
		return SessionState{
			Snapshot:  cloneSnapshot(update.Snapshot),
			Prompts:   cloneMessages(update.Prompts),
			Steering:  cloneMessages(update.Steering),
			FollowUps: cloneMessages(update.FollowUps),
			Mode:      update.Mode,
		}, nil
	}

	result := cloneSessionState(current)
	if len(update.Prompts) > 0 {
		result.Prompts = append(result.Prompts, cloneMessages(update.Prompts)...)
	}
	if len(update.Steering) > 0 {
		result.Steering = append(result.Steering, cloneMessages(update.Steering)...)
	}
	if len(update.FollowUps) > 0 {
		result.FollowUps = append(result.FollowUps, cloneMessages(update.FollowUps)...)
	}
	if update.Mode != "" {
		result.Mode = update.Mode
	}
	return result, nil
}

func hasSnapshot(snapshot agent.AgentSnapshot) bool {
	return snapshot.SessionID != "" ||
		snapshot.SystemPrompt != "" ||
		snapshot.Error != "" ||
		snapshot.Model.Provider != "" ||
		snapshot.Model.Model != "" ||
		!isZeroProviderConfig(snapshot.Model.ProviderConfig) ||
		len(snapshot.Model.Metadata) > 0 ||
		len(snapshot.Messages) > 0 ||
		len(snapshot.PendingToolCalls) > 0 ||
		len(snapshot.Metadata) > 0
}

func cloneSessionState(state SessionState) SessionState {
	return SessionState{
		Snapshot:  cloneSnapshot(state.Snapshot),
		Prompts:   cloneMessages(state.Prompts),
		Steering:  cloneMessages(state.Steering),
		FollowUps: cloneMessages(state.FollowUps),
		Mode:      state.Mode,
	}
}

func normalizeSessionStateThreadID(state SessionState, threadID string) SessionState {
	state.Snapshot = normalizeSnapshotSessionID(state.Snapshot, threadID)
	return state
}

func cloneSnapshot(snapshot agent.AgentSnapshot) agent.AgentSnapshot {
	return agent.AgentSnapshot{
		SessionID:        snapshot.SessionID,
		SystemPrompt:     snapshot.SystemPrompt,
		Model:            cloneModelRef(snapshot.Model),
		Messages:         cloneMessages(snapshot.Messages),
		PendingToolCalls: clonePendingToolCalls(snapshot.PendingToolCalls),
		Error:            snapshot.Error,
		Metadata:         cloneStringAnyMap(snapshot.Metadata),
	}
}

func cloneMessages(messages []agent.Message) []agent.Message {
	if len(messages) == 0 {
		return nil
	}

	cloned := make([]agent.Message, 0, len(messages))
	for _, message := range messages {
		cloned = append(cloned, cloneMessage(message))
	}
	return cloned
}

func cloneMessage(message agent.Message) agent.Message {
	return agent.Message{
		ID:           message.ID,
		Role:         message.Role,
		Kind:         message.Kind,
		Parts:        cloneParts(message.Parts),
		ToolCalls:    cloneToolCalls(message.ToolCalls),
		ToolResult:   cloneToolResultPayload(message.ToolResult),
		Timestamp:    message.Timestamp,
		API:          message.API,
		Provider:     message.Provider,
		Model:        message.Model,
		ResponseID:   message.ResponseID,
		Metadata:     cloneStringAnyMap(message.Metadata),
		Payload:      cloneStringAnyMap(message.Payload),
		StopReason:   message.StopReason,
		ErrorMessage: message.ErrorMessage,
	}
}

func cloneParts(parts []agent.Part) []agent.Part {
	if len(parts) == 0 {
		return nil
	}

	cloned := make([]agent.Part, len(parts))
	copy(cloned, parts)
	return cloned
}

func cloneToolCalls(calls []agent.ToolCall) []agent.ToolCall {
	if len(calls) == 0 {
		return nil
	}

	cloned := make([]agent.ToolCall, 0, len(calls))
	for _, call := range calls {
		cloned = append(cloned, agent.ToolCall{
			ID:               call.ID,
			OriginalID:       call.OriginalID,
			Name:             call.Name,
			Arguments:        cloneRawMessage(call.Arguments),
			ParsedArgs:       cloneStringAnyMap(call.ParsedArgs),
			ThoughtSignature: call.ThoughtSignature,
		})
	}
	return cloned
}

func cloneToolResultPayload(payload *agent.ToolResultPayload) *agent.ToolResultPayload {
	if payload == nil {
		return nil
	}

	return &agent.ToolResultPayload{
		ToolCallID:         payload.ToolCallID,
		OriginalToolCallID: payload.OriginalToolCallID,
		ToolName:           payload.ToolName,
		Content:            cloneParts(payload.Content),
		Details:            payload.Details,
		IsError:            payload.IsError,
	}
}

func clonePendingToolCalls(calls []agent.PendingToolCall) []agent.PendingToolCall {
	if len(calls) == 0 {
		return nil
	}

	cloned := make([]agent.PendingToolCall, len(calls))
	copy(cloned, calls)
	return cloned
}

func cloneModelRef(ref agent.ModelRef) agent.ModelRef {
	return agent.ModelRef{
		Provider:       ref.Provider,
		Model:          ref.Model,
		ProviderConfig: cloneProviderConfig(ref.ProviderConfig),
		Metadata:       cloneStringAnyMap(ref.Metadata),
	}
}

func cloneProviderConfig(config agent.ProviderConfig) agent.ProviderConfig {
	return agent.ProviderConfig{
		BaseURL: config.BaseURL,
		APIKey:  config.APIKey,
		Headers: cloneStringMap(config.Headers),
		Auth:    cloneProviderAuthConfig(config.Auth),
	}
}

func cloneProviderAuthConfig(config *agent.ProviderAuthConfig) *agent.ProviderAuthConfig {
	if config == nil {
		return nil
	}

	cloned := *config
	cloned.OAuth = cloneOAuthCredentials(config.OAuth)
	return &cloned
}

func cloneOAuthCredentials(credentials *agent.OAuthCredentials) *agent.OAuthCredentials {
	if credentials == nil {
		return nil
	}

	cloned := *credentials
	return &cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func isZeroProviderConfig(config agent.ProviderConfig) bool {
	return config.BaseURL == "" &&
		config.APIKey == "" &&
		len(config.Headers) == 0 &&
		config.Auth == nil
}

func cloneStringAnyMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}

	cloned := make(json.RawMessage, len(value))
	copy(cloned, value)
	return cloned
}

func cloneConfig(config *graph.Config) *graph.Config {
	if config == nil {
		return &graph.Config{}
	}

	cloned := &graph.Config{
		Callbacks:       append([]graph.CallbackHandler(nil), config.Callbacks...),
		Tags:            append([]string(nil), config.Tags...),
		Metadata:        cloneStringAnyMap(config.Metadata),
		Configurable:    cloneStringAnyMap(config.Configurable),
		InterruptBefore: append([]string(nil), config.InterruptBefore...),
		InterruptAfter:  append([]string(nil), config.InterruptAfter...),
		ResumeFrom:      append([]string(nil), config.ResumeFrom...),
		ResumeValue:     config.ResumeValue,
	}
	return cloned
}
