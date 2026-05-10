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
type Transport string

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

	TransportSSE             Transport = "sse"
	TransportWebSocket       Transport = "websocket"
	TransportWebSocketCached Transport = "websocket-cached"
	TransportAuto            Transport = "auto"

	HostedToolTypeWebSearch  HostedToolType = "web_search"
	HostedToolTypeFetch      HostedToolType = "fetch"
	HostedToolTypeCodeRunner HostedToolType = "code_runner"
	HostedToolTypeExcel      HostedToolType = "excel"
)

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

type ProviderResponse struct {
	Status  int
	Headers map[string]string
}
