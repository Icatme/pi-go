package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Icatme/pi-go/pkg/pigo"
	"github.com/google/jsonschema-go/jsonschema"
)

func newToolArgumentValidator(tool ToolDefinition) (func(any) (any, error), error) {
	var resolved *jsonschema.Resolved
	if len(tool.Parameters) > 0 {
		encoded, err := json.Marshal(tool.Parameters)
		if err != nil {
			return nil, fmt.Errorf("agent: marshal schema for tool %q: %w", tool.Name, err)
		}

		var schema jsonschema.Schema
		if err := json.Unmarshal(encoded, &schema); err != nil {
			return nil, fmt.Errorf("agent: decode schema for tool %q: %w", tool.Name, err)
		}
		resolved, err = schema.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("agent: resolve schema for tool %q: %w", tool.Name, err)
		}
	}

	return func(args any) (any, error) {
		validated := cloneAny(args)
		if resolved != nil {
			if object, ok := validated.(map[string]any); ok && tool.ParseArguments == nil {
				coerced, err := pigo.ValidateToolArguments(
					pigo.Tool{Name: tool.Name, Parameters: tool.Parameters},
					pigo.ToolCall{Name: tool.Name, Arguments: object},
				)
				if err != nil {
					return nil, err
				}
				validated = coerced
			}
			instance := validated
			if tool.ParseArguments != nil {
				projected, err := projectJSONInstance(validated)
				if err != nil {
					return nil, fmt.Errorf("agent: project arguments for tool %q: %w", tool.Name, err)
				}
				instance = projected
			}
			if err := resolved.Validate(instance); err != nil {
				return nil, fmt.Errorf("agent: arguments for tool %q do not match schema: %w", tool.Name, err)
			}
		}
		return validated, nil
	}, nil
}

func validateToolDefinitions(tools []ToolDefinition) error {
	for _, tool := range tools {
		if _, err := newToolArgumentValidator(tool); err != nil {
			return err
		}
	}
	return nil
}

func projectJSONInstance(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var instance any
	if err := decoder.Decode(&instance); err != nil {
		return nil, err
	}
	return normalizeJSONNumbers(instance)
}

func normalizeJSONNumbers(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		text := typed.String()
		if !strings.ContainsAny(text, ".eE") {
			if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
				return integer, nil
			}
			if integer, err := strconv.ParseUint(text, 10, 64); err == nil {
				return integer, nil
			}
		}
		number, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, err
		}
		return number, nil
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			normalized, err := normalizeJSONNumbers(item)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeJSONNumbers(item)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	default:
		return typed, nil
	}
}
