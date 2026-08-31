package pigo

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

var (
	errOpenAIResponsesSamplingInvalid     = errors.New("invalid OpenAI Responses sampling option")
	errOpenAIResponsesSamplingUnsupported = errors.New("OpenAI Responses sampling option is not supported by model")
	errOpenAIResponsesSamplingReasoning   = errors.New("OpenAI Responses sampling option is not supported at the selected reasoning level")
)

func validateOpenAIResponsesSamplingOptions(model Model, options ProviderStreamOptions) error {
	if options.Temperature == nil && options.TopP == nil {
		return nil
	}

	snapshot, ok := LookupModelCapabilities(model.Provider, model.ID)
	if !ok || snapshot.WireAPI != model.API {
		return fmt.Errorf("%w: no exact capability facts for %q/%q", errOpenAIResponsesSamplingUnsupported, model.Provider, model.ID)
	}

	if options.Temperature != nil {
		if value := *options.Temperature; math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 2 {
			return fmt.Errorf("%w: temperature must be between 0 and 2", errOpenAIResponsesSamplingInvalid)
		}
		if err := validateOpenAIResponsesSamplingReasoning(
			model,
			options.Reasoning,
			"temperature",
			snapshot.Capabilities.Temperature,
			snapshot.Capabilities.TemperatureReasoningLevels,
			snapshot.Capabilities.DefaultReasoningLevel,
		); err != nil {
			return err
		}
	}

	if options.TopP != nil {
		if value := *options.TopP; math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return fmt.Errorf("%w: top_p must be between 0 and 1", errOpenAIResponsesSamplingInvalid)
		}
		if err := validateOpenAIResponsesSamplingReasoning(
			model,
			options.Reasoning,
			"top_p",
			snapshot.Capabilities.TopP,
			snapshot.Capabilities.TopPReasoningLevels,
			snapshot.Capabilities.DefaultReasoningLevel,
		); err != nil {
			return err
		}
	}

	return nil
}

func validateOpenAIResponsesSamplingReasoning(
	model Model,
	reasoning ThinkingLevel,
	parameter string,
	support CapabilitySupport,
	supportedLevels []ModelThinkingLevel,
	defaultLevel ModelThinkingLevel,
) error {
	if support != CapabilitySupported {
		return fmt.Errorf("%w: %s for %q/%q is %s", errOpenAIResponsesSamplingUnsupported, parameter, model.Provider, model.ID, support)
	}
	if defaultLevel == "" && len(supportedLevels) == 0 {
		return nil
	}

	effectiveLevel := defaultLevel
	requested := strings.TrimSpace(string(reasoning))
	if requested != "" {
		switch requested {
		case "none":
			effectiveLevel = ModelThinkingLevelOff
		case "low", "medium", "high", "xhigh", "max":
			effectiveLevel = ModelThinkingLevel(requested)
		default:
			return fmt.Errorf("%w: %s for %q/%q with unrecognized reasoning effort %q", errOpenAIResponsesSamplingReasoning, parameter, model.Provider, model.ID, requested)
		}
	}
	if effectiveLevel == "" {
		return fmt.Errorf("%w: %s for %q/%q has no known default reasoning level", errOpenAIResponsesSamplingReasoning, parameter, model.Provider, model.ID)
	}
	if !slices.Contains(supportedLevels, effectiveLevel) {
		return fmt.Errorf("%w: %s for %q/%q with reasoning %q", errOpenAIResponsesSamplingReasoning, parameter, model.Provider, model.ID, effectiveLevel)
	}
	return nil
}
