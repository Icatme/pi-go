package strictschema_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/strictschema"
)

func schema(t *testing.T, raw string) map[string]any {
	t.Helper()
	var result map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

const nested = `{"type":"object","properties":{"name":{"type":"string","minLength":1},"note":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"items":{"type":"array","items":{"type":"object","properties":{"keep":{"type":"string"},"omit":{"type":"string"}},"required":["keep"],"additionalProperties":false}},"flag":{"type":"boolean"},"zero":{"type":"integer"},"explicit":{"type":"null"},"empty":{"type":"object","properties":{},"additionalProperties":false}},"required":["name","explicit"],"additionalProperties":false}`

func TestStrictSchemaToolRoundTrip(t *testing.T) {
	original := schema(t, nested)
	before, _ := json.Marshal(original)
	compiled, err := strictschema.New(original, strictschema.Options{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	tool := agent.ToolDefinition{Name: "write", Parameters: compiled.Parameters(), MaxArgumentBytes: 8192,
		ParseArguments: func(call agent.ToolCall) (any, error) { return call.Arguments, nil },
		Execute: func(_ context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
			raw, err := compiled.Normalize(in.Args.(json.RawMessage))
			if err != nil {
				return agent.ToolResult{}, err
			}
			if string(raw) != `{"empty":{},"explicit":null,"flag":false,"items":[{"keep":""}],"name":"keep","tags":[],"zero":0}` {
				t.Fatalf("omission/empty meanings changed: %s", raw)
			}
			calls++
			return agent.ToolResult{}, nil
		}}
	raw := json.RawMessage(`{"name":"keep","note":null,"tags":[],"items":[{"keep":"","omit":null}],"flag":false,"zero":0,"explicit":null,"empty":{}}`)
	out := agent.RunToolCall(t.Context(), agent.ToolCall{Name: tool.Name, Arguments: raw}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
	if out.Err != nil || calls != 1 {
		t.Fatal(out.Err, calls)
	}
	invalid := strings.Replace(string(raw), `"name":"keep"`, `"name":""`, 1)
	out = agent.RunToolCall(t.Context(), agent.ToolCall{Name: tool.Name, Arguments: json.RawMessage(invalid)}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
	if out.Err == nil || calls != 1 {
		t.Fatal("scalar constraints lost")
	}
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("source declaration mutated")
	}
	// Both source and returned maps are caller-owned; the omission plan is frozen.
	original["properties"] = map[string]any{}
	tool.Parameters["properties"] = map[string]any{}
	again, err := compiled.Normalize(raw)
	if err != nil || !strings.Contains(string(again), `"name":"keep"`) {
		t.Fatal("caller mutation changed compiled schema", err)
	}
	if len(compiled.Parameters()["properties"].(map[string]any)) != 8 {
		t.Fatal("returned parameters aliased internal state")
	}
}

func TestStrictSchemaRejectsAmbiguousOrUnsupportedShapes(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object"}`,
		`{"type":"object","properties":{},"additionalProperties":true}`,
		`{"type":"object","properties":{"v":{"type":"null"}},"additionalProperties":false}`,
		`{"type":"object","properties":{"v":{"type":["string","null"]}},"additionalProperties":false}`,
		`{"type":"object","properties":{"v":{"type":"string","anyOf":[{"minLength":1}]}},"additionalProperties":false}`,
		`{"type":"object","properties":{"v":{"$ref":"https://invalid.example/schema"}},"additionalProperties":false}`,
		`{"type":"object","properties":{},"additionalProperties":false,"required":["absent"]}`,
		`{"type":"object","properties":{"v":{"type":"string"}},"additionalProperties":false,"required":["v","v"]}`,
		`{"type":"object","properties":{},"additionalProperties":false,"minProperties":1}`,
		`{"type":"object","properties":{"v":{"type":"array","prefixItems":[{"type":"string"}]}},"additionalProperties":false}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := strictschema.New(schema(t, raw), strictschema.Options{}); err == nil {
				t.Fatal("unsupported semantics accepted")
			}
		})
	}
}

func TestStrictSchemaBoundsAndArgumentShape(t *testing.T) {
	source := schema(t, `{"type":"object","properties":{"v":{"type":"string"}},"additionalProperties":false}`)
	encoded, _ := json.Marshal(source)
	for _, options := range []strictschema.Options{{MaxSchemaBytes: -1}, {MaxArgumentBytes: -1}, {MaxDepth: -1}, {MaxSchemaBytes: len(encoded) - 1}, {MaxSchemaBytes: len(encoded)}} {
		if _, err := strictschema.New(source, options); err == nil {
			t.Fatal("invalid limit or expanded schema bound accepted", options)
		}
	}
	deep := schema(t, `{"type":"object","properties":{"a":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}`)
	if _, err := strictschema.New(deep, strictschema.Options{MaxDepth: 1}); err == nil {
		t.Fatal("depth exceeded")
	}
	compiled, err := strictschema.New(source, strictschema.Options{MaxArgumentBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{}`, `null`, `{"v":0}`, `{"v":"x","x":0}`, `{"v":"\ud800"}`, `{"v":null} {}`, `{"v":"123456789"}`, `{"v":"&&"}`} {
		if _, err := compiled.Normalize(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid, missing or oversized arguments accepted: %s", raw)
		}
	}
	if got, err := compiled.Normalize(json.RawMessage(`{"v":null}`)); err != nil || string(got) != `{}` {
		t.Fatal(string(got), err)
	}
	empty, err := strictschema.New(schema(t, `{"type":"object","properties":{},"additionalProperties":false}`), strictschema.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := empty.Parameters()["required"]; exists {
		t.Fatal("empty required array added")
	}
	if got, err := empty.Normalize(json.RawMessage(`{}`)); err != nil || string(got) != `{}` {
		t.Fatal(string(got), err)
	}
}

func TestStrictSchemaRetainsNumericLexemes(t *testing.T) {
	compiled, err := strictschema.New(schema(t, `{"type":"object","properties":{"id":{"type":"integer","minimum":9007199254740993},"ratio":{"type":"number"},"note":{"type":"string"}},"required":["id","ratio"],"additionalProperties":false}`), strictschema.Options{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := compiled.Normalize(json.RawMessage(`{"id":9007199254740993,"ratio":1.2300e-2,"note":null}`))
	if err != nil || string(raw) != `{"id":9007199254740993,"ratio":1.2300e-2}` {
		t.Fatal(string(raw), err)
	}
	declaration, _ := json.Marshal(compiled.Parameters())
	if !strings.Contains(string(declaration), `"minimum":9007199254740993`) {
		t.Fatal("schema constraint rounded", string(declaration))
	}
}

func TestStrictSchemaDefaultToolArguments(t *testing.T) {
	tests := []struct {
		name       string
		parameters string
		arguments  string
		want       string
	}{
		{
			name:       "omit null scalars",
			parameters: `{"type":"object","properties":{"text":{"type":"string"},"number":{"type":"number"},"integer":{"type":"integer"},"flag":{"type":"boolean"}},"additionalProperties":false}`,
			arguments:  `{"text":null,"number":null,"integer":null,"flag":null}`,
			want:       `{}`,
		},
		{
			name:       "keep empty and zero scalars",
			parameters: `{"type":"object","properties":{"text":{"type":"string"},"number":{"type":"number"},"integer":{"type":"integer"},"flag":{"type":"boolean"}},"additionalProperties":false}`,
			arguments:  `{"text":"","number":0,"integer":0,"flag":false}`,
			want:       `{"flag":false,"integer":0,"number":0,"text":""}`,
		},
		{
			name:       "nested omission and required null",
			parameters: `{"type":"object","properties":{"explicit":{"type":"null"},"items":{"type":"array","items":{"type":"object","properties":{"text":{"type":"string"},"flag":{"type":"boolean"}},"additionalProperties":false}},"nested":{"type":"object","properties":{"number":{"type":"number"}},"additionalProperties":false}},"required":["explicit"],"additionalProperties":false}`,
			arguments:  `{"explicit":null,"items":[{"text":null,"flag":null},{"text":"","flag":false}],"nested":{"number":null}}`,
			want:       `{"explicit":null,"items":[{},{"flag":false,"text":""}],"nested":{}}`,
		},
		{
			name:       "omit containers",
			parameters: `{"type":"object","properties":{"items":{"type":"array","items":{"type":"string"}},"nested":{"type":"object","properties":{},"additionalProperties":false}},"additionalProperties":false}`,
			arguments:  `{"items":null,"nested":null}`,
			want:       `{}`,
		},
		{
			name:       "keep empty containers",
			parameters: `{"type":"object","properties":{"items":{"type":"array","items":{"type":"string"}},"nested":{"type":"object","properties":{},"additionalProperties":false}},"additionalProperties":false}`,
			arguments:  `{"items":[],"nested":{}}`,
			want:       `{"items":[],"nested":{}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, parsed := range []bool{false, true} {
				prepared, err := strictschema.New(schema(t, tt.parameters), strictschema.Options{})
				if err != nil {
					t.Fatal(err)
				}
				var got json.RawMessage
				tool := agent.ToolDefinition{Name: "write", Parameters: prepared.Parameters(),
					Execute: func(_ context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
						raw, err := json.Marshal(in.Args)
						if err != nil {
							return agent.ToolResult{}, err
						}
						got, err = prepared.Normalize(raw)
						return agent.ToolResult{}, err
					}}
				call := agent.ToolCall{Name: tool.Name, Arguments: json.RawMessage(tt.arguments)}
				if parsed {
					call.ParsedArgs = schema(t, tt.arguments)
				}
				out := agent.RunToolCall(t.Context(), call, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
				if out.Err != nil || string(got) != tt.want {
					t.Errorf("parsed=%v: got %s, want %s; error: %v", parsed, got, tt.want, out.Err)
				}
			}
		})
	}
}

func TestStrictSchemaDefaultToolArgumentsRetainConstraints(t *testing.T) {
	for _, tt := range []struct {
		name    string
		field   string
		invalid string
		valid   string
	}{
		{"minLength", `{"type":"string","minLength":1}`, `""`, `"x"`},
		{"pattern", `{"type":"string","pattern":"^x$"}`, `""`, `"x"`},
		{"integer minimum", `{"type":"integer","minimum":1}`, `0`, `1`},
		{"number minimum", `{"type":"number","minimum":0.5}`, `0`, `1`},
		{"string enum", `{"type":"string","enum":["x"]}`, `""`, `"x"`},
		{"boolean enum", `{"type":"boolean","enum":[true]}`, `false`, `true`},
		{"const", `{"type":"string","const":"x"}`, `""`, `"x"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := schema(t, `{"type":"object","properties":{"value":`+tt.field+`},"additionalProperties":false}`)
			prepared, err := strictschema.New(original, strictschema.Options{})
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{tt.invalid, tt.valid, `null`} {
				calls, gates := 0, 0
				var got json.RawMessage
				tool := agent.ToolDefinition{Name: "write", Parameters: prepared.Parameters(),
					Execute: func(_ context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
						calls++
						raw, err := json.Marshal(in.Args)
						if err != nil {
							return agent.ToolResult{}, err
						}
						got, err = prepared.Normalize(raw)
						return agent.ToolResult{}, err
					}}
				out := agent.RunToolCall(t.Context(), agent.ToolCall{Name: tool.Name, Arguments: json.RawMessage(`{"value":` + value + `}`)}, agent.RunToolCallOptions{
					Tools: []agent.ToolDefinition{tool},
					ToolGate: func(context.Context, agent.BeforeToolCallContext) (agent.ToolGateResult, error) {
						gates++
						return agent.ToolGateResult{Action: agent.ToolGateActionAllow}, nil
					},
				})
				if value == tt.invalid {
					if out.Err == nil || calls != 0 || gates != 0 || out.Execution.Local != agent.ToolLocalNotStarted {
						t.Errorf("invalid %s reached approval/execution: calls=%d gates=%d outcome=%+v", value, calls, gates, out)
					}
					continue
				}
				want := `{"value":` + value + `}`
				if value == `null` {
					want = `{}`
				}
				if out.Err != nil || calls != 1 || string(got) != want {
					t.Errorf("valid %s changed: got %s, want %s; error: %v", value, got, want, out.Err)
				}
			}
		})
	}
}
