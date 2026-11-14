package pigo

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestGoogleParametersJSONSchemaOptIn(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name string
		flag *bool
	}{{"nil", nil}, {"false", &no}, {"true", &yes}} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", tc.name, empty), func(t *testing.T) {
				var schema any = map[string]any{"type": "object", "$defs": map[string]any{"choice": map[string]any{"anyOf": []any{map[string]any{"const": "foo"}, map[string]any{"const": "bar"}}}}, "properties": map[string]any{"choice": map[string]any{"$ref": "#/$defs/choice"}}, "additionalProperties": false}
				if empty {
					schema = nil
				}
				ctx := Context{Tools: []Tool{{Name: "pick", Description: "Choose", Parameters: schema}}}
				before, _ := json.Marshal(ctx.Tools)
				count := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					count++
					raw, _ := io.ReadAll(r.Body)
					var wire struct {
						Tools []struct {
							Declarations []map[string]json.RawMessage `json:"functionDeclarations"`
						} `json:"tools"`
					}
					if err := json.Unmarshal(raw, &wire); err != nil {
						t.Error(err)
						return
					}
					d := wire.Tools[0].Declarations[0]
					field, other := "parameters", "parametersJsonSchema"
					if tc.flag != nil && *tc.flag {
						field, other = other, field
					}
					if _, ok := d[other]; ok {
						t.Errorf("both/wrong schema fields: %s", raw)
					}
					want := schema
					if want == nil {
						want = map[string]any{"type": "object", "properties": map[string]any{}}
					}
					expected, _ := json.Marshal(want)
					if string(d[field]) != string(expected) {
						t.Errorf("schema changed: %s want %s", d[field], expected)
					}
					if string(d["name"]) != `"pick"` || string(d["description"]) != `"Choose"` {
						t.Error("identity changed")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")
				}))
				defer server.Close()
				result := Complete(Model{ID: "fixture", Provider: "google", API: "google-generative-ai", BaseURL: server.URL, Compat: &GoogleGenerativeAICompat{SupportsParametersJSONSchema: tc.flag}}, ctx, ProviderStreamOptions{APIKey: "fixture"})
				if count != 1 || result.StopReason != StopReasonStop {
					t.Fatalf("count%d result%+v", count, result)
				}
				after, _ := json.Marshal(ctx.Tools)
				if string(before) != string(after) {
					t.Fatal("mutated schema")
				}
			})
		}
	}
}
func TestGoogleParametersJSONSchemaClone(t *testing.T) {
	yes := true
	original := &GoogleGenerativeAICompat{SupportsParametersJSONSchema: &yes}
	copied := cloneCompat(original).(*GoogleGenerativeAICompat)
	if !reflect.DeepEqual(original, copied) || copied.SupportsParametersJSONSchema == original.SupportsParametersJSONSchema {
		t.Fatal("not isolated")
	}
	*copied.SupportsParametersJSONSchema = false
	if !yes {
		t.Fatal("mutated original")
	}
}

func TestGoogleParametersJSONSchemaNoToolsAndNoCompat(t *testing.T) {
	yes := true
	for _, compat := range []ProviderCompat{nil, &GoogleGenerativeAICompat{SupportsParametersJSONSchema: &yes}} {
		req, err := buildGoogleRequest(Model{API: "google-generative-ai", Provider: "google", Compat: compat}, Context{}, ProviderStreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(req.Tools) != 0 {
			t.Fatal("invented tools")
		}
	}
	legacy := convertGoogleTools(Model{}, []Tool{{Name: "plain"}})[0].FunctionDeclarations[0]
	if legacy.Parameters == nil || legacy.ParametersJSONSchema != nil {
		t.Fatal("nil compat changed")
	}
}
