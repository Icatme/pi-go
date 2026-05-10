package pigo

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

func ValidateToolCall(tools []Tool, toolCall ToolCall) (map[string]any, error) {
	for _, tool := range tools {
		if tool.Name == toolCall.Name {
			return ValidateToolArguments(tool, toolCall)
		}
	}
	return nil, fmt.Errorf("Tool %q not found", toolCall.Name)
}

func ValidateToolArguments(tool Tool, toolCall ToolCall) (map[string]any, error) {
	args := cloneMap(toolCall.Arguments)

	if tool.Validator != nil {
		return tool.Validator.Validate(args)
	}

	if tool.Parameters == nil {
		return args, nil
	}

	schema, ok := normalizeToolSchema(tool.Parameters)
	if !ok {
		return args, nil
	}

	validated, errors := validateSchemaValue(schema, args, "")
	if len(errors) == 0 {
		validatedArgs, ok := validated.(map[string]any)
		if !ok {
			return map[string]any{}, nil
		}
		return validatedArgs, nil
	}

	return nil, fmt.Errorf(
		"Validation failed for tool %q:\n%s\n\nReceived arguments:\n%s",
		toolCall.Name,
		formatValidationErrors(errors),
		marshalIndentedJSON(toolCall.Arguments),
	)
}

type schemaValidationError struct {
	Path    string
	Message string
}

func normalizeToolSchema(parameters any) (map[string]any, bool) {
	switch typed := parameters.(type) {
	case map[string]any:
		return cloneMap(typed), true
	default:
		bytes, err := json.Marshal(parameters)
		if err != nil {
			return nil, false
		}
		var schema map[string]any
		if err := json.Unmarshal(bytes, &schema); err != nil {
			return nil, false
		}
		return schema, true
	}
}

func formatValidationErrors(errors []schemaValidationError) string {
	lines := make([]string, 0, len(errors))
	for _, err := range errors {
		path := err.Path
		if path == "" {
			path = "root"
		}
		lines = append(lines, fmt.Sprintf("  - %s: %s", path, err.Message))
	}
	return strings.Join(lines, "\n")
}

func marshalIndentedJSON(value any) string {
	bytes, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(bytes)
}

// validateSchemaValue intentionally supports a small JSON-Schema-like subset.
// Keywords outside the implemented set, including $ref, are ignored instead of resolved.
func validateSchemaValue(schema map[string]any, value any, path string) (any, []schemaValidationError) {
	if len(schema) == 0 {
		return value, nil
	}

	if anyOf, ok := schema["anyOf"].([]any); ok && len(anyOf) > 0 {
		return validateSchemaAlternatives(anyOf, value, path, false)
	}
	if oneOf, ok := schema["oneOf"].([]any); ok && len(oneOf) > 0 {
		return validateSchemaAlternatives(oneOf, value, path, true)
	}
	if allOf, ok := schema["allOf"].([]any); ok && len(allOf) > 0 {
		return validateSchemaAllOf(schema, allOf, value, path)
	}

	if constValue, ok := schema["const"]; ok && !jsonValuesEqual(value, constValue) {
		return nil, []schemaValidationError{{Path: path, Message: fmt.Sprintf("must be equal to %v", constValue)}}
	}

	if enumValues, ok := schema["enum"].([]any); ok && len(enumValues) > 0 {
		for _, enumValue := range enumValues {
			if jsonValuesEqual(value, enumValue) {
				goto typeValidation
			}
		}
		return nil, []schemaValidationError{{Path: path, Message: "must be equal to one of the allowed values"}}
	}

typeValidation:
	if schemaType, ok := schema["type"]; ok {
		return validateTypedSchema(schema, schemaType, value, path)
	}

	switch {
	case schema["properties"] != nil || schema["required"] != nil:
		return validateObjectSchema(schema, value, path)
	case schema["items"] != nil:
		return validateArraySchema(schema, value, path)
	default:
		return value, nil
	}
}

func validateSchemaAlternatives(alternatives []any, value any, path string, requireSingle bool) (any, []schemaValidationError) {
	var (
		matches int
		result  any
	)
	for _, alternative := range alternatives {
		schema, ok := alternative.(map[string]any)
		if !ok {
			continue
		}
		validated, errors := validateSchemaValue(schema, cloneAny(value), path)
		if len(errors) == 0 {
			matches++
			result = validated
			if !requireSingle {
				return result, nil
			}
		}
	}

	if requireSingle {
		if matches == 1 {
			return result, nil
		}
		if matches > 1 {
			return nil, []schemaValidationError{{Path: path, Message: "must match exactly one schema"}}
		}
		return nil, []schemaValidationError{{Path: path, Message: "must match exactly one schema"}}
	}

	return nil, []schemaValidationError{{Path: path, Message: "must match at least one schema"}}
}

func validateSchemaAllOf(baseSchema map[string]any, alternatives []any, value any, path string) (any, []schemaValidationError) {
	currentValue := cloneAny(value)
	for _, alternative := range alternatives {
		schema, ok := alternative.(map[string]any)
		if !ok {
			continue
		}
		validated, errors := validateSchemaValue(removeSchemaCompositionKeywords(schema), currentValue, path)
		if len(errors) > 0 {
			return nil, errors
		}
		currentValue = validated
	}

	remainingSchema := removeSchemaCompositionKeywords(baseSchema)
	return validateSchemaValue(remainingSchema, currentValue, path)
}

func validateTypedSchema(schema map[string]any, schemaType any, value any, path string) (any, []schemaValidationError) {
	switch typed := schemaType.(type) {
	case string:
		return validateSingleType(schema, typed, value, path)
	case []any:
		// For union types, first check if the value already matches a member type
		// without coercion. Only coerce if no member matches natively.
		var candidateTypes []string
		for _, candidate := range typed {
			candidateType, ok := candidate.(string)
			if !ok {
				continue
			}
			candidateTypes = append(candidateTypes, candidateType)
			if matchesJSONType(value, candidateType) {
				return validateSingleType(schema, candidateType, value, path)
			}
		}
		for _, candidateType := range candidateTypes {
			validated, errors := validateSingleType(schema, candidateType, cloneAny(value), path)
			if len(errors) == 0 {
				return validated, nil
			}
		}
		return nil, []schemaValidationError{{Path: path, Message: "must match one of the allowed types"}}
	default:
		return value, nil
	}
}

func matchesJSONType(value any, schemaType string) bool {
	switch schemaType {
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		n, ok := value.(float64)
		return ok && math.Trunc(n) == n
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "null":
		return value == nil
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		return false
	}
}

func validateSingleType(schema map[string]any, schemaType string, value any, path string) (any, []schemaValidationError) {
	switch schemaType {
	case "object":
		return validateObjectSchema(schema, value, path)
	case "array":
		return validateArraySchema(schema, value, path)
	case "string":
		return validateStringSchema(schema, value, path)
	case "number":
		return validateNumberSchema(schema, value, path, false)
	case "integer":
		return validateNumberSchema(schema, value, path, true)
	case "boolean":
		return validateBooleanSchema(schema, value, path)
	case "null":
		if value == nil {
			return nil, nil
		}
		if s, ok := value.(string); ok && s == "" {
			return nil, nil
		}
		if n, ok := toFloat64(value); ok && n == 0 {
			return nil, nil
		}
		if b, ok := value.(bool); ok && !b {
			return nil, nil
		}
		return nil, []schemaValidationError{{Path: path, Message: "must be null"}}
	default:
		return value, nil
	}
}

func validateObjectSchema(schema map[string]any, value any, path string) (any, []schemaValidationError) {
	objectValue, ok := value.(map[string]any)
	if !ok {
		return nil, []schemaValidationError{{Path: path, Message: "must be object"}}
	}

	result := cloneMap(objectValue)
	var errors []schemaValidationError

	if minProperties, ok := schemaInt(schema["minProperties"]); ok && len(result) < minProperties {
		errors = append(errors, schemaValidationError{
			Path:    path,
			Message: fmt.Sprintf("must have at least %d properties", minProperties),
		})
	}
	if maxProperties, ok := schemaInt(schema["maxProperties"]); ok && len(result) > maxProperties {
		errors = append(errors, schemaValidationError{
			Path:    path,
			Message: fmt.Sprintf("must have no more than %d properties", maxProperties),
		})
	}

	required := schemaStringSlice(schema["required"])
	for _, name := range required {
		if _, exists := result[name]; !exists {
			errors = append(errors, schemaValidationError{
				Path:    joinSchemaPath(path, name),
				Message: "must have required property",
			})
		}
	}

	properties := schemaMapMap(schema["properties"])
	for name, propertySchema := range properties {
		currentValue, exists := result[name]
		if !exists {
			continue
		}
		validated, propertyErrors := validateSchemaValue(propertySchema, currentValue, joinSchemaPath(path, name))
		if len(propertyErrors) > 0 {
			errors = append(errors, propertyErrors...)
			continue
		}
		result[name] = validated
	}

	if len(errors) > 0 {
		return nil, errors
	}

	if additional, exists := schema["additionalProperties"]; exists {
		switch typed := additional.(type) {
		case bool:
			if !typed {
				for key := range result {
					if _, known := properties[key]; !known {
						errors = append(errors, schemaValidationError{
							Path:    joinSchemaPath(path, key),
							Message: "must NOT have additional properties",
						})
					}
				}
			}
		case map[string]any:
			for key, currentValue := range result {
				if _, known := properties[key]; known {
					continue
				}
				validated, propertyErrors := validateSchemaValue(typed, currentValue, joinSchemaPath(path, key))
				if len(propertyErrors) > 0 {
					errors = append(errors, propertyErrors...)
					continue
				}
				result[key] = validated
			}
		}
	}

	if len(errors) > 0 {
		return nil, errors
	}
	return result, nil
}

func validateArraySchema(schema map[string]any, value any, path string) (any, []schemaValidationError) {
	arrayValue, ok := value.([]any)
	if !ok {
		return nil, []schemaValidationError{{Path: path, Message: "must be array"}}
	}

	var errors []schemaValidationError
	if minItems, ok := schemaInt(schema["minItems"]); ok && len(arrayValue) < minItems {
		errors = append(errors, schemaValidationError{
			Path:    path,
			Message: fmt.Sprintf("must NOT have fewer than %d items", minItems),
		})
	}
	if maxItems, ok := schemaInt(schema["maxItems"]); ok && len(arrayValue) > maxItems {
		errors = append(errors, schemaValidationError{
			Path:    path,
			Message: fmt.Sprintf("must NOT have more than %d items", maxItems),
		})
	}
	if len(errors) > 0 {
		return nil, errors
	}

	itemsSchema, ok := schema["items"].(map[string]any)
	if !ok {
		return cloneAny(arrayValue), nil
	}

	result := make([]any, len(arrayValue))
	for index, item := range arrayValue {
		validated, itemErrors := validateSchemaValue(itemsSchema, item, joinSchemaPath(path, strconv.Itoa(index)))
		if len(itemErrors) > 0 {
			errors = append(errors, itemErrors...)
			continue
		}
		result[index] = validated
	}
	if len(errors) > 0 {
		return nil, errors
	}
	return result, nil
}

func validateStringSchema(schema map[string]any, value any, path string) (any, []schemaValidationError) {
	stringValue, ok := coerceString(value)
	if !ok {
		return nil, []schemaValidationError{{Path: path, Message: "must be string"}}
	}

	if minLength, ok := schemaInt(schema["minLength"]); ok && len(stringValue) < minLength {
		return nil, []schemaValidationError{{Path: path, Message: fmt.Sprintf("must NOT have fewer than %d characters", minLength)}}
	}
	if maxLength, ok := schemaInt(schema["maxLength"]); ok && len(stringValue) > maxLength {
		return nil, []schemaValidationError{{Path: path, Message: fmt.Sprintf("must NOT have more than %d characters", maxLength)}}
	}
	if pattern, ok := schema["pattern"].(string); ok && pattern != "" {
		matched, err := regexp.MatchString(pattern, stringValue)
		if err == nil && !matched {
			return nil, []schemaValidationError{{Path: path, Message: fmt.Sprintf("must match pattern %q", pattern)}}
		}
	}

	if enumValues, ok := schema["enum"].([]any); ok && len(enumValues) > 0 {
		matched := false
		for _, enumValue := range enumValues {
			enumString, ok := enumValue.(string)
			if ok && stringValue == enumString {
				matched = true
				break
			}
		}
		if !matched {
			return nil, []schemaValidationError{{Path: path, Message: "must be equal to one of the allowed values"}}
		}
	}

	return stringValue, nil
}

func validateNumberSchema(schema map[string]any, value any, path string, integerOnly bool) (any, []schemaValidationError) {
	numberValue, ok := coerceNumber(value, integerOnly)
	if !ok {
		message := "must be number"
		if integerOnly {
			message = "must be integer"
		}
		return nil, []schemaValidationError{{Path: path, Message: message}}
	}

	if enumValues, ok := schema["enum"].([]any); ok && len(enumValues) > 0 {
		matched := false
		for _, enumValue := range enumValues {
			enumNumber, ok := toFloat64(enumValue)
			if ok && numberValue == enumNumber {
				matched = true
				break
			}
		}
		if !matched {
			return nil, []schemaValidationError{{Path: path, Message: "must be equal to one of the allowed values"}}
		}
	}

	if minimum, ok := toFloat64(schema["minimum"]); ok && numberValue < minimum {
		return nil, []schemaValidationError{{Path: path, Message: fmt.Sprintf("must be >= %v", trimFloat(minimum))}}
	}
	if maximum, ok := toFloat64(schema["maximum"]); ok && numberValue > maximum {
		return nil, []schemaValidationError{{Path: path, Message: fmt.Sprintf("must be <= %v", trimFloat(maximum))}}
	}
	if exclusiveMinimum, ok := toFloat64(schema["exclusiveMinimum"]); ok && numberValue <= exclusiveMinimum {
		return nil, []schemaValidationError{{Path: path, Message: fmt.Sprintf("must be > %v", trimFloat(exclusiveMinimum))}}
	}
	if exclusiveMaximum, ok := toFloat64(schema["exclusiveMaximum"]); ok && numberValue >= exclusiveMaximum {
		return nil, []schemaValidationError{{Path: path, Message: fmt.Sprintf("must be < %v", trimFloat(exclusiveMaximum))}}
	}

	if integerOnly {
		return int(numberValue), nil
	}
	return numberValue, nil
}

func validateBooleanSchema(schema map[string]any, value any, path string) (any, []schemaValidationError) {
	booleanValue, ok := coerceBoolean(value)
	if !ok {
		return nil, []schemaValidationError{{Path: path, Message: "must be boolean"}}
	}

	if enumValues, ok := schema["enum"].([]any); ok && len(enumValues) > 0 {
		matched := false
		for _, enumValue := range enumValues {
			enumBool, ok := enumValue.(bool)
			if ok && booleanValue == enumBool {
				matched = true
				break
			}
		}
		if !matched {
			return nil, []schemaValidationError{{Path: path, Message: "must be equal to one of the allowed values"}}
		}
	}

	return booleanValue, nil
}

func schemaStringSlice(value any) []string {
	values, ok := value.([]string)
	if ok {
		return append([]string(nil), values...)
	}

	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if ok {
			result = append(result, text)
		}
	}
	return result
}

func schemaMapMap(value any) map[string]map[string]any {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]map[string]any, len(raw))
	for key, item := range raw {
		schema, ok := item.(map[string]any)
		if ok {
			result[key] = schema
		}
	}
	return result
}

func joinSchemaPath(base string, part string) string {
	if base == "" {
		return part
	}
	if part == "" {
		return base
	}
	return base + "/" + part
}

func coerceString(value any) (string, bool) {
	if value == nil {
		return "", true
	}
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		if typed {
			return "true", true
		}
		return "false", true
	case int:
		return strconv.Itoa(typed), true
	case int8, int16, int32, int64:
		return fmt.Sprintf("%d", typed), true
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", typed), true
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	default:
		return "", false
	}
}

func coerceNumber(value any, integerOnly bool) (float64, bool) {
	if value == nil {
		return 0, true
	}
	numberValue, ok := toFloat64(value)
	if !ok {
		return 0, false
	}
	if integerOnly && math.Trunc(numberValue) != numberValue {
		return 0, false
	}
	return numberValue, true
}

func toFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		value, err := typed.Float64()
		return value, err == nil
	case string:
		value, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return value, err == nil
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func coerceBoolean(value any) (bool, bool) {
	if value == nil {
		return false, true
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true":
			return true, true
		case "false":
			return false, true
		default:
			return false, false
		}
	case int:
		return typed != 0, typed == 0 || typed == 1
	case int8:
		return typed != 0, typed == 0 || typed == 1
	case int16:
		return typed != 0, typed == 0 || typed == 1
	case int32:
		return typed != 0, typed == 0 || typed == 1
	case int64:
		return typed != 0, typed == 0 || typed == 1
	case float32:
		return typed != 0, typed == 0 || typed == 1
	case float64:
		return typed != 0, typed == 0 || typed == 1
	default:
		return false, false
	}
}

func jsonValuesEqual(left any, right any) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftBytes) == string(rightBytes)
}

func removeSchemaCompositionKeywords(schema map[string]any) map[string]any {
	cleaned := cloneMap(schema)
	delete(cleaned, "allOf")
	delete(cleaned, "anyOf")
	delete(cleaned, "oneOf")
	return cleaned
}

func schemaInt(value any) (int, bool) {
	number, ok := toFloat64(value)
	if !ok || math.Trunc(number) != number {
		return 0, false
	}
	return int(number), true
}

func trimFloat(value float64) any {
	if math.Trunc(value) == value {
		return int(value)
	}
	return value
}
