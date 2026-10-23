// Package strictschema prepares closed tool schemas for transports that require
// every property to be present. Optional properties use null to mean omission.
// It does not enable provider strict mode or replace JSON Schema validation.
package strictschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Icatme/pi-go/internal/jsontext"
)

// Options bounds both source and converted schemas, argument JSON and nesting.
// Zero selects defaults of 64 KiB, 64 KiB and 32 levels respectively.
type Options struct {
	MaxSchemaBytes   int
	MaxArgumentBytes int
	MaxDepth         int
}

// Schema owns detached declarations and an immutable omission plan. It is safe
// to share across calls. Only closed objects, homogeneous arrays and scalar
// types are supported; references, unions and conditional schemas are rejected.
type Schema struct {
	parameters    []byte
	root          *node
	argumentLimit int
}

type node struct {
	kind       string
	properties map[string]*node
	required   map[string]bool
	items      *node
}

// New validates the supported shape and returns its required/nullable form.
// Constraints and annotations are preserved, not weakened or silently removed.
// An optional explicit-null field is rejected because null cannot represent
// both omission and a distinct business value. No schema URLs are fetched.
func New(parameters map[string]any, options Options) (*Schema, error) {
	limits := []*int{&options.MaxSchemaBytes, &options.MaxArgumentBytes, &options.MaxDepth}
	defaults := []int{64 << 10, 64 << 10, 32}
	for i, limit := range limits {
		if *limit < 0 {
			return nil, fmt.Errorf("strictschema: negative limit")
		}
		if *limit == 0 {
			*limit = defaults[i]
		}
	}
	if err := jsontext.ValidateStrings(parameters); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(parameters)
	if err != nil {
		return nil, fmt.Errorf("strictschema: encode schema: %w", err)
	}
	if len(raw) > options.MaxSchemaBytes {
		return nil, fmt.Errorf("strictschema: source schema exceeds byte limit")
	}
	var detached map[string]any
	if err := decode(raw, &detached); err != nil {
		return nil, err
	}
	if detached["type"] != "object" {
		return nil, fmt.Errorf("strictschema: root must be an object")
	}
	root, err := convert(detached, 0, options.MaxDepth)
	if err != nil {
		return nil, err
	}
	raw, err = json.Marshal(detached)
	if err != nil {
		return nil, err
	}
	if len(raw) > options.MaxSchemaBytes {
		return nil, fmt.Errorf("strictschema: converted schema exceeds byte limit")
	}
	return &Schema{parameters: raw, root: root, argumentLimit: options.MaxArgumentBytes}, nil
}

// Parameters returns a detached declaration for the model-facing validator and
// transport. Changes to it do not change future declarations or normalization.
func (s *Schema) Parameters() map[string]any {
	var result map[string]any
	_ = decode(s.parameters, &result) // New produced valid JSON.
	return result
}

// Normalize removes only nulls representing optional property omission, including
// inside arrays. Empty strings/arrays/objects, false, zero and required nulls
// retain their meanings. Call it after validating against Parameters and before
// executing the original tool. This checks shape, not scalar constraints such
// as enum, pattern or minimum; keep the original tool's business validation.
// Numeric JSON lexemes survive without a float64 round trip.
func (s *Schema) Normalize(arguments json.RawMessage) (json.RawMessage, error) {
	if s == nil || s.root == nil {
		return nil, fmt.Errorf("strictschema: uninitialized schema")
	}
	if len(arguments) > s.argumentLimit {
		return nil, fmt.Errorf("strictschema: arguments exceed byte limit")
	}
	if err := jsontext.ValidateUnicode(arguments); err != nil {
		return nil, err
	}
	var value any
	if err := decode(arguments, &value); err != nil {
		return nil, err
	}
	if err := s.root.normalize(value); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > s.argumentLimit {
		return nil, fmt.Errorf("strictschema: normalized arguments exceed byte limit")
	}
	return raw, nil
}

func decode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d.Decode(out)
}

func convert(schema map[string]any, depth, limit int) (*node, error) {
	if depth > limit {
		return nil, fmt.Errorf("strictschema: schema exceeds depth limit")
	}
	kind, ok := schema["type"].(string)
	if !ok {
		return nil, fmt.Errorf("strictschema: one explicit type is required")
	}
	for key := range schema {
		if !supportedKeyword(kind, key) {
			return nil, fmt.Errorf("strictschema: unsupported %q keyword for %s", key, kind)
		}
	}
	n := &node{kind: kind}
	switch kind {
	case "object":
		properties, ok := schema["properties"].(map[string]any)
		if !ok || schema["additionalProperties"] != false {
			return nil, fmt.Errorf("strictschema: objects must declare properties and additionalProperties=false")
		}
		n.required = map[string]bool{}
		if raw, exists := schema["required"]; exists {
			names, ok := raw.([]any)
			if !ok {
				return nil, fmt.Errorf("strictschema: required must be an array")
			}
			for _, name := range names {
				key, ok := name.(string)
				if !ok || n.required[key] {
					return nil, fmt.Errorf("strictschema: invalid or duplicate required property")
				}
				if _, exists := properties[key]; !exists {
					return nil, fmt.Errorf("strictschema: required property %q is undeclared", key)
				}
				n.required[key] = true
			}
		}
		n.properties = make(map[string]*node, len(properties))
		names := make([]string, 0, len(properties))
		for name, value := range properties {
			child, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("strictschema: property %q is not a schema object", name)
			}
			plan, err := convert(child, depth+1, limit)
			if err != nil {
				return nil, fmt.Errorf("strictschema: property %q: %w", name, err)
			}
			n.properties[name] = plan
			if !n.required[name] {
				if plan.kind == "null" {
					return nil, fmt.Errorf("strictschema: optional null property %q has ambiguous omission", name)
				}
				properties[name] = map[string]any{"anyOf": []any{child, map[string]any{"type": "null"}}}
			}
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			delete(schema, "required")
		} else {
			schema["required"] = names
		}
	case "array":
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("strictschema: arrays require one items schema")
		}
		var err error
		n.items, err = convert(items, depth+1, limit)
		if err != nil {
			return nil, err
		}
	case "string", "number", "integer", "boolean", "null":
	default:
		return nil, fmt.Errorf("strictschema: unsupported type %q", kind)
	}
	return n, nil
}

func supportedKeyword(kind, key string) bool {
	switch key {
	case "type", "title", "description", "default", "examples", "deprecated", "readOnly", "writeOnly":
		return true
	}
	switch kind {
	case "object":
		return key == "properties" || key == "required" || key == "additionalProperties"
	case "array":
		return key == "items" || key == "minItems" || key == "maxItems" || key == "uniqueItems"
	default:
		if key == "enum" || key == "const" {
			return true
		}
	}
	switch kind {
	case "string":
		return key == "format" || key == "pattern" || key == "minLength" || key == "maxLength"
	case "number", "integer":
		return key == "minimum" || key == "maximum" || key == "exclusiveMinimum" || key == "exclusiveMaximum" || key == "multipleOf"
	}
	return false
}

func (n *node) normalize(value any) error {
	switch n.kind {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("strictschema: expected object")
		}
		for name := range object {
			if _, ok := n.properties[name]; !ok {
				return fmt.Errorf("strictschema: undeclared property %q", name)
			}
		}
		for name, child := range n.properties {
			value, exists := object[name]
			if !exists {
				return fmt.Errorf("strictschema: wire property %q is missing; use null for omission", name)
			}
			if value == nil && !n.required[name] {
				delete(object, name)
				continue
			}
			if err := child.normalize(value); err != nil {
				return fmt.Errorf("strictschema: property %q: %w", name, err)
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("strictschema: expected array")
		}
		for _, item := range items {
			if err := n.items.normalize(item); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("strictschema: expected string")
		}
	case "number", "integer":
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("strictschema: expected number")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("strictschema: expected boolean")
		}
	case "null":
		if value != nil {
			return fmt.Errorf("strictschema: expected null")
		}
	}
	return nil
}
