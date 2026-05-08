package pigo

import "strings"

const anthropicClaudeCodeVersion = "2.1.75"

var anthropicClaudeCodeToolNames = []string{
	"Read",
	"Write",
	"Edit",
	"Bash",
	"Grep",
	"Glob",
	"AskUserQuestion",
	"EnterPlanMode",
	"ExitPlanMode",
	"KillShell",
	"NotebookEdit",
	"Skill",
	"Task",
	"TaskOutput",
	"TodoWrite",
	"WebFetch",
	"WebSearch",
}

var anthropicClaudeCodeToolNameLookup = func() map[string]string {
	lookup := make(map[string]string, len(anthropicClaudeCodeToolNames))
	for _, name := range anthropicClaudeCodeToolNames {
		lookup[strings.ToLower(name)] = name
	}
	return lookup
}()

func isAnthropicOAuthToken(token string) bool {
	return strings.Contains(token, "sk-ant-oat")
}

func supportsAdaptiveAnthropicThinking(model Model) bool {
	return contains(model.ID, "opus-4-6") || contains(model.ID, "opus-4.6") || contains(model.ID, "sonnet-4-6") || contains(model.ID, "sonnet-4.6")
}

func mapAnthropicReasoningEffort(model Model, level ThinkingLevel) string {
	switch level {
	case ThinkingLevelMinimal, ThinkingLevelLow:
		return "low"
	case ThinkingLevelMedium:
		return "medium"
	case ThinkingLevelHigh:
		return "high"
	case ThinkingLevelXHigh:
		if SupportsXHigh(model) {
			return "max"
		}
		return "high"
	default:
		return "high"
	}
}

func anthropicToolNameForOutboundLegacy(name string, isOAuth bool) string {
	if !isOAuth {
		return name
	}
	if normalized, ok := anthropicClaudeCodeToolNameLookup[strings.ToLower(name)]; ok {
		return normalized
	}
	return name
}

func anthropicToolNameForInboundLegacy(name string, tools []Tool, isOAuth bool) string {
	if !isOAuth {
		return name
	}
	lowerName := strings.ToLower(name)
	for _, tool := range tools {
		if strings.ToLower(tool.Name) == lowerName {
			return tool.Name
		}
	}
	return name
}

func buildAnthropicToolChoice(toolChoice string, tools []Tool, isOAuth bool) any {
	normalized := strings.TrimSpace(toolChoice)
	if normalized == "" {
		return nil
	}

	switch normalized {
	case "auto", "any", "none":
		return map[string]any{"type": normalized}
	default:
		return map[string]any{
			"type": "tool",
			"name": anthropicToolNameForOutboundLegacy(normalized, isOAuth),
		}
	}
}

func buildAnthropicBetaHeader(model Model, isOAuth bool) string {
	betas := make([]string, 0, 4)
	if isOAuth {
		betas = append(betas, "claude-code-20250219", "oauth-2025-04-20")
	}
	betas = append(betas, "fine-grained-tool-streaming-2025-05-14")
	if !supportsAdaptiveAnthropicThinking(model) {
		betas = append(betas, "interleaved-thinking-2025-05-14")
	}
	return strings.Join(betas, ",")
}
