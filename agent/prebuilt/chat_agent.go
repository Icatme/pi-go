package prebuilt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"reflect"
	"strings"
	"time"

	core "github.com/Icatme/pi-go/agent"
)

// ChatAgentOption customizes a native ChatAgent.
type ChatAgentOption func(*core.AgentDefinition)

// WithSystemMessage sets the default chat system prompt.
func WithSystemMessage(message string) ChatAgentOption {
	return func(definition *core.AgentDefinition) {
		definition.SystemPrompt = message
	}
}

// WithStateModifier rewrites message history before model invocation.
func WithStateModifier(modifier func([]core.Message) []core.Message) ChatAgentOption {
	return func(definition *core.AgentDefinition) {
		if modifier == nil {
			return
		}
		definition.TransformContext = func(_ context.Context, messages []core.Message) ([]core.Message, error) {
			return modifier(messages), nil
		}
	}
}

// WithMaxIterations limits assistant turns for one chat request.
func WithMaxIterations(maxIterations int) ChatAgentOption {
	return func(definition *core.AgentDefinition) {
		definition.MaxTurns = maxIterations
	}
}

// ChatAgent is a session-oriented single-agent wrapper.
// Convenience APIs stay here, but the underlying behavior should reuse agent.Agent directly.
type ChatAgent struct {
	agent        *core.Agent
	baseTools    []core.ToolDefinition
	dynamicTools []core.ToolDefinition
}

// NewChatAgent creates a session-oriented native ChatAgent.
func NewChatAgent(definition core.AgentDefinition, opts ...ChatAgentOption) (*ChatAgent, error) {
	for _, opt := range opts {
		opt(&definition)
	}

	if strings.TrimSpace(definition.SessionID) == "" {
		definition.SessionID = newSessionID()
	}

	baseTools := cloneToolDefinitions(definition.Tools)
	agent, err := NewPiAgent(definition)
	if err != nil {
		return nil, err
	}

	return &ChatAgent{
		agent:     agent,
		baseTools: baseTools,
	}, nil
}

// ThreadID returns the stable session identifier for the chat.
func (c *ChatAgent) ThreadID() string {
	return c.agent.Snapshot().SessionID
}

// Chat appends a user message, runs one agent interaction, and returns the final assistant text.
func (c *ChatAgent) Chat(ctx context.Context, message string) (string, error) {
	if err := c.agent.PromptText(ctx, message); err != nil {
		return "", err
	}
	return latestAssistantText(c.agent.State().Messages), nil
}

// PrintStream streams the response chunks to a writer-like callback.
func (c *ChatAgent) PrintStream(ctx context.Context, message string, write func(string) error) error {
	chunks, err := c.AsyncChat(ctx, message)
	if err != nil {
		return err
	}
	for chunk := range chunks {
		if err := write(chunk); err != nil {
			return err
		}
	}
	return nil
}

// AsyncChat streams assistant text deltas for one user message.
func (c *ChatAgent) AsyncChat(ctx context.Context, message string) (<-chan string, error) {
	output := make(chan string, 64)

	go func() {
		defer close(output)

		var sawDelta bool
		unsubscribe := c.agent.Subscribe(func(event core.AgentEvent) {
			switch event.Type {
			case core.EventMessageUpdate:
				if event.Message == nil || event.Message.Role != core.RoleAssistant {
					return
				}
				if event.AssistantEvent != nil && event.AssistantEvent.Type != core.AssistantEventTextDelta {
					return
				}
				if event.Delta == "" {
					return
				}
				sawDelta = true
				select {
				case <-ctx.Done():
				case output <- event.Delta:
				}
			case core.EventMessageEnd:
				if sawDelta || event.Message == nil || event.Message.Role != core.RoleAssistant {
					return
				}
				text := messageText(*event.Message)
				if text == "" {
					return
				}
				select {
				case <-ctx.Done():
				case output <- text:
				}
			}
		})
		defer unsubscribe()

		_ = c.agent.PromptText(ctx, message)
	}()

	return output, nil
}

// AsyncChatWithChunks streams the final response in word-sized chunks.
func (c *ChatAgent) AsyncChatWithChunks(ctx context.Context, message string) (<-chan string, error) {
	output := make(chan string, 64)

	go func() {
		defer close(output)

		response, err := c.Chat(ctx, message)
		if err != nil {
			return
		}

		words := splitIntoWords(response)
		for i, word := range words {
			select {
			case <-ctx.Done():
				return
			case output <- word:
			}
			if i < len(words)-1 {
				select {
				case <-ctx.Done():
					return
				case output <- " ":
				}
			}
		}
	}()

	return output, nil
}

// SetTools replaces dynamic tools while preserving base tools from construction time.
func (c *ChatAgent) SetTools(newTools []core.ToolDefinition) {
	c.dynamicTools = cloneToolDefinitions(newTools)
	c.refreshTools()
}

// AddTool adds or replaces one dynamic tool by name.
func (c *ChatAgent) AddTool(tool core.ToolDefinition) {
	for i := range c.dynamicTools {
		if c.dynamicTools[i].Name == tool.Name {
			c.dynamicTools[i] = cloneToolDefinition(tool)
			c.refreshTools()
			return
		}
	}
	c.dynamicTools = append(c.dynamicTools, cloneToolDefinition(tool))
	c.refreshTools()
}

// RemoveTool removes one dynamic tool by name.
func (c *ChatAgent) RemoveTool(toolName string) bool {
	for i := range c.dynamicTools {
		if c.dynamicTools[i].Name != toolName {
			continue
		}
		c.dynamicTools = append(c.dynamicTools[:i], c.dynamicTools[i+1:]...)
		c.refreshTools()
		return true
	}
	return false
}

// GetTools returns a copy of the dynamic tool list.
func (c *ChatAgent) GetTools() []core.ToolDefinition {
	return cloneToolDefinitions(c.dynamicTools)
}

// ClearTools removes all dynamic tools.
func (c *ChatAgent) ClearTools() {
	c.dynamicTools = nil
	c.refreshTools()
}

func (c *ChatAgent) refreshTools() {
	tools := append(cloneToolDefinitions(c.baseTools), cloneToolDefinitions(c.dynamicTools)...)
	c.agent.SetTools(tools)
}

func latestAssistantText(messages []core.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == core.RoleAssistant {
			return messageText(messages[i])
		}
	}
	return ""
}

func messageText(message core.Message) string {
	var builder strings.Builder
	for _, part := range message.Parts {
		if part.Type == core.PartTypeText {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func splitIntoWords(text string) []string {
	return strings.Fields(text)
}

func newSessionID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "chat-" + hex.EncodeToString(bytes[:])
	}
	return "chat-" + time.Now().UTC().Format("20060102150405.000000000")
}

func cloneToolDefinitions(tools []core.ToolDefinition) []core.ToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	cloned := make([]core.ToolDefinition, len(tools))
	for i := range tools {
		cloned[i] = cloneToolDefinition(tools[i])
	}
	return cloned
}

func cloneToolDefinition(tool core.ToolDefinition) core.ToolDefinition {
	cloned := tool
	if tool.Parameters != nil {
		cloned.Parameters = cloneStringAnyMap(tool.Parameters)
	}
	return cloned
}

func cloneStringAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = cloneAny(value)
	}
	return cloned
}

func cloneAny(value any) any {
	cloned := cloneReflectValue(reflect.ValueOf(value), make(map[cloneVisit]reflect.Value))
	if !cloned.IsValid() {
		return nil
	}
	return cloned.Interface()
}

type cloneVisit struct {
	typeOf   reflect.Type
	pointer  uintptr
	length   int
	capacity int
}

func cloneReflectValue(value reflect.Value, visited map[cloneVisit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return reflect.Value{}
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneReflectValue(value.Elem(), visited)
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{typeOf: value.Type(), pointer: value.Pointer()}
		if cloned, ok := visited[visit]; ok {
			return cloned
		}
		result := reflect.New(value.Type().Elem())
		visited[visit] = result
		result.Elem().Set(cloneReflectValue(value.Elem(), visited))
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{typeOf: value.Type(), pointer: value.Pointer()}
		if cloned, ok := visited[visit]; ok {
			return cloned
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		visited[visit] = result
		iterator := value.MapRange()
		for iterator.Next() {
			result.SetMapIndex(iterator.Key(), cloneReflectValue(iterator.Value(), visited))
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{typeOf: value.Type(), pointer: value.Pointer(), length: value.Len(), capacity: value.Cap()}
		if cloned, ok := visited[visit]; ok {
			return cloned
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Cap())
		visited[visit] = result
		for i := 0; i < value.Len(); i++ {
			result.Index(i).Set(cloneReflectValue(value.Index(i), visited))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			result.Index(i).Set(cloneReflectValue(value.Index(i), visited))
		}
		return result
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for i := 0; i < value.NumField(); i++ {
			if result.Field(i).CanSet() && value.Type().Field(i).IsExported() {
				result.Field(i).Set(cloneReflectValue(value.Field(i), visited))
			}
		}
		return result
	default:
		return value
	}
}
