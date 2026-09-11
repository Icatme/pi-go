package pigo

import (
	"slices"
	"testing"
)

func TestCommandCodeCatalogPreservesThinkingOff(t *testing.T) {
	for _, test := range []struct {
		id        string
		reasoning bool
		support   CapabilitySupport
		levels    []ModelThinkingLevel
	}{
		{"claude-fable-5", true, CapabilitySupported, []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelLow, ModelThinkingLevelMedium, ModelThinkingLevelHigh, ModelThinkingLevelXHigh, ModelThinkingLevelMax}},
		{"gpt-5.6-luna", true, CapabilitySupported, []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelLow, ModelThinkingLevelMedium, ModelThinkingLevelHigh, ModelThinkingLevelXHigh, ModelThinkingLevelMax}},
		{"meta/muse-spark-1.3", true, CapabilitySupported, []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelMinimal, ModelThinkingLevelLow, ModelThinkingLevelMedium, ModelThinkingLevelHigh, ModelThinkingLevelXHigh}},
		{"MiniMaxAI/MiniMax-M3", true, CapabilityUnsupported, []ModelThinkingLevel{ModelThinkingLevelOff}},
		{"claude-haiku-4-5-20251001", false, CapabilityUnsupported, []ModelThinkingLevel{ModelThinkingLevelOff}},
		{"unknown/model", false, CapabilityUnknown, []ModelThinkingLevel{ModelThinkingLevelOff}},
	} {
		t.Run(test.id, func(t *testing.T) {
			model := newCommandCodeModel(test.id, test.id, 200000, 65536, UsageCost{})
			if levels := GetSupportedThinkingLevels(model); !slices.Equal(levels, test.levels) {
				t.Errorf("supported thinking levels=%v want %v", levels, test.levels)
			}
			if got := ClampThinkingLevel(model, ModelThinkingLevelOff); got != ModelThinkingLevelOff {
				t.Errorf("off was enabled as %q", got)
			}
			if model.Reasoning != test.reasoning || model.Capabilities.ReasoningLevels != test.support {
				t.Errorf("changed reasoning capabilities: reasoning=%v capabilities=%+v", model.Reasoning, model.Capabilities)
			}
			if test.id == "unknown/model" && model.Capabilities.Reasoning != CapabilityUnknown {
				t.Errorf("unknown model claims reasoning support: %+v", model.Capabilities)
			}
		})
	}
}
