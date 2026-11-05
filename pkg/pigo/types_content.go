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
	// it. Unlike Arguments, it preserves exact number literals and duplicate
	// keys. Providers including Responses, OpenAI chat completions, DeepSeek,
	// Anthropic, Command Code, Google, and Mistral expose it on completed calls.
	// This evidence is ephemeral and is excluded from serialized history.
	RawArguments     string `json:"-"`
	ThoughtSignature string
}

func (ToolCall) isContentBlock() {}
