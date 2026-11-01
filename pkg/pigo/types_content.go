package pigo

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
	ID   string
	Name string
	// Namespace is provider replay metadata, not a local tool routing key.
	Namespace string
	Arguments map[string]any
	// RawArguments is the original JSON object text when the provider exposes
	// it. It is available on completed calls from Responses, OpenAI chat
	// completions, DeepSeek chat, Anthropic Messages and Command Code. Unlike Arguments, it
	// preserves exact number literals and duplicate keys. It is ephemeral
	// provider evidence and is not part of serialized conversation history.
	RawArguments     string `json:"-"`
	ThoughtSignature string
}

func (ToolCall) isContentBlock() {}
