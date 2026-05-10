package pigo

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
