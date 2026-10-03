package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/Icatme/pi-go/pkg/pigo"
)

// MessageRole identifies the semantic role of a message in the conversation.
type MessageRole string

const (
	// RoleSystem carries replayable prompt and tool declaration changes.
	RoleSystem MessageRole = "system"
	// RoleUser is a user-authored message.
	RoleUser MessageRole = "user"
	// RoleAssistant is an assistant-authored message.
	RoleAssistant MessageRole = "assistant"
	// RoleTool is a tool-result message.
	RoleTool MessageRole = "tool"
	// RoleCustom is an app-defined message that usually needs conversion before model use.
	RoleCustom MessageRole = "custom"
)

// PartType identifies the content type within a message part.
type PartType string

const (
	// PartTypeText represents plain text content.
	PartTypeText PartType = "text"
	// PartTypeImage represents image content.
	PartTypeImage PartType = "image"
	// PartTypeThinking represents reasoning/thinking deltas.
	PartTypeThinking PartType = "thinking"
)

// StopReason describes why an assistant message finished.
type StopReason string

const (
	// StopReasonStop indicates a normal completion.
	StopReasonStop StopReason = "stop"
	// StopReasonLength indicates a token or length bound was reached.
	StopReasonLength StopReason = "length"
	// StopReasonToolUse indicates the assistant requested tool execution.
	StopReasonToolUse StopReason = "tool_use"
	// StopReasonError indicates the model run failed.
	StopReasonError StopReason = "error"
	// StopReasonAborted indicates the run was cancelled.
	StopReasonAborted StopReason = "aborted"
)

// EventType identifies a runtime event emitted by the agent.
type EventType string

const (
	// EventAgentStart is emitted when a run starts.
	EventAgentStart EventType = "agent_start"
	// EventAgentEnd is emitted when a run ends.
	EventAgentEnd EventType = "agent_end"
	// EventTurnStart is emitted when a turn starts.
	EventTurnStart EventType = "turn_start"
	// EventTurnEnd is emitted when a turn ends.
	EventTurnEnd EventType = "turn_end"
	// EventMessageStart is emitted when a message begins.
	EventMessageStart EventType = "message_start"
	// EventMessageUpdate is emitted while an assistant message streams.
	EventMessageUpdate EventType = "message_update"
	// EventMessageEnd is emitted when a message completes.
	EventMessageEnd EventType = "message_end"
	// EventToolExecutionStart is emitted before a tool executes.
	EventToolExecutionStart EventType = "tool_execution_start"
	// EventToolExecutionUpdate is emitted for streaming tool updates.
	EventToolExecutionUpdate EventType = "tool_execution_update"
	// EventToolExecutionEnd is emitted when a tool finishes.
	EventToolExecutionEnd EventType = "tool_execution_end"
)

// AssistantEventType identifies low-level stream events from a model adapter.
type AssistantEventType string

const (
	// AssistantEventStart is emitted when the assistant stream starts.
	AssistantEventStart AssistantEventType = "start"
	// AssistantEventTextStart is emitted when a text block starts.
	AssistantEventTextStart AssistantEventType = "text_start"
	// AssistantEventTextDelta is emitted for text increments.
	AssistantEventTextDelta AssistantEventType = "text_delta"
	// AssistantEventTextEnd is emitted when a text block completes.
	AssistantEventTextEnd AssistantEventType = "text_end"
	// AssistantEventThinkingStart is emitted when a thinking block starts.
	AssistantEventThinkingStart AssistantEventType = "thinking_start"
	// AssistantEventThinkingDelta is emitted for reasoning increments.
	AssistantEventThinkingDelta AssistantEventType = "thinking_delta"
	// AssistantEventThinkingEnd is emitted when a thinking block completes.
	AssistantEventThinkingEnd AssistantEventType = "thinking_end"
	// AssistantEventToolCallStart is emitted when a tool call block starts.
	AssistantEventToolCallStart AssistantEventType = "toolcall_start"
	// AssistantEventToolCallDelta is emitted for streamed tool call argument increments.
	AssistantEventToolCallDelta AssistantEventType = "toolcall_delta"
	// AssistantEventToolCallEnd is emitted when a tool call block completes.
	AssistantEventToolCallEnd AssistantEventType = "toolcall_end"
	// AssistantEventDone is emitted when the stream reaches a normal terminal message.
	AssistantEventDone AssistantEventType = "done"
	// AssistantEventError is emitted when the stream reaches an error terminal message.
	AssistantEventError AssistantEventType = "error"
)

// QueueMode controls how queued steering and follow-up messages are delivered.
type QueueMode string

const (
	// QueueModeAll delivers every queued message in one batch.
	QueueModeAll QueueMode = "all"
	// QueueModeOneAtATime delivers one queued message per dequeue.
	QueueModeOneAtATime QueueMode = "one-at-a-time"
)

// ToolExecutionMode controls how tool calls from one assistant message are executed.
type ToolExecutionMode string

const (
	// ToolExecutionSequential executes tool calls one by one.
	ToolExecutionSequential ToolExecutionMode = "sequential"
	// ToolExecutionParallel executes preflight sequentially and tool bodies concurrently.
	ToolExecutionParallel ToolExecutionMode = "parallel"
)

// ThinkingLevel controls how much reasoning a model should perform.
type ThinkingLevel string

const (
	// ThinkingOff disables explicit reasoning.
	ThinkingOff ThinkingLevel = "off"
	// ThinkingMinimal requests minimal reasoning.
	ThinkingMinimal ThinkingLevel = "minimal"
	// ThinkingLow requests low reasoning effort.
	ThinkingLow ThinkingLevel = "low"
	// ThinkingMedium requests medium reasoning effort.
	ThinkingMedium ThinkingLevel = "medium"
	// ThinkingHigh requests high reasoning effort.
	ThinkingHigh ThinkingLevel = "high"
	// ThinkingXHigh requests extra high reasoning effort.
	ThinkingXHigh ThinkingLevel = "xhigh"
	// ThinkingMax requests the highest reasoning effort supported by the model.
	ThinkingMax ThinkingLevel = "max"
)

// Transport identifies the preferred model transport.
type Transport string

const (
	// TransportSSE prefers server-sent events style streaming.
	TransportSSE Transport = "sse"
	// TransportWebSocket prefers a websocket stream when the provider supports it.
	TransportWebSocket Transport = "websocket"
	// TransportWebSocketCached prefers a cached websocket connection when supported.
	TransportWebSocketCached Transport = "websocket-cached"
	// TransportAuto lets the provider choose the best available stream transport.
	TransportAuto Transport = "auto"
)

// ThinkingBudgets stores optional token budgets per thinking level.
type ThinkingBudgets map[ThinkingLevel]int

// Part is a single content fragment inside a message.
// Image parts store provider-ready base64 data plus MIME type.
type Part struct {
	Type      PartType `json:"type"`
	Text      string   `json:"text,omitempty"`
	Data      string   `json:"data,omitempty"`
	MIMEType  string   `json:"mime_type,omitempty"`
	Signature string   `json:"signature,omitempty"`
	Redacted  bool     `json:"redacted,omitempty"`
}

// ToolCall is an assistant-emitted tool invocation request.
type ToolCall struct {
	ID               string          `json:"id"`
	OriginalID       string          `json:"original_id,omitempty"`
	Name             string          `json:"name"`
	Namespace        string          `json:"namespace,omitempty"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	ParsedArgs       map[string]any  `json:"parsed_args,omitempty"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
}

// UnmarshalJSON preserves arbitrary-precision JSON numbers inside ParsedArgs.
func (c *ToolCall) UnmarshalJSON(data []byte) error {
	if err := checkRawArgumentUnicode(data); err != nil {
		return err
	}
	type toolCallJSON ToolCall
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded toolCallJSON
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("agent: multiple JSON values for tool call")
		}
		return err
	}
	*c = ToolCall(decoded)
	return nil
}

// ToolResult is the normalized output of a tool execution.
type ToolResult struct {
	Content []Part `json:"content,omitempty"`
	Details any    `json:"details,omitempty"`
	// StructuredContent is machine-readable JSON, independent of model-facing Content.
	StructuredContent json.RawMessage `json:"structured_content,omitempty"`
	IsError           bool            `json:"is_error,omitempty"`
	Terminate         bool            `json:"terminate,omitempty"`
	// Execution is supplied by trusted adapters. The runtime owns Local and keeps
	// these facts independent from after-hook content overrides.
	Execution *ToolExecutionInfo `json:"execution,omitempty"`
	Failure   *ToolFailure       `json:"failure,omitempty"`
	// ChildCalls is the host-owned bounded execution report. It survives content
	// hook failures and is available only to Go observers, never model JSON.
	ChildCalls *ChildCallReport `json:"-"`
}

// ToolResultPayload stores tool-result specific message data.
type ToolResultPayload struct {
	ToolCallID         string             `json:"tool_call_id"`
	OriginalToolCallID string             `json:"original_tool_call_id,omitempty"`
	ToolName           string             `json:"tool_name"`
	Content            []Part             `json:"content,omitempty"`
	Details            any                `json:"details,omitempty"`
	StructuredContent  json.RawMessage    `json:"structured_content,omitempty"`
	IsError            bool               `json:"is_error"`
	Execution          *ToolExecutionInfo `json:"execution,omitempty"`
	Failure            *ToolFailure       `json:"failure,omitempty"`
	ChildCalls         *ChildCallReport   `json:"-"`
}

// Message is the canonical runtime message envelope.
type Message struct {
	ID            string                `json:"id,omitempty"`
	Role          MessageRole           `json:"role"`
	System        *SystemMessagePayload `json:"system,omitempty"`
	Kind          string                `json:"kind,omitempty"`
	Parts         []Part                `json:"parts,omitempty"`
	ToolCalls     []ToolCall            `json:"tool_calls,omitempty"`
	ToolResult    *ToolResultPayload    `json:"tool_result,omitempty"`
	Timestamp     time.Time             `json:"timestamp"`
	API           string                `json:"api,omitempty"`
	Provider      string                `json:"provider,omitempty"`
	Model         string                `json:"model,omitempty"`
	ResponseID    string                `json:"response_id,omitempty"`
	ThinkingLevel ThinkingLevel         `json:"thinking_level,omitempty"`
	Metadata      map[string]any        `json:"metadata,omitempty"`
	Payload       map[string]any        `json:"payload,omitempty"`
	StopReason    StopReason            `json:"stop_reason,omitempty"`
	ErrorMessage  string                `json:"error_message,omitempty"`
}

// PendingToolCall tracks outstanding tool calls that are suspended before
// execution or currently in flight.
type PendingToolCall struct {
	ToolCallID         string `json:"tool_call_id"`
	OriginalToolCallID string `json:"original_tool_call_id,omitempty"`
	ToolName           string `json:"tool_name"`
}

// PendingToolControl stores the durable binding and turn position for one
// suspended tool-call batch.
type PendingToolControl struct {
	Turn    int    `json:"turn"`
	Binding string `json:"binding"`
}

// ProviderAuthType identifies the provider credential strategy.
type ProviderAuthType string

const (
	// ProviderAuthTypeAPIKey uses a static API key or bearer token.
	ProviderAuthTypeAPIKey ProviderAuthType = "apiKey"
	// ProviderAuthTypeOAuth uses OAuth credentials.
	ProviderAuthTypeOAuth ProviderAuthType = "oauth"
)

// OAuthCredentials stores provider OAuth tokens.
type OAuthCredentials struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresUnix  int64  `json:"expires_unix,omitempty"`
}

// ProviderAuthConfig stores the auth payload for one provider.
type ProviderAuthConfig struct {
	Type   ProviderAuthType  `json:"type,omitempty"`
	APIKey string            `json:"api_key,omitempty"`
	OAuth  *OAuthCredentials `json:"oauth,omitempty"`
}

// ProviderConfig stores typed runtime configuration for the selected provider.
type ProviderConfig struct {
	BaseURL string              `json:"base_url,omitempty"`
	APIKey  string              `json:"api_key,omitempty"`
	Headers map[string]string   `json:"headers,omitempty"`
	Auth    *ProviderAuthConfig `json:"auth,omitempty"`
}

// ModelRef identifies a model without storing provider runtime objects in snapshot state.
type ModelRef struct {
	Provider       string         `json:"provider,omitempty"`
	Model          string         `json:"model,omitempty"`
	ProviderConfig ProviderConfig `json:"provider_config,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// AgentSnapshot is the serializable runtime state for a session.
type AgentSnapshot struct {
	// RequestedThinkingLevel is the saved preference; ThinkingLevel is derived from it and the effective model.
	RequestedThinkingLevel ThinkingLevel       `json:"requested_thinking_level,omitempty"`
	ThinkingLevel          ThinkingLevel       `json:"thinking_level,omitempty"`
	SessionID              string              `json:"session_id,omitempty"`
	SystemPrompt           string              `json:"system_prompt,omitempty"`
	Model                  ModelRef            `json:"model,omitempty"`
	Messages               []Message           `json:"messages,omitempty"`
	PendingToolCalls       []PendingToolCall   `json:"pending_tool_calls,omitempty"`
	PendingToolControl     *PendingToolControl `json:"pending_tool_control,omitempty"`
	Error                  string              `json:"error,omitempty"`
	Metadata               map[string]any      `json:"metadata,omitempty"`
}

// AgentContext is the message/tool context consumed by one agent turn.
type AgentContext struct {
	SystemPrompt string           `json:"system_prompt,omitempty"`
	Messages     []Message        `json:"messages,omitempty"`
	Tools        []ToolDefinition `json:"tools,omitempty"`
}

// AgentState is the in-memory runtime state exposed by Agent.
type AgentState struct {
	RequestedThinkingLevel ThinkingLevel              `json:"requested_thinking_level,omitempty"`
	SystemPrompt           string                     `json:"system_prompt,omitempty"`
	Model                  ModelRef                   `json:"model,omitempty"`
	ThinkingLevel          ThinkingLevel              `json:"thinking_level,omitempty"`
	Tools                  []ToolDefinition           `json:"-"`
	Messages               []Message                  `json:"messages,omitempty"`
	IsStreaming            bool                       `json:"is_streaming"`
	StreamMessage          *Message                   `json:"stream_message,omitempty"`
	PendingToolCalls       map[string]PendingToolCall `json:"pending_tool_calls,omitempty"`
	Error                  string                     `json:"error,omitempty"`
	SessionID              string                     `json:"session_id,omitempty"`
	Transport              Transport                  `json:"transport,omitempty"`
	MaxRetryDelayMs        int                        `json:"max_retry_delay_ms,omitempty"`
	ThinkingBudgets        ThinkingBudgets            `json:"thinking_budgets,omitempty"`
	Metadata               map[string]any             `json:"metadata,omitempty"`
}

// AgentEvent is emitted to subscribers for lifecycle, message, and tool updates.
type AgentEvent struct {
	Type               EventType          `json:"type"`
	Timestamp          time.Time          `json:"timestamp"`
	RunID              string             `json:"run_id,omitempty"`
	ParentRunID        string             `json:"parent_run_id,omitempty"`
	AgentName          string             `json:"agent_name,omitempty"`
	Sequence           uint64             `json:"sequence,omitempty"`
	Message            *Message           `json:"message,omitempty"`
	Messages           []Message          `json:"messages,omitempty"`
	Delta              string             `json:"delta,omitempty"`
	UpdateType         AssistantEventType `json:"update_type,omitempty"`
	ContentIndex       int                `json:"content_index,omitempty"`
	Reason             StopReason         `json:"reason,omitempty"`
	ToolCall           *ToolCall          `json:"tool_call,omitempty"`
	ToolCallID         string             `json:"tool_call_id,omitempty"`
	OriginalToolCallID string             `json:"original_tool_call_id,omitempty"`
	ToolName           string             `json:"tool_name,omitempty"`
	ParentToolCallID   string             `json:"parent_tool_call_id,omitempty"`
	Execution          *ToolExecutionInfo `json:"execution,omitempty"`
	Failure            *ToolFailure       `json:"failure,omitempty"`
	Args               any                `json:"args,omitempty"`
	ToolResult         *ToolResult        `json:"tool_result,omitempty"`
	PartialToolResult  *ToolResult        `json:"partial_tool_result,omitempty"`
	ToolMessages       []Message          `json:"tool_messages,omitempty"`
	IsError            bool               `json:"is_error,omitempty"`
	Err                error              `json:"-"`
	// ToolErr is a per-call failure; Err remains reserved for run/stream errors.
	ToolErr error `json:"-"`
}

// EventSink receives runtime events from an engine.
type EventSink func(AgentEvent)

// ToolUpdateFunc receives partial tool output during execution.
type ToolUpdateFunc func(ToolResult)

// ToolExecutorFunc executes a tool call with validated arguments.
type ToolExecutorFunc func(context.Context, ToolExecutionContext) (ToolResult, error)

// ToolDefinition defines a tool available to the agent runtime.
type ToolDefinition struct {
	ConstrainedSampling *pigo.ToolConstrainedSampling `json:"constrained_sampling,omitempty"`
	Name                string                        `json:"name"`
	Revision            string                        `json:"revision,omitempty"`
	Label               string                        `json:"label,omitempty"`
	Description         string                        `json:"description,omitempty"`
	Parameters          map[string]any                `json:"parameters,omitempty"`
	OutputSchema        map[string]any                `json:"output_schema,omitempty"`
	ExecutionMode       ToolExecutionMode             `json:"execution_mode,omitempty"`
	ParseArguments      func(ToolCall) (any, error)   `json:"-"`
	// ValidateResult checks the effective, after-hook result before publication.
	// It opts into stricter boundaries without changing ordinary OutputSchema semantics.
	ValidateResult func(ToolResult) error `json:"-"`
	Execute        ToolExecutorFunc       `json:"-"`
	// ChildTools grants this container a fixed, invocation-local leaf allowlist.
	// A child may not itself declare ChildTools.
	ChildTools  []ToolDefinition `json:"-"`
	ChildLimits ChildCallLimits  `json:"-"`
	// ResolveChildTools lazily captures a host-authorized leaf set at container
	// execution, after admission and permission checks. The result is frozen for
	// this parent call; children cannot use this resolver or declare children.
	// It is mutually exclusive with ChildTools and cannot be restored by a
	// checkpoint runner without a host resolver. Its absolute Deadline bounds
	// the remaining permission, execution, child and after-hook lifecycle.
	ResolveChildTools func(context.Context, ToolExecutionContext) (ChildToolResolution, error) `json:"-"`
}

// ChildToolResolution supplies a frozen child directory and an optional host
// deadline. A zero Deadline inherits the caller context; a nonzero value can
// only shorten its deadline. The resolver must use this same absolute deadline
// for its own work when the limit also covers directory setup.
type ChildToolResolution struct {
	Tools    []ToolDefinition
	Deadline time.Time
	// FailureTextLimitBytes bounds model-facing diagnostic text if resolution,
	// child validation or the subsequent permission check rejects setup. It is
	// honored even when the resolver returns an error. Zero uses the ordinary
	// rejection presentation; original Go errors and execution facts are kept.
	FailureTextLimitBytes int
}

// BeforeToolCallContext is passed to a before-tool hook.
type BeforeToolCallContext struct {
	AssistantMessage Message      `json:"assistant_message"`
	ToolCall         ToolCall     `json:"tool_call"`
	Args             any          `json:"args,omitempty"`
	Context          AgentContext `json:"context"`
	ParentToolCallID string       `json:"parent_tool_call_id,omitempty"`
}

// BeforeToolCallResult can block tool execution during preflight.
type BeforeToolCallResult struct {
	Block     bool   `json:"block,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Terminate bool   `json:"terminate,omitempty"`
}

// BeforeToolCallHook runs before a tool body executes.
type BeforeToolCallHook func(context.Context, BeforeToolCallContext) (BeforeToolCallResult, error)

// ToolGateAction selects how a runtime gate handles one fully prepared tool
// call. Gates run after BeforeToolCall and schema revalidation, but before any
// tool execution lifecycle event or tool body.
type ToolGateAction string

const (
	// ToolGateActionAllow permits the prepared tool call to execute.
	ToolGateActionAllow ToolGateAction = "allow"
	// ToolGateActionBlock converts the prepared tool call into an error tool result.
	ToolGateActionBlock ToolGateAction = "block"
	// ToolGateActionSuspend pauses the entire tool-call batch without executing it.
	ToolGateActionSuspend ToolGateAction = "suspend"
)

// ToolGateResult is the decision returned by a ToolGateHook.
type ToolGateResult struct {
	Action    ToolGateAction `json:"action"`
	Reason    string         `json:"reason,omitempty"`
	Terminate bool           `json:"terminate,omitempty"`
}

// ToolGateHook evaluates final, schema-valid tool arguments before execution.
type ToolGateHook func(context.Context, BeforeToolCallContext) (ToolGateResult, error)

// SuspendedToolCall describes a prepared call that requested suspension.
type SuspendedToolCall struct {
	ToolCall  ToolCall        `json:"tool_call"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Reason    string          `json:"reason,omitempty"`
}

// AfterToolCallContext is passed to an after-tool hook.
type AfterToolCallContext struct {
	AssistantMessage Message           `json:"assistant_message"`
	ToolCall         ToolCall          `json:"tool_call"`
	Args             any               `json:"args,omitempty"`
	Context          AgentContext      `json:"context"`
	Result           ToolResult        `json:"result"`
	IsError          bool              `json:"is_error"`
	Err              error             `json:"-"`
	Execution        ToolExecutionInfo `json:"execution"`
	Failure          *ToolFailure      `json:"failure,omitempty"`
	ParentToolCallID string            `json:"parent_tool_call_id,omitempty"`
}

// AfterToolCallResult can override a tool result before it is emitted.
type AfterToolCallResult struct {
	Result    *ToolResult `json:"result,omitempty"`
	IsError   *bool       `json:"is_error,omitempty"`
	Terminate *bool       `json:"terminate,omitempty"`
}

// AfterToolCallHook runs after a tool body completes.
type AfterToolCallHook func(context.Context, AfterToolCallContext) (AfterToolCallResult, error)

// AgentTurnContext describes a completed turn after its tool results
// have been appended to the snapshot.
type AgentTurnContext struct {
	Message     Message      `json:"message"`
	ToolResults []Message    `json:"tool_results,omitempty"`
	Context     AgentContext `json:"context"`
	NewMessages []Message    `json:"new_messages,omitempty"`
}

// PrepareNextTurnContext describes the state available before another model
// request is started.
type PrepareNextTurnContext = AgentTurnContext

// AgentLoopTurnUpdate replaces selected state used by the next turn. A nil
// field preserves the current value.
type AgentLoopTurnUpdate struct {
	Context       *AgentContext  `json:"context,omitempty"`
	Model         StreamModel    `json:"-"`
	ModelRef      *ModelRef      `json:"model,omitempty"`
	ThinkingLevel *ThinkingLevel `json:"thinking_level,omitempty"`
}

// AgentTurnAction controls scheduling after a completed turn.
type AgentTurnAction string

const (
	TurnActionEnd      AgentTurnAction = "end"
	TurnActionContinue AgentTurnAction = "continue"
)

// AgentTurnDecision leaves normal scheduling intact when Action is empty.
type AgentTurnDecision struct {
	Action AgentTurnAction `json:"action,omitempty"`
}

// FinishTurnHook runs after assistant and tool results are finalized, before
// EventTurnEnd. End preserves queues and skips preparation. Continue guarantees
// one next request, which natural tool/queue scheduling can satisfy. Error and
// aborted assistant responses always exit regardless of the decision.
type FinishTurnHook func(context.Context, AgentTurnContext) (AgentTurnDecision, error)

// PrepareRequestContext is the state immediately before a provider request.
// Already-selected input has been appended and emitted; this hook never polls queues.
type PrepareRequestContext struct {
	Context       AgentContext  `json:"context"`
	Model         ModelRef      `json:"model"`
	ThinkingLevel ThinkingLevel `json:"thinking_level"`
}

// PrepareRequestHook can replace request context, model, and thinking state for
// this and later requests in the invocation, including the first request.
type PrepareRequestHook func(context.Context, PrepareRequestContext) (*AgentLoopTurnUpdate, error)

// PrepareNextTurnHook can replace context, model, or thinking state for the
// next turn in the same run. It runs only when another turn will start, after the stop decision and before EventTurnStart.
type PrepareNextTurnHook func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error)

// TransformContext allows pruning or enriching messages before model conversion.
type TransformContext func(context.Context, []Message) ([]Message, error)

// ConvertToLLM filters or normalizes messages into model-compatible messages.
type ConvertToLLM func(context.Context, []Message) ([]Message, error)

// ModelRequest is the normalized request given to a model adapter.
type ModelRequest struct {
	Model                 ModelRef                `json:"model"`
	SystemPrompt          string                  `json:"system_prompt,omitempty"`
	Messages              []Message               `json:"messages,omitempty"`
	Tools                 []ToolDefinition        `json:"tools,omitempty"`
	ThinkingLevel         ThinkingLevel           `json:"thinking_level,omitempty"`
	SessionID             string                  `json:"session_id,omitempty"`
	APIKey                string                  `json:"api_key,omitempty"`
	Transport             Transport               `json:"transport,omitempty"`
	MaxRetryDelayMs       int                     `json:"max_retry_delay_ms,omitempty"`
	ThinkingBudgets       ThinkingBudgets         `json:"thinking_budgets,omitempty"`
	OnProviderStreamEvent ProviderStreamEventHook `json:"-"`
}

// ProviderStreamEventHook observes a copied provider JSON event before normalization.
// An error stops the request without retrying it.
type ProviderStreamEventHook func(json.RawMessage, ModelRef) error

// AssistantEvent is a single low-level event from a model stream.
type AssistantEvent struct {
	Type         AssistantEventType `json:"type"`
	Message      Message            `json:"message"`
	Delta        string             `json:"delta,omitempty"`
	ToolCall     *ToolCall          `json:"tool_call,omitempty"`
	ContentIndex int                `json:"content_index,omitempty"`
	Reason       StopReason         `json:"reason,omitempty"`
	Err          error              `json:"-"`
}

// AssistantStream streams assistant deltas and returns a final assistant message.
type AssistantStream interface {
	Events() <-chan AssistantEvent
	Wait() (Message, error)
	Close() error
}

// StreamModel is the model contract consumed by the runtime engine.
type StreamModel interface {
	Stream(context.Context, ModelRequest) (AssistantStream, error)
}

// StreamFunc adapts a function to the StreamModel interface.
type StreamFunc func(context.Context, ModelRequest) (AssistantStream, error)

// Stream executes the function as a StreamModel implementation.
func (f StreamFunc) Stream(ctx context.Context, request ModelRequest) (AssistantStream, error) {
	return f(ctx, request)
}

// ModelResolver resolves a runtime model for the current snapshot.
type ModelResolver func(context.Context, ModelRef, AgentSnapshot) (StreamModel, error)

// ToolResolver resolves tools for the current snapshot.
type ToolResolver func(context.Context, AgentSnapshot) ([]ToolDefinition, error)

// DefinitionResolver produces an AgentDefinition from snapshot state.
type DefinitionResolver func(context.Context, AgentSnapshot) (AgentDefinition, error)

// NewTextMessage creates a single-text message with the provided role.
func NewTextMessage(role MessageRole, text string) Message {
	return Message{
		Role:      role,
		Parts:     []Part{{Type: PartTypeText, Text: text}},
		Timestamp: time.Now().UTC(),
	}
}

// NewToolResultMessage converts a tool result into a tool-role message.
func NewToolResultMessage(call ToolCall, result ToolResult, isError bool) Message {
	return Message{
		Role:  RoleTool,
		Parts: cloneParts(result.Content),
		ToolResult: &ToolResultPayload{
			ToolCallID:         call.ID,
			OriginalToolCallID: call.OriginalID,
			ToolName:           call.Name,
			Content:            cloneParts(result.Content),
			Details:            cloneAny(result.Details),
			StructuredContent:  append(json.RawMessage(nil), result.StructuredContent...),
			IsError:            isError || result.IsError,
			Execution:          cloneToolExecutionInfo(result.Execution),
			Failure:            cloneToolFailure(result.Failure),
			ChildCalls:         cloneChildCallReport(result.ChildCalls),
		},
		Timestamp: time.Now().UTC(),
	}
}
