package pigo

import (
	"fmt"
	"math"
	"strings"
)

// DeepSeek V4.1 Flash facts verified against the 2026-09-10 API documentation:
// https://api-docs.deepseek.com/zh-cn/quick_start/pricing/
// https://api-docs.deepseek.com/zh-cn/guides/thinking_mode/
func newDeepSeekFlashModel() Model {
	return Model{
		ID:        "deepseek-flash",
		Name:      "DeepSeek V4.1 Flash",
		API:       "deepseek-chat-completions",
		BaseURL:   "https://api.deepseek.com",
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelOff:     "none",
			ModelThinkingLevelMinimal: "low",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "high",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "high",
			ModelThinkingLevelMax:     "max",
		},
		Capabilities: ModelCapabilities{
			DefaultReasoningLevel:      ModelThinkingLevelHigh,
			Temperature:                CapabilitySupported,
			TemperatureReasoningLevels: []ModelThinkingLevel{ModelThinkingLevelOff},
			TopP:                       CapabilitySupported,
			TopPReasoningLevels: []ModelThinkingLevel{
				ModelThinkingLevelMinimal, ModelThinkingLevelLow, ModelThinkingLevelMedium,
				ModelThinkingLevelHigh, ModelThinkingLevelXHigh, ModelThinkingLevelMax,
			},
		},
		Input:         []InputType{InputText, InputImage},
		ContextWindow: 1000000,
		MaxTokens:     384000,
	}
}

func isDeepSeekFlash(model Model) bool {
	return model.Provider == "deepseek" && model.ID == "deepseek-flash"
}

func deepSeekDefaultThinkingLevel(model Model) ThinkingLevel {
	if isDeepSeekFlash(model) {
		return ThinkingLevelHigh
	}
	return ThinkingLevelXHigh
}

func deepSeekFlashThinking(level ThinkingLevel) *deepSeekThinkingOptions {
	switch strings.TrimSpace(string(level)) {
	case "none", "off", "disabled":
		return &deepSeekThinkingOptions{Type: "disabled"}
	case "minimal", "low":
		return &deepSeekThinkingOptions{Type: "enabled", ReasoningEffort: "low"}
	case "max", "ultra":
		return &deepSeekThinkingOptions{Type: "enabled", ReasoningEffort: "max"}
	default:
		return &deepSeekThinkingOptions{Type: "enabled", ReasoningEffort: "high"}
	}
}

func validateDeepSeekFlashOptions(model Model, options ProviderStreamOptions) error {
	if !isDeepSeekFlash(model) {
		return nil
	}
	thinking := deepSeekFlashThinking(options.Reasoning).Type == "enabled"
	if options.Temperature != nil {
		value := *options.Temperature
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 2 {
			return fmt.Errorf("deepseek-flash temperature must be between 0 and 2")
		}
		if thinking {
			return fmt.Errorf("deepseek-flash temperature is not supported in thinking mode")
		}
	}
	if options.TopP != nil {
		value := *options.TopP
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0.95 || value > 1 {
			return fmt.Errorf("deepseek-flash top_p must be between 0.95 and 1")
		}
		if !thinking {
			return fmt.Errorf("deepseek-flash top_p is fixed in non-thinking mode")
		}
	}
	if options.ParallelToolCalls != nil {
		return fmt.Errorf("deepseek-flash parallel_tool_calls is not configurable")
	}
	return nil
}

func deepSeekFlashUserContent(content any) any {
	blocks, ok := content.([]ContentBlock)
	if !ok {
		return deepSeekTextFromContent(content)
	}
	parts := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block := block.(type) {
		case TextContent:
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, map[string]any{"type": "text", "text": block.Text})
			}
		case ImageContent:
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": "data:" + block.MIMEType + ";base64," + block.Data},
			})
		}
	}
	return parts
}
