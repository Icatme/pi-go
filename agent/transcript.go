package agent

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Icatme/pi-go/pkg/pigo"
)

// ToolDeclaration persists only the interface visible to the model.
type ToolDeclaration struct {
	ConstrainedSampling *pigo.ToolConstrainedSampling `json:"constrained_sampling,omitempty"`
	Name                string                        `json:"name"`
	Description         string                        `json:"description,omitempty"`
	Parameters          map[string]any                `json:"parameters,omitempty"`
	OutputSchema        map[string]any                `json:"output_schema,omitempty"`
}

type ToolReference struct {
	Name string `json:"name"`
}

// SystemMessagePayload appends content, patches named sections (nil removes a
// section), and changes tool declarations. Executable functions never persist.
type SystemMessagePayload struct {
	Content      string             `json:"content,omitempty"`
	Sections     map[string]*string `json:"sections,omitempty"`
	ToolsAdded   []ToolDeclaration  `json:"tools_added,omitempty"`
	ToolsRemoved []ToolReference    `json:"tools_removed,omitempty"`
}

func NewSystemMessage(payload SystemMessagePayload) Message {
	return Message{Role: RoleSystem, System: cloneSystemPayload(&payload), Timestamp: time.Now().UTC()}
}

func cloneSystemPayload(payload *SystemMessagePayload) *SystemMessagePayload {
	if payload == nil {
		return nil
	}
	cloned := *payload
	if payload.Sections != nil {
		cloned.Sections = make(map[string]*string, len(payload.Sections))
		for name, value := range payload.Sections {
			if value == nil {
				cloned.Sections[name] = nil
			} else {
				copyValue := *value
				cloned.Sections[name] = &copyValue
			}
		}
	}
	cloned.ToolsAdded = make([]ToolDeclaration, len(payload.ToolsAdded))
	for i, tool := range payload.ToolsAdded {
		cloned.ToolsAdded[i] = tool
		cloned.ToolsAdded[i].Parameters = cloneStringAnyMap(tool.Parameters)
		cloned.ToolsAdded[i].OutputSchema = cloneStringAnyMap(tool.OutputSchema)
		cloned.ToolsAdded[i].ConstrainedSampling = tool.ConstrainedSampling.Clone()
	}
	cloned.ToolsRemoved = append([]ToolReference(nil), payload.ToolsRemoved...)
	return &cloned
}

// GetCurrentTools replays removals before additions, preserving declaration order.
func GetCurrentTools(messages []Message) []ToolDeclaration {
	var tools []ToolDeclaration
	for _, message := range messages {
		if message.Role != RoleSystem || message.System == nil {
			continue
		}
		for _, removed := range message.System.ToolsRemoved {
			for i, tool := range tools {
				if tool.Name == removed.Name {
					tools = append(tools[:i], tools[i+1:]...)
					break
				}
			}
		}
		for _, added := range message.System.ToolsAdded {
			found := false
			for i, tool := range tools {
				if tool.Name == added.Name {
					tools[i] = added
					found = true
					break
				}
			}
			if !found {
				tools = append(tools, added)
			}
		}
	}
	return cloneSystemPayload(&SystemMessagePayload{ToolsAdded: tools}).ToolsAdded
}

// GetCurrentSystemMessage folds the complete transcript into one leading state.
func GetCurrentSystemMessage(messages []Message) *Message {
	var head *Message
	var content []string
	sections := make(map[string]*string)
	for _, message := range messages {
		if message.Role != RoleSystem || message.System == nil {
			continue
		}
		if head == nil {
			cloned := cloneMessage(message)
			head = &cloned
		}
		if message.System.Content != "" {
			content = append(content, message.System.Content)
		}
		for name, value := range message.System.Sections {
			if value == nil {
				delete(sections, name)
			} else {
				copied := *value
				sections[name] = &copied
			}
		}
	}
	if head == nil {
		return nil
	}
	head.System = &SystemMessagePayload{Content: strings.Join(content, "\n\n"), Sections: sections, ToolsAdded: GetCurrentTools(messages)}
	return head
}

// GetCurrentSystemPrompt renders named sections in sorted order, matching the
// deterministic Go transcript wire representation.
func GetCurrentSystemPrompt(messages []Message) string {
	head := GetCurrentSystemMessage(messages)
	if head == nil {
		return ""
	}
	var text []string
	if head.System.Content != "" {
		text = append(text, head.System.Content)
	}
	names := make([]string, 0, len(head.System.Sections))
	for name := range head.System.Sections {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if value := head.System.Sections[name]; value != nil && *value != "" {
			text = append(text, *value)
		}
	}
	return strings.Join(text, "\n\n")
}

func toolDeclarations(tools []ToolDefinition) []ToolDeclaration {
	result := make([]ToolDeclaration, len(tools))
	for i, tool := range tools {
		result[i] = ToolDeclaration{Name: tool.Name, Description: tool.Description, Parameters: cloneStringAnyMap(tool.Parameters), OutputSchema: cloneStringAnyMap(tool.OutputSchema), ConstrainedSampling: tool.ConstrainedSampling.Clone()}
	}
	return result
}

func toolStateChanges(messages []Message, tools []ToolDefinition) SystemMessagePayload {
	previous, current := GetCurrentTools(messages), toolDeclarations(tools)
	byName := make(map[string]ToolDeclaration, len(previous))
	for _, tool := range previous {
		byName[tool.Name] = tool
	}
	currentByName := make(map[string]ToolDeclaration, len(current))
	for _, tool := range current {
		currentByName[tool.Name] = tool
	}
	var update SystemMessagePayload
	equal := func(a, b ToolDeclaration) bool {
		left, err := json.Marshal(a)
		if err != nil {
			return false
		}
		right, err := json.Marshal(b)
		return err == nil && reflect.DeepEqual(left, right)
	}
	for _, tool := range previous {
		next, exists := currentByName[tool.Name]
		if !exists || !equal(tool, next) {
			update.ToolsRemoved = append(update.ToolsRemoved, ToolReference{Name: tool.Name})
		}
	}
	for _, tool := range current {
		prior, exists := byName[tool.Name]
		if !exists || !equal(prior, tool) {
			update.ToolsAdded = append(update.ToolsAdded, tool)
		}
	}
	return update
}

func initializeSystemTranscript(definition AgentDefinition, snapshot *AgentSnapshot, appendOnly bool) {
	if GetCurrentSystemMessage(snapshot.Messages) != nil {
		snapshot.SystemPrompt = GetCurrentSystemPrompt(snapshot.Messages)
		return
	}
	prompt := snapshot.SystemPrompt
	if prompt == "" {
		prompt = definition.SystemPrompt
	}
	if prompt == "" && len(definition.Tools) == 0 {
		return
	}
	head := NewSystemMessage(SystemMessagePayload{Content: prompt, ToolsAdded: toolDeclarations(definition.Tools)})
	head.Timestamp = time.Time{}
	if appendOnly {
		// A journal owns the existing prefix. Introducing system context on a
		// later run must append a declaration instead of rewriting that prefix.
		snapshot.Messages = append(snapshot.Messages, head)
	} else {
		snapshot.Messages = append([]Message{head}, snapshot.Messages...)
	}
	snapshot.SystemPrompt = GetCurrentSystemPrompt(snapshot.Messages)
}
