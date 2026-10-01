package pigo

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// TranscriptContext carries the prompt and tool declarations in system messages.
// Context remains the public authoring form accepted by Stream and StreamSimple.
type TranscriptContext struct {
	Messages    []Message
	HostedTools []HostedTool
}

func CreateInitialSystemMessage(systemPrompt string, tools []Tool) *SystemMessage {
	if systemPrompt == "" && len(tools) == 0 {
		return nil
	}
	return &SystemMessage{Content: systemPrompt, ToolsAdded: cloneTools(tools), Timestamp: time.Unix(0, 0).UTC()}
}

func NormalizeContext(ctx Context) TranscriptContext {
	messages := cloneMessages(ctx.Messages)
	if initial := CreateInitialSystemMessage(ctx.SystemPrompt, ctx.Tools); initial != nil {
		messages = append([]Message{*initial}, messages...)
	}
	return TranscriptContext{Messages: messages, HostedTools: append([]HostedTool(nil), ctx.HostedTools...)}
}

func asSystemMessage(message Message) (SystemMessage, bool) {
	switch typed := message.(type) {
	case SystemMessage:
		return typed, true
	case *SystemMessage:
		if typed != nil {
			return *typed, true
		}
	}
	return SystemMessage{}, false
}

func GetInitialSystemMessage(messages []Message) *SystemMessage {
	if len(messages) == 0 {
		return nil
	}
	message, ok := asSystemMessage(messages[0])
	if !ok {
		return nil
	}
	cloned := message.clone().(SystemMessage)
	return &cloned
}

func WithoutInitialSystemMessage(messages []Message) []Message {
	if len(messages) > 0 {
		if _, ok := asSystemMessage(messages[0]); ok {
			return cloneMessages(messages[1:])
		}
	}
	return cloneMessages(messages)
}

// GetCurrentTools replays removal before addition. Updating an existing name
// preserves its position; removing and adding it later appends it to the end.
func GetCurrentTools(messages []Message) []Tool {
	tools := map[string]Tool{}
	var order []string
	for _, entry := range messages {
		message, ok := asSystemMessage(entry)
		if !ok {
			continue
		}
		for _, tool := range message.ToolsRemoved {
			delete(tools, tool.Name)
			for index, name := range order {
				if name == tool.Name {
					order = append(order[:index], order[index+1:]...)
					break
				}
			}
		}
		for _, tool := range message.ToolsAdded {
			if _, exists := tools[tool.Name]; !exists {
				order = append(order, tool.Name)
			}
			tools[tool.Name] = tool
		}
	}
	result := make([]Tool, 0, len(order))
	for _, name := range order {
		result = append(result, tools[name])
	}
	return cloneTools(result)
}

func GetCurrentSystemMessage(messages []Message) *SystemMessage {
	var content []string
	sections := map[string]*string{}
	var result *SystemMessage
	for _, entry := range messages {
		message, ok := asSystemMessage(entry)
		if !ok {
			continue
		}
		if result == nil {
			result = &SystemMessage{Timestamp: message.Timestamp}
		}
		if message.Content != "" {
			content = append(content, message.Content)
		}
		for name, value := range message.Sections {
			if value == nil {
				delete(sections, name)
			} else {
				text := *value
				sections[name] = &text
			}
		}
	}
	if result == nil {
		return nil
	}
	result.Content = strings.Join(content, "\n\n")
	if len(sections) > 0 {
		result.Sections = sections
	}
	result.ToolsAdded = GetCurrentTools(messages)
	return result
}

func systemSectionNames(sections map[string]*string) []string {
	names := make([]string, 0, len(sections))
	for name := range sections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func GetSystemMessageText(message SystemMessage) string {
	parts := []string{message.Content}
	for _, name := range systemSectionNames(message.Sections) {
		if value := message.Sections[name]; value != nil {
			parts = append(parts, *value)
		}
	}
	return joinSystemText(parts)
}

func RenderSystemMessageUpdate(message SystemMessage) string {
	parts := []string{message.Content}
	for _, name := range systemSectionNames(message.Sections) {
		value := message.Sections[name]
		if value == nil {
			parts = append(parts, "Removed system prompt section \""+name+"\".")
		} else {
			parts = append(parts, "Updated system prompt section \""+name+"\":\n\n"+*value)
		}
	}
	return joinSystemText(parts)
}

func joinSystemText(parts []string) string {
	nonempty := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			nonempty = append(nonempty, part)
		}
	}
	return strings.Join(nonempty, "\n\n")
}

func GetCurrentSystemPrompt(messages []Message) string {
	if message := GetCurrentSystemMessage(messages); message != nil {
		return GetSystemMessageText(*message)
	}
	return ""
}

func CollapseSystemMessages(ctx TranscriptContext) TranscriptContext {
	result := TranscriptContext{HostedTools: append([]HostedTool(nil), ctx.HostedTools...)}
	if initial := GetCurrentSystemMessage(ctx.Messages); initial != nil {
		result.Messages = append(result.Messages, *initial)
	}
	for _, message := range ctx.Messages {
		if _, system := asSystemMessage(message); !system && message != nil {
			result.Messages = append(result.Messages, message.clone())
		}
	}
	return result
}

func ResolveTranscript(ctx TranscriptContext, supportsMidConvoSystemMessages bool) TranscriptContext {
	if !supportsMidConvoSystemMessages {
		return CollapseSystemMessages(ctx)
	}
	return TranscriptContext{Messages: cloneMessages(ctx.Messages), HostedTools: append([]HostedTool(nil), ctx.HostedTools...)}
}

func ToToolDeclaration(tool Tool) Tool {
	declaration := cloneTools([]Tool{tool})[0]
	declaration.Validator = nil
	return declaration
}

func DeclarationsEqual(left, right Tool) bool {
	leftJSON, leftErr := json.Marshal(ToToolDeclaration(left))
	rightJSON, rightErr := json.Marshal(ToToolDeclaration(right))
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

type ToolStateChanges struct {
	ToolsAdded   []Tool
	ToolsRemoved []ToolReference
}

func GetToolStateChanges(previous, current []Tool) ToolStateChanges {
	before, after := map[string]Tool{}, map[string]Tool{}
	for _, tool := range previous {
		before[tool.Name] = tool
	}
	for _, tool := range current {
		after[tool.Name] = tool
	}
	changes := ToolStateChanges{}
	for _, tool := range current {
		old, exists := before[tool.Name]
		if !exists || !DeclarationsEqual(old, tool) {
			changes.ToolsAdded = append(changes.ToolsAdded, ToToolDeclaration(tool))
		}
	}
	for _, tool := range previous {
		next, exists := after[tool.Name]
		if !exists || !DeclarationsEqual(tool, next) {
			changes.ToolsRemoved = append(changes.ToolsRemoved, ToolReference{Name: tool.Name})
		}
	}
	return changes
}

func supportsTranscriptSystemMessages(model Model) bool {
	switch compat := model.Compat.(type) {
	case *OpenAIResponsesCompat:
		return (model.API == "openai-responses" || model.API == "openai-codex-responses") && compat != nil && compat.SupportsMidConvoSystemMessages != nil && *compat.SupportsMidConvoSystemMessages
	case *OpenAICompletionsCompat:
		return model.API == "openai-completions" && compat != nil && compat.SupportsMidConvoSystemMessages != nil && *compat.SupportsMidConvoSystemMessages
	}
	return false
}

func contextFromTranscript(ctx TranscriptContext) Context {
	return Context{Messages: ctx.Messages, HostedTools: ctx.HostedTools}
}

func currentContextTools(ctx Context) []Tool {
	return GetCurrentTools(NormalizeContext(ctx).Messages)
}

// resolveProviderContext folds system changes for protocols without native
// support. Native adapters keep the transcript and derive request tools from it.
func resolveProviderContext(model Model, ctx Context) Context {
	transcript := ResolveTranscript(NormalizeContext(ctx), supportsTranscriptSystemMessages(model))
	if supportsTranscriptSystemMessages(model) {
		return contextFromTranscript(transcript)
	}
	return Context{
		SystemPrompt: GetCurrentSystemPrompt(transcript.Messages),
		Tools:        GetCurrentTools(transcript.Messages),
		Messages:     WithoutInitialSystemMessage(transcript.Messages),
		HostedTools:  transcript.HostedTools,
	}
}

func (ctx TranscriptContext) MarshalJSON() ([]byte, error) {
	return json.Marshal(contextFromTranscript(ctx))
}

func (ctx *TranscriptContext) UnmarshalJSON(data []byte) error {
	var authoring Context
	if err := json.Unmarshal(data, &authoring); err != nil {
		return err
	}
	*ctx = NormalizeContext(authoring)
	return nil
}
