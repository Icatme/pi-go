package pigo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

type ResponseFormatType string

const (
	ResponseFormatJSON       ResponseFormatType = "json_object"
	ResponseFormatJSONSchema ResponseFormatType = "json_schema"
)

var (
	ErrResponseFormatInvalid     = errors.New("invalid response format")
	ErrResponseFormatUnsupported = errors.New("response format is not supported by provider")
	responseFormatNamePattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
)

const (
	maxResponseJSONSchemaBytes = 1 << 20
	maxResponseJSONSchemaDepth = 64
)

// ResponseFormat expresses provider-neutral structured text output.
// JSONSchema is required for json_schema and omitted for json_object.
type ResponseFormat struct {
	Type       ResponseFormatType
	Name       string
	JSONSchema json.RawMessage
	Strict     bool
}

func ValidateResponseFormat(model Model, format *ResponseFormat) error {
	if format == nil {
		return nil
	}
	capabilities := GetProviderCapabilities(model.Provider)
	switch format.Type {
	case ResponseFormatJSON:
		if strings.TrimSpace(format.Name) != "" || len(bytes.TrimSpace(format.JSONSchema)) != 0 || format.Strict {
			return fmt.Errorf("%w: json_object does not accept name, schema, or strict", ErrResponseFormatInvalid)
		}
		if !capabilities.SupportsJSONOutput {
			return fmt.Errorf("%w: provider %q does not support json_object", ErrResponseFormatUnsupported, model.Provider)
		}
		return nil
	case ResponseFormatJSONSchema:
		if !responseFormatNamePattern.MatchString(format.Name) {
			return fmt.Errorf("%w: json_schema name must match %s", ErrResponseFormatInvalid, responseFormatNamePattern)
		}
		if err := validateResponseJSONSchema(format.JSONSchema); err != nil {
			return err
		}
		if !capabilities.SupportsJSONSchema {
			return fmt.Errorf("%w: provider %q does not support json_schema", ErrResponseFormatUnsupported, model.Provider)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown type %q", ErrResponseFormatInvalid, format.Type)
	}
}

func validateResponseJSONSchema(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > maxResponseJSONSchemaBytes || trimmed[0] != '{' {
		return fmt.Errorf("%w: json_schema must be a JSON object", ErrResponseFormatInvalid)
	}
	if err := rejectDuplicateResponseJSONKeys(trimmed); err != nil {
		return fmt.Errorf("%w: %v", ErrResponseFormatInvalid, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	var schema map[string]any
	if err := decoder.Decode(&schema); err != nil {
		return fmt.Errorf("%w: %v", ErrResponseFormatInvalid, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: json_schema contains trailing data", ErrResponseFormatInvalid)
	}
	if len(schema) == 0 {
		return fmt.Errorf("%w: json_schema must not be empty", ErrResponseFormatInvalid)
	}
	return nil
}

func rejectDuplicateResponseJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > maxResponseJSONSchemaDepth {
			return fmt.Errorf("JSON exceeds depth %d", maxResponseJSONSchemaDepth)
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid JSON object key")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	return walk(0)
}

func cloneResponseFormat(format *ResponseFormat) *ResponseFormat {
	if format == nil {
		return nil
	}
	cloned := *format
	cloned.JSONSchema = append(json.RawMessage(nil), format.JSONSchema...)
	return &cloned
}
