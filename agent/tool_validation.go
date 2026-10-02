package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Icatme/pi-go/internal/jsontext"
	"github.com/Icatme/pi-go/pkg/pigo"
	"github.com/google/jsonschema-go/jsonschema"
)

func newToolArgumentValidator(tool ToolDefinition, argumentLimit int) (func(any) (any, error), error) {
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
		if err := checkArgumentBudget(args, argumentLimit); err != nil {
			return nil, err
		}
		if err := checkArgumentStrings(args); err != nil {
			return nil, err
		}
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
			if err := checkArgumentBudget(validated, argumentLimit); err != nil {
				return nil, err
			}
			if err := checkArgumentStrings(validated); err != nil {
				return nil, err
			}
			instance := validated
			if tool.ParseArguments != nil {
				projected, err := projectJSONInstance(validated, argumentLimit)
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
		if err := validateChildTools(tool); err != nil {
			return err
		}
		if _, err := json.Marshal(tool.OutputSchema); err != nil {
			return fmt.Errorf("agent: marshal output schema for tool %q: %w", tool.Name, err)
		}
		if _, err := newToolArgumentValidator(tool, 0); err != nil {
			return err
		}
	}
	return nil
}

func validateToolOutput(tool ToolDefinition, result ToolResult) error {
	if len(tool.OutputSchema) == 0 {
		return nil
	}
	if len(result.StructuredContent) == 0 {
		return fmt.Errorf("tool %q returned no structured content for its output schema", tool.Name)
	}
	encoded, err := json.Marshal(tool.OutputSchema)
	if err != nil {
		return err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return err
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(result.StructuredContent))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	value, err = normalizeJSONNumbers(value)
	if err != nil {
		return err
	}
	if err := resolved.Validate(value); err != nil {
		return fmt.Errorf("tool %q output does not match schema: %w", tool.Name, err)
	}
	return nil
}

func projectJSONInstance(value any, argumentLimit int) (any, error) {
	if err := checkArgumentBudget(value, argumentLimit); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if argumentLimit > 0 && len(encoded) > argumentLimit {
		return nil, argumentBudgetFailure(nil)
	}
	if err := checkRawArgumentUnicode(encoded); err != nil {
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

func argumentTextFailure(err error) *ToolExecutionError {
	reason := "arguments"
	if errors.Is(err, jsontext.ErrInvalidUnicode) {
		reason = "invalid_unicode"
	}
	return &ToolExecutionError{Code: ToolFailureArgumentInvalid, Reason: reason, Message: err.Error(), Err: err,
		Execution: ToolExecutionInfo{Local: ToolLocalNotStarted, Remote: ToolRemoteNotDispatched}}
}

func checkArgumentStrings(value any) error {
	if err := jsontext.ValidateStrings(value); err != nil {
		return argumentTextFailure(err)
	}
	return nil
}

func checkRawArgumentUnicode(raw []byte) error {
	if err := jsontext.ValidateUnicode(raw); err != nil && !errors.Is(err, jsontext.ErrInvalidJSON) {
		return argumentTextFailure(err)
	}
	// Keep grammar diagnostics with the caller's existing JSON decoder.
	return nil
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
