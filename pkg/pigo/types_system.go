package pigo

import "time"

// SystemMessage records instruction and tool state changes at their position in
// a transcript. Later content appends instructions; a nil section removes it.
// Section names are rendered in sorted order to keep Go map iteration stable.
type SystemMessage struct {
	Content      string
	Sections     map[string]*string
	ToolsAdded   []Tool
	ToolsRemoved []ToolReference
	Timestamp    time.Time
}

func (SystemMessage) messageRole() string { return "system" }

func (message SystemMessage) clone() Message {
	cloned := message
	cloned.Sections = cloneSystemSections(message.Sections)
	cloned.ToolsAdded = cloneTools(message.ToolsAdded)
	cloned.ToolsRemoved = append([]ToolReference(nil), message.ToolsRemoved...)
	return cloned
}

func cloneSystemSections(sections map[string]*string) map[string]*string {
	if sections == nil {
		return nil
	}
	cloned := make(map[string]*string, len(sections))
	for name, value := range sections {
		if value == nil {
			cloned[name] = nil
		} else {
			text := *value
			cloned[name] = &text
		}
	}
	return cloned
}

func cloneTools(tools []Tool) []Tool {
	if tools == nil {
		return nil
	}
	cloned := make([]Tool, len(tools))
	for index, tool := range tools {
		cloned[index] = tool
		cloned[index].Parameters = cloneAny(tool.Parameters)
		cloned[index].OutputSchema = cloneAny(tool.OutputSchema)
	}
	return cloned
}
