package pigo

import (
	"reflect"
	"testing"
)

func TestValidationUnionNullPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name           string
		arms           []any
		anyErr, oneErr bool
	}{
		{"number null", []any{map[string]any{"type": "number"}, map[string]any{"type": "null"}}, false, false},
		{"string null", []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}, false, false},
		{"boolean null", []any{map[string]any{"type": "boolean"}, map[string]any{"type": "null"}}, false, false},
		{"native overlap", []any{map[string]any{"type": "null"}, map[string]any{}}, false, true},
		{"const null", []any{map[string]any{"type": "number"}, map[string]any{"const": nil}}, false, false},
		{"enum null", []any{map[string]any{"type": "number"}, map[string]any{"enum": []any{nil}}}, false, false},
		{"constraint mismatch", []any{map[string]any{"type": "null", "const": "x"}, map[string]any{"type": "null", "enum": []any{"x"}}}, true, true},
		{"nested union", []any{map[string]any{"type": "number"}, map[string]any{"anyOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "string"}}}}, false, false},
		{"type union", []any{map[string]any{"type": "number"}, map[string]any{"type": []any{"string", "null"}}}, false, false},
		{"allOf", []any{map[string]any{"type": "number"}, map[string]any{"allOf": []any{map[string]any{"type": "null"}}}}, false, false},
	} {
		for _, union := range []string{"anyOf", "oneOf"} {
			t.Run(tc.name+"/"+union, func(t *testing.T) {
				wantErr := tc.anyErr
				if union == "oneOf" {
					wantErr = tc.oneErr
				}
				got, errs := validateSchemaValue(map[string]any{union: tc.arms}, nil, "")
				if (len(errs) > 0) != wantErr || (!wantErr && got != nil) {
					t.Fatalf("got=%#v errors=%v", got, errs)
				}
			})
		}
	}
}
func TestValidationUnionNullNestedContainers(t *testing.T) {
	nullable := map[string]any{"anyOf": []any{map[string]any{"type": "number"}, map[string]any{"type": "null"}}}
	for _, tc := range []struct {
		name   string
		schema map[string]any
		value  any
	}{
		{"property", map[string]any{"type": "object", "properties": map[string]any{"n": nullable}}, map[string]any{"n": nil}},
		{"additional", map[string]any{"type": "object", "additionalProperties": nullable}, map[string]any{"n": nil}},
		{"array", map[string]any{"type": "array", "items": nullable}, []any{nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, errs := validateSchemaValue(tc.schema, tc.value, "")
			if len(errs) > 0 || !reflect.DeepEqual(got, tc.value) {
				t.Fatalf("got=%#v value=%#v errors=%v", got, tc.value, errs)
			}
		})
	}
}
func TestValidationUnionNonNullCoercionUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name        string
		schema      map[string]any
		value, want any
		wantErr     bool
	}{
		{"ordered string", map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "number"}}}, 42, "42", false},
		{"ordered null zero", map[string]any{"anyOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "number"}}}, 0, nil, false},
		{"ordered null false", map[string]any{"anyOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "boolean"}}}, false, nil, false},
		{"empty fallback", map[string]any{"oneOf": []any{map[string]any{"type": "number"}, map[string]any{"type": "null"}}}, "", nil, false},
		{"oneOf overlap", map[string]any{"oneOf": []any{map[string]any{"type": "number"}, map[string]any{"type": "integer"}}}, 42, nil, true},
		{"no nullable arm ambiguous", map[string]any{"oneOf": []any{map[string]any{"type": "number"}, map[string]any{"type": "string"}}}, nil, nil, true},
		{"no nullable arm", map[string]any{"anyOf": []any{map[string]any{"type": "number"}}}, nil, float64(0), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, errs := validateSchemaValue(tc.schema, tc.value, "")
			if (len(errs) > 0) != tc.wantErr || (!tc.wantErr && !reflect.DeepEqual(got, tc.want)) {
				t.Fatalf("got=%#v want=%#v errors=%v", got, tc.want, errs)
			}
		})
	}
}
