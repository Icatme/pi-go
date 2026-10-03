package pigo

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

type Tool struct {
	Name                string                   `json:"name"`
	Description         string                   `json:"description,omitempty"`
	Parameters          any                      `json:"parameters,omitempty"`
	OutputSchema        any                      `json:"outputSchema,omitempty"`
	Validator           ToolArgumentsValidator   `json:"-"`
	ConstrainedSampling *ToolConstrainedSampling `json:"constrainedSampling,omitempty"`
}

type ToolReference struct {
	Name string `json:"name"`
}

type Context struct {
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
	HostedTools  []HostedTool
}

type ToolArgumentsValidator interface {
	Validate(map[string]any) (map[string]any, error)
}

type ToolArgumentsValidatorFunc func(map[string]any) (map[string]any, error)

func (f ToolArgumentsValidatorFunc) Validate(args map[string]any) (map[string]any, error) {
	return f(args)
}
