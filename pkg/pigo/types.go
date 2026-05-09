package pigo

import "time"

type API string
type Provider string
type StopReason string
type InputType string
type CacheRetention string
type ThinkingLevel string
type ModelThinkingLevel string
type HostedToolType string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"

	InputText  InputType = "text"
	InputImage InputType = "image"

	CacheRetentionNone  CacheRetention = "none"
	CacheRetentionShort CacheRetention = "short"
	CacheRetentionLong  CacheRetention = "long"

	ThinkingLevelMinimal ThinkingLevel = "minimal"
	ThinkingLevelLow     ThinkingLevel = "low"
	ThinkingLevelMedium  ThinkingLevel = "medium"
	ThinkingLevelHigh    ThinkingLevel = "high"
	ThinkingLevelXHigh   ThinkingLevel = "xhigh"

	ModelThinkingLevelOff     ModelThinkingLevel = "off"
	ModelThinkingLevelMinimal ModelThinkingLevel = "minimal"
	ModelThinkingLevelLow     ModelThinkingLevel = "low"
	ModelThinkingLevelMedium  ModelThinkingLevel = "medium"
	ModelThinkingLevelHigh    ModelThinkingLevel = "high"
	ModelThinkingLevelXHigh   ModelThinkingLevel = "xhigh"

	TransportSSE          Transport = "sse"
	TransportWebSocket    Transport = "websocket"
	TransportWebSocketCached Transport = "websocket-cached"
	TransportAuto         Transport = "auto"

	HostedToolTypeWebSearch  HostedToolType = "web_search"
	HostedToolTypeFetch      HostedToolType = "fetch"
	HostedToolTypeCodeRunner HostedToolType = "code_runner"
	HostedToolTypeExcel      HostedToolType = "excel"
)

type Transport string

type ThinkingBudgets struct {
	Minimal int
	Low     int
	Medium  int
	High    int
}

type ThinkingLevelMap map[ModelThinkingLevel]string

type HostedToolCapabilities struct {
	WebSearch  bool
	Fetch      bool
	CodeRunner bool
	Excel      bool
}

func (c HostedToolCapabilities) Supports(toolType HostedToolType) bool {
	switch toolType {
	case HostedToolTypeWebSearch:
		return c.WebSearch
	case HostedToolTypeFetch:
		return c.Fetch
	case HostedToolTypeCodeRunner:
		return c.CodeRunner
	case HostedToolTypeExcel:
		return c.Excel
	default:
		return false
	}
}

type UsageCost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Total      float64
}

type Usage struct {
	Input       int
	Output      int
	CacheRead   int
	CacheWrite  int
	TotalTokens int
	Cost        UsageCost
}

type ContentBlock interface {
	isContentBlock()
}

type TextContent struct {
	Text          string
	TextSignature string
}

func (TextContent) isContentBlock() {}

type ThinkingContent struct {
	Thinking          string
	ThinkingSignature string
	Redacted          bool
}

func (ThinkingContent) isContentBlock() {}

type ImageContent struct {
	Data     string
	MIMEType string
}

func (ImageContent) isContentBlock() {}

type ToolCall struct {
	ID               string
	Name             string
	Arguments        map[string]any
	ThoughtSignature string
}

func (ToolCall) isContentBlock() {}

type HostedTool struct {
	Type HostedToolType
	Name string
}

type HostedToolExecution struct {
	ID               string
	Type             HostedToolType
	Name             string
	ProviderToolName string
	Arguments        map[string]any
	Result           map[string]any
}

type Message interface {
	messageRole() string
	clone() Message
}

type UserMessage struct {
	Content   any
	Timestamp time.Time
}

func (UserMessage) messageRole() string { return "user" }

func (m UserMessage) clone() Message {
	return UserMessage{
		Content:   cloneAny(m.Content),
		Timestamp: m.Timestamp,
	}
}

type TextSignatureV1 struct {
	V     int    `json:"v"`
	ID    string `json:"id"`
	Phase string `json:"phase,omitempty"`
}

type AssistantMessageDiagnostic struct {
	Type      string
	Timestamp time.Time
	Error     *DiagnosticErrorInfo
	Details   map[string]any
}

type DiagnosticErrorInfo struct {
	Name    string
	Message string
	Stack   string
	Code    any
}

type AssistantMessage struct {
	Content              []ContentBlock
	HostedToolExecutions []HostedToolExecution
	API                  API
	Provider             Provider
	Model                string
	ResponseModel        string
	ResponseID           string
	Usage                Usage
	StopReason           StopReason
	ErrorMessage         string
	Diagnostics          []AssistantMessageDiagnostic
	Timestamp            time.Time
}

func (AssistantMessage) messageRole() string { return "assistant" }

func (m AssistantMessage) clone() Message {
	return AssistantMessage{
		Content:              cloneBlocks(m.Content),
		HostedToolExecutions: cloneHostedToolExecutions(m.HostedToolExecutions),
		API:                  m.API,
		Provider:             m.Provider,
		Model:                m.Model,
		ResponseModel:        m.ResponseModel,
		ResponseID:           m.ResponseID,
		Usage:                m.Usage,
		StopReason:           m.StopReason,
		ErrorMessage:         m.ErrorMessage,
		Diagnostics:          cloneDiagnostics(m.Diagnostics),
		Timestamp:            m.Timestamp,
	}
}

type ToolResultMessage struct {
	ToolCallID string
	ToolName   string
	Content    []ContentBlock
	Details    any
	IsError    bool
	Timestamp  time.Time
}

func (ToolResultMessage) messageRole() string { return "toolResult" }

func (m ToolResultMessage) clone() Message {
	return ToolResultMessage{
		ToolCallID: m.ToolCallID,
		ToolName:   m.ToolName,
		Content:    cloneBlocks(m.Content),
		Details:    cloneAny(m.Details),
		IsError:    m.IsError,
		Timestamp:  m.Timestamp,
	}
}

type ProviderResponse struct {
	Status  int
	Headers map[string]string
}

type Model struct {
	ID               string
	Name             string
	API              API
	Provider         Provider
	BaseURL          string
	Reasoning        bool
	ThinkingLevelMap ThinkingLevelMap
	Input            []InputType
	HostedTools      HostedToolCapabilities
	Cost             UsageCost
	ContextWindow    int
	MaxTokens        int
	Headers          map[string]string
}

type Tool struct {
	Name        string
	Description string
	Parameters  any
	Validator   ToolArgumentsValidator
}

type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
	HostedTools  []HostedTool
}

type AssistantMessageEventType string

const (
	AssistantMessageEventStart         AssistantMessageEventType = "start"
	AssistantMessageEventTextStart     AssistantMessageEventType = "text_start"
	AssistantMessageEventTextDelta     AssistantMessageEventType = "text_delta"
	AssistantMessageEventTextEnd       AssistantMessageEventType = "text_end"
	AssistantMessageEventThinkingStart AssistantMessageEventType = "thinking_start"
	AssistantMessageEventThinkingDelta AssistantMessageEventType = "thinking_delta"
	AssistantMessageEventThinkingEnd   AssistantMessageEventType = "thinking_end"
	AssistantMessageEventToolCallStart AssistantMessageEventType = "toolcall_start"
	AssistantMessageEventToolCallDelta AssistantMessageEventType = "toolcall_delta"
	AssistantMessageEventToolCallEnd   AssistantMessageEventType = "toolcall_end"
	AssistantMessageEventDone          AssistantMessageEventType = "done"
	AssistantMessageEventError         AssistantMessageEventType = "error"
)

type AssistantMessageEvent struct {
	Type          AssistantMessageEventType
	ContentIndex  int
	Delta         string
	Content       string
	ToolCall      ToolCall
	Partial       AssistantMessage
	Reason        StopReason
	Message       AssistantMessage
	Error         AssistantMessage
	DroppedEvents int
}

type ToolArgumentsValidator interface {
	Validate(map[string]any) (map[string]any, error)
}

type ToolArgumentsValidatorFunc func(map[string]any) (map[string]any, error)

func (f ToolArgumentsValidatorFunc) Validate(args map[string]any) (map[string]any, error) {
	return f(args)
}

func cloneBlocks(blocks []ContentBlock) []ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		switch value := block.(type) {
		case TextContent:
			out = append(out, value)
		case ThinkingContent:
			out = append(out, value)
		case ImageContent:
			out = append(out, value)
		case ToolCall:
			out = append(out, ToolCall{
				ID:               value.ID,
				Name:             value.Name,
				Arguments:        cloneMap(value.Arguments),
				ThoughtSignature: value.ThoughtSignature,
			})
		}
	}
	return out
}

func cloneDiagnostics(diagnostics []AssistantMessageDiagnostic) []AssistantMessageDiagnostic {
	if len(diagnostics) == 0 {
		return nil
	}
	out := make([]AssistantMessageDiagnostic, 0, len(diagnostics))
	for _, d := range diagnostics {
		cloned := AssistantMessageDiagnostic{
			Type:      d.Type,
			Timestamp: d.Timestamp,
			Details:   cloneMap(d.Details),
		}
		if d.Error != nil {
			cloned.Error = &DiagnosticErrorInfo{
				Name:    d.Error.Name,
				Message: d.Error.Message,
				Stack:   d.Error.Stack,
				Code:    d.Error.Code,
			}
		}
		out = append(out, cloned)
	}
	return out
}

func cloneHostedToolExecutions(items []HostedToolExecution) []HostedToolExecution {
	if len(items) == 0 {
		return nil
	}
	out := make([]HostedToolExecution, 0, len(items))
	for _, item := range items {
		out = append(out, HostedToolExecution{
			ID:               item.ID,
			Type:             item.Type,
			Name:             item.Name,
			ProviderToolName: item.ProviderToolName,
			Arguments:        cloneMap(item.Arguments),
			Result:           cloneMap(item.Result),
		})
	}
	return out
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		out = append(out, message.clone())
	}
	return out
}

func cloneMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = cloneAny(value)
	}
	return out
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneAny(item)
		}
		return out
	default:
		return typed
	}
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}
