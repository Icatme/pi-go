package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicCacheDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"diagnostics\":{\"cache_miss_reason\":{\"type\":\"system_changed\",\"cache_missed_input_tokens\":9007199254740993}},\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	result := Complete(Model{ID: "fixture", Provider: "anthropic", API: "anthropic-messages", BaseURL: server.URL}, Context{}, ProviderStreamOptions{APIKey: "fixture"})
	if result.StopReason != StopReasonStop || len(result.Diagnostics) != 1 {
		t.Fatalf("result=%+v", result)
	}
	data, _ := json.Marshal(result.Diagnostics)
	if !strings.Contains(string(data), `"cache_missed_input_tokens":9007199254740993`) {
		t.Fatal("missing diagnostics")
	}
	reason := result.Diagnostics[0].Details["diagnostics"].(map[string]any)["cache_miss_reason"].(map[string]any)
	if reason["cache_missed_input_tokens"] != json.Number("9007199254740993") {
		t.Fatal(reason)
	}
	if result.Usage.Input != 3 || result.Usage.Output != 2 {
		t.Fatal(result.Usage)
	}
}

func TestAnthropicCacheDiagnosticsShapes(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      bool
	}{
		{"missing", "", false}, {"null", "null", true}, {"pending", `{"cache_miss_reason":null}`, true},
		{"unknown_type", `{"cache_miss_reason":{"type":"future_reason","extra":"SENSITIVE","cache_missed_input_tokens":42},"other":"SENSITIVE"}`, true},
		{"invalid", `[]`, false}, {"missing_reason", `{}`, false}, {"string_reason", `{"cache_miss_reason":"bad"}`, false},
		{"invalid_count", `{"cache_miss_reason":{"type":"system_changed","cache_missed_input_tokens":-1}}`, false},

		{"fraction", `{"cache_miss_reason":{"type":"system_changed","cache_missed_input_tokens":1.5}}`, false},
		{"exponent", `{"cache_miss_reason":{"type":"system_changed","cache_missed_input_tokens":1e2}}`, false},
		{"string_count", `{"cache_miss_reason":{"type":"system_changed","cache_missed_input_tokens":"42"}}`, false},
		{"null_count", `{"cache_miss_reason":{"type":"system_changed","cache_missed_input_tokens":null}}`, false},
		{"long_type", `{"cache_miss_reason":{"type":"` + strings.Repeat("x", 129) + `"}}`, false},
		{"oversize", `{"cache_miss_reason":null,"extra":"` + strings.Repeat("x", 4096) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m AssistantMessage
			appendAnthropicCacheDiagnostics(&m, json.RawMessage(tc.raw))
			if (len(m.Diagnostics) == 1) != tc.want {
				t.Fatalf("%+v", m.Diagnostics)
			}
			if !tc.want {
				return
			}
			raw, err := json.Marshal(m.Diagnostics)
			if err != nil || strings.Contains(string(raw), "SENSITIVE") {
				t.Fatalf("leaked/invalid: %s %v", raw, err)
			}
			if tc.name == "null" && m.Diagnostics[0].Details["diagnostics"] != nil {
				t.Fatal("null lost")
			}
			if tc.name == "pending" && m.Diagnostics[0].Details["diagnostics"].(map[string]any)["cache_miss_reason"] != nil {
				t.Fatal("pending lost")
			}
		})
	}
	var m AssistantMessage
	appendAnthropicCacheDiagnostics(&m, []byte(`{"cache_miss_reason":{"type":"system_changed"}}`))
	old := m.Diagnostics
	appendAnthropicCacheDiagnostics(&m, []byte(`{"cache_miss_reason":null}`))
	if len(m.Diagnostics) != 1 || old[0].Details["diagnostics"].(map[string]any)["cache_miss_reason"] == nil {
		t.Fatal("old observation changed or duplicate")
	}
}

func TestAnthropicCacheDiagnosticsContextRoundTrip(t *testing.T) {
	for _, raw := range []string{`null`, `{"cache_miss_reason":null}`, `{"cache_miss_reason":{"type":"system_changed","cache_missed_input_tokens":9007199254740993}}`} {
		var m AssistantMessage
		appendAnthropicCacheDiagnostics(&m, []byte(raw))
		data, err := SerializeContext(Context{Messages: []Message{m}})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := DeserializeContext(data)
		if err != nil {
			t.Fatal(err)
		}
		again, err := SerializeContext(restored)
		if err != nil || string(data) != string(again) {
			t.Fatalf("not exact: %s vs %s (%v)", data, again, err)
		}
	}
	for _, kind := range []string{"anthropic_cache_diagnostics", "legacy", ""} {
		for _, details := range []string{`null`, `{}`, `{"value":42}`} {
			var d assistantMessageDiagnosticJSON
			if err := json.Unmarshal([]byte(fmt.Sprintf(`{"type":%q,"details":%s}`, kind, details)), &d); err != nil {
				t.Fatal(err)
			}
			if details == `{"value":42}` {
				if kind == "anthropic_cache_diagnostics" {
					if d.Details["value"] != json.Number("42") {
						t.Fatal(d)
					}
				} else if d.Details["value"] != float64(42) {
					t.Fatal("changed legacy numbers", d)
				}
			}
		}
	}
	var missing assistantMessageDiagnosticJSON
	if err := json.Unmarshal([]byte(`{"type":"anthropic_cache_diagnostics"}`), &missing); err != nil || missing.Details != nil {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
}
