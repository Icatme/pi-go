package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestToolLocalReferenceIntegerProjection(t *testing.T) {
	for _, id := range []string{"", "https://example.invalid/tool.json"} {
		for _, tc := range []struct {
			raw   string
			valid bool
			bound string
			limit any
		}{
			{"2", true, "", nil}, {"3", true, "", nil}, {"2.0", true, "", nil}, {"200e-2", true, "", nil},
			{"9007199254740993", true, "", nil}, {"9007199254740993e0", true, "", nil},
			{"18446744073709551615", true, "", nil}, {"18446744073709551615e0", true, "", nil},
			{"18446744073709551616", false, "", nil}, {"1e1000000000", false, "", nil}, {"2.5", false, "", nil}, {`"2"`, false, "", nil},
			{"9007199254740993", false, "maximum", float64(9007199254740992)},
			{"9007199254740993e0", false, "maximum", float64(9007199254740992)},
			{"9007199254740991", false, "minimum", float64(9007199254740992)},
			{"9007199254740991e0", false, "minimum", float64(9007199254740992)},
			{"-9223372036854775808", true, "", nil}, {"-9223372036854775809", false, "", nil},
		} {
			t.Run(id+"/"+tc.raw+tc.bound, func(t *testing.T) {
				def := map[string]any{"type": "integer"}
				if tc.bound != "" {
					def[tc.bound] = tc.limit
				}
				schema := map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"$ref": "#/$defs/count"}}, "required": []any{"count"}, "$defs": map[string]any{"count": def}}
				if id != "" {
					schema["$id"] = id
				}
				raw := json.RawMessage(`{"count":` + tc.raw + `}`)
				called := false
				tool := ToolDefinition{Name: "counter", Parameters: schema, Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
					called = true
					if got := in.Args.(map[string]any)["count"]; !reflect.DeepEqual(got, json.Number(tc.raw)) {
						t.Fatalf("execution value changed: %#v", got)
					}
					return ToolResult{}, nil
				}}
				out := RunToolCall(t.Context(), ToolCall{Name: "counter", Arguments: raw}, RunToolCallOptions{Tools: []ToolDefinition{tool}})
				if called != tc.valid || out.IsError == tc.valid {
					t.Fatalf("called=%v outcome=%+v", called, out)
				}
			})
		}
	}
}
func TestExactSchemaIntegerProjectionDetached(t *testing.T) {
	v := map[string]any{"nested": []any{json.Number("9007199254740993e0"), json.Number("0.1"), "2"}}
	out := projectExactSchemaIntegers(v).(map[string]any)
	a := out["nested"].([]any)
	if a[0] != int64(9007199254740993) || a[1] != json.Number("0.1") || a[2] != "2" {
		t.Fatal(out)
	}
	a[0] = int64(1)
	if v["nested"].([]any)[0] != json.Number("9007199254740993e0") {
		t.Fatal("aliased input")
	}
}

func TestExactSchemaIntegerProjectionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want any
	}{
		{"-0", int64(0)}, {"1.20e1", int64(12)}, {"120e-1", int64(12)}, {"1.2", json.Number("1.2")},
		{"-9223372036854775808e0", int64(-9223372036854775808)}, {"18446744073709551616", json.Number("18446744073709551616")},
		{"0e999999999", int64(0)}, {"1e-999999999", json.Number("1e-999999999")},
		{"1e999999999999999999999999", json.Number("1e999999999999999999999999")},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := projectExactSchemaIntegers(json.Number(tc.raw)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%#v want=%#v", got, tc.want)
			}
		})
	}
}

func TestToolLocalReferenceStringRejectsNumber(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"v": map[string]any{"$ref": "#/$defs/v"}}, "$defs": map[string]any{"v": map[string]any{"type": "string"}}}
	for _, raw := range []string{`{"v":2}`, `{"v":"2"}`} {
		called := false
		tool := ToolDefinition{Name: "string", Parameters: schema, Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
			called = true
			return ToolResult{}, nil
		}}
		out := RunToolCall(t.Context(), ToolCall{Name: "string", Arguments: json.RawMessage(raw)}, RunToolCallOptions{Tools: []ToolDefinition{tool}})
		want := raw == `{"v":"2"}`
		if called != want || out.IsError == want {
			t.Fatalf("%s called=%v err=%v", raw, called, out.Err)
		}
	}
}

func TestToolOutputExactIntegerValidation(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{"9007199254740992e0", true}, {"9007199254740993e0", false}, {"18446744073709551617", false}, {"0.1", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			raw := json.RawMessage(`{"count":` + tc.raw + `}`)
			tool := ToolDefinition{Name: "output", OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "number", "maximum": float64(9007199254740992)}}}, Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
				return ToolResult{StructuredContent: raw}, nil
			}}
			var out ToolCallOutcome
			parent := ToolDefinition{Name: "container", ChildTools: []ToolDefinition{tool}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
				out = in.ChildCaller.Call(ctx, ToolCall{Name: "output"})
				return ToolResult{}, nil
			}}
			parentOut := RunToolCall(t.Context(), ToolCall{Name: "container"}, RunToolCallOptions{Tools: []ToolDefinition{parent}})
			if parentOut.Err != nil {
				t.Fatal(parentOut.Err)
			}
			if out.IsError == tc.valid {
				t.Fatalf("out=%+v", out)
			}
			if tc.valid && string(out.Result.StructuredContent) != string(raw) {
				t.Fatal("output rewritten")
			}
		})
	}
}
