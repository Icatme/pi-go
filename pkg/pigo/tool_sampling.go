package pigo

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Icatme/pi-go/internal/jsontext"
)

// ToolConstrainedSampling opts a single-string object tool into a native custom
// input. Type is "text" or "grammar"; grammar requires Syntax ("lark" or
// "regex") and Definition. Mode is "prefer" (the default) or "require".
// Prefer selects an ordinary function before dispatch when native support is
// unknown/unsupported. Require fails before authorization or network activity.
// This is a sampling constraint, not a replacement for argument validation.
type ToolConstrainedSampling struct {
	Type       string `json:"type"`
	Mode       string `json:"mode,omitempty"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

// Clone returns an independent declaration, including for a nil receiver.
func (sampling *ToolConstrainedSampling) Clone() *ToolConstrainedSampling {
	if sampling == nil {
		return nil
	}
	copy := *sampling
	return &copy
}

// ToolSamplingDecision makes the pre-request fallback observable to hosts.
// Representation is "function" or "custom". InputProperty is set for custom.
type ToolSamplingDecision struct {
	Representation string
	InputProperty  string
}

const MaxToolGrammarBytes = 64 << 10

// ResolveToolSampling uses only registered facts for the exact provider/model
// and wire API. Per-request Model.Capabilities cannot grant native support.
// The parameter schema must have exactly one property, a required string.
func ResolveToolSampling(model Model, tool Tool) (ToolSamplingDecision, error) {
	decision := ToolSamplingDecision{Representation: "function"}
	config := tool.ConstrainedSampling
	if config == nil {
		return decision, nil
	}
	fail := func(message string) (ToolSamplingDecision, error) {
		return decision, fmt.Errorf("tool %q constrained sampling: %s", tool.Name, message)
	}
	if tool.Name == "" || len(tool.Name) > 64 || strings.IndexFunc(tool.Name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) >= 0 {
		return fail("custom name must contain 1 to 64 letters, digits, underscores or hyphens")
	}
	if config.Mode != "" && config.Mode != "prefer" && config.Mode != "require" {
		return fail("mode must be prefer or require")
	}
	switch config.Type {
	case "text":
		if config.Syntax != "" || config.Definition != "" {
			return fail("text input cannot specify a grammar")
		}
	case "grammar":
		if config.Syntax != "lark" && config.Syntax != "regex" {
			return fail("syntax must be lark or regex")
		}
		if strings.TrimSpace(config.Definition) == "" || len(config.Definition) > MaxToolGrammarBytes || !utf8.ValidString(config.Definition) {
			return fail("grammar must be nonempty UTF-8 and at most 64 KiB")
		}
	default:
		return fail("type must be text or grammar")
	}
	property, err := toolSamplingInputProperty(tool.Parameters)
	if err != nil {
		return fail(err.Error())
	}
	facts, ok := LookupModelCapabilities(model.Provider, model.ID)
	supported := ok && facts.WireAPI == model.API && isOpenAIResponsesAPI(model.API) &&
		facts.Capabilities.CustomTools == CapabilitySupported &&
		(config.Type != "grammar" || facts.Capabilities.GrammarTools == CapabilitySupported)
	if !supported {
		if config.Mode == "require" {
			return fail(fmt.Sprintf("native %s input is not supported for %q/%q (%s)", config.Type, model.Provider, model.ID, model.API))
		}
		return decision, nil
	}
	return ToolSamplingDecision{Representation: "custom", InputProperty: property}, nil
}

func isOpenAIResponsesAPI(api API) bool {
	return api == "openai-responses" || api == "openai-codex-responses"
}

func toolSamplingInputProperty(parameters any) (string, error) {
	fail := fmt.Errorf("parameters must be an object with exactly one required string property")
	if err := jsontext.ValidateStrings(parameters); err != nil {
		return "", err
	}
	data, err := json.Marshal(parameters)
	if err != nil {
		return "", fmt.Errorf("invalid parameters: %w", err)
	}
	if len(data) > MaxToolGrammarBytes {
		return "", fmt.Errorf("parameter schema exceeds 64 KiB")
	}
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if json.Unmarshal(data, &schema) != nil || schema.Type != "object" || len(schema.Properties) != 1 || len(schema.Required) != 1 {
		return "", fail
	}
	property := schema.Required[0]
	if property == "" || len(property) > 256 || !utf8.ValidString(property) || schema.Properties[property].Type != "string" {
		return "", fail
	}
	return property, nil
}

func resolveContextToolSampling(model Model, ctx Context) (map[string]string, error) {
	custom := make(map[string]string)
	tools := currentContextTools(ctx)
	seen := make(map[string]bool)
	for _, tool := range tools {
		if configured, exists := seen[tool.Name]; exists && (configured || tool.ConstrainedSampling != nil) {
			return nil, fmt.Errorf("ambiguous sampling declaration for tool %q", tool.Name)
		}
		seen[tool.Name] = tool.ConstrainedSampling != nil
		decision, err := ResolveToolSampling(model, tool)
		if err != nil {
			return nil, err
		}
		if decision.Representation == "custom" {
			custom[tool.Name] = decision.InputProperty
		}
	}
	if len(custom) > 256 {
		return nil, fmt.Errorf("custom tool declarations exceed 256 tools")
	}
	// Check replay before any request. Failed/aborted partial calls are removed
	// by the same transformation used by the Responses encoder.
	if len(custom) > 0 {
		transcript := ResolveTranscript(NormalizeContext(ctx), supportsTranscriptSystemMessages(model))
		for _, message := range TransformMessages(transcript.Messages, model, NormalizeOpenAIResponsesToolCallID) {
			if assistant, ok := message.(AssistantMessage); ok {
				for _, content := range assistant.Content {
					if call, ok := content.(ToolCall); ok && custom[call.Name] != "" {
						input, ok := call.Arguments[custom[call.Name]].(string)
						if !ok || len(call.Arguments) != 1 || len(input) > MaxCustomToolInputBytes || !utf8.ValidString(input) {
							return nil, fmt.Errorf("custom tool %q replay requires one bounded string argument %q", call.Name, custom[call.Name])
						}
					}
				}
			}
		}
	}
	return custom, nil
}
