package pigo

func ValidateToolArguments(tool Tool, toolCall ToolCall) (map[string]any, error) {
	if tool.Validator == nil {
		return cloneMap(toolCall.Arguments), nil
	}
	return tool.Validator.Validate(cloneMap(toolCall.Arguments))
}
