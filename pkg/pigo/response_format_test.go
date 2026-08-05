package pigo

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidateResponseFormatRejectsInvalidSchemaBeforeRequest(t *testing.T) {
	model := Model{Provider: "openai", API: "openai-responses", ID: "test"}
	for name, schema := range map[string]string{
		"syntax":     `{"type":`,
		"not-object": `[]`,
		"duplicate":  `{"type":"object","type":"object"}`,
		"trailing":   `{"type":"object"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			format := &ResponseFormat{Type: ResponseFormatJSONSchema, Name: "result", JSONSchema: json.RawMessage(schema), Strict: true}
			if err := ValidateResponseFormat(model, format); !errors.Is(err, ErrResponseFormatInvalid) {
				t.Fatalf("error = %v, want ErrResponseFormatInvalid", err)
			}
		})
	}
}

func TestDeepSeekMapsJSONOutputAndRejectsJSONSchema(t *testing.T) {
	model := Model{Provider: "deepseek", API: "deepseek-chat-completions", ID: "deepseek-v4-pro"}
	jsonObject := &ResponseFormat{Type: ResponseFormatJSON}
	if err := ValidateResponseFormat(model, jsonObject); err != nil {
		t.Fatal(err)
	}
	request := buildDeepSeekChatRequest(model, Context{}, ProviderStreamOptions{ResponseFormat: jsonObject})
	if request.ResponseFormat == nil || request.ResponseFormat.Type != "json_object" {
		t.Fatalf("response format = %#v", request.ResponseFormat)
	}

	jsonSchema := &ResponseFormat{
		Type: ResponseFormatJSONSchema, Name: "result", Strict: true,
		JSONSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
	if err := ValidateResponseFormat(model, jsonSchema); !errors.Is(err, ErrResponseFormatUnsupported) {
		t.Fatalf("error = %v, want ErrResponseFormatUnsupported", err)
	}
	response := Complete(model, Context{}, ProviderStreamOptions{ResponseFormat: jsonSchema})
	if response.StopReason != StopReasonError || !strings.Contains(response.ErrorMessage, ErrResponseFormatUnsupported.Error()) {
		t.Fatalf("response = %#v", response)
	}
}

func TestOpenAIResponsesMapsJSONSchema(t *testing.T) {
	model := Model{Provider: "openai", API: "openai-responses", ID: "gpt-5.4-mini"}
	format := &ResponseFormat{
		Type: ResponseFormatJSONSchema, Name: "lead_analysis", Strict: true,
		JSONSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["qualified"],"properties":{"qualified":{"type":"boolean"}}}`),
	}
	if err := ValidateResponseFormat(model, format); err != nil {
		t.Fatal(err)
	}
	request := buildOpenAIResponsesRequest(model, Context{}, ProviderStreamOptions{ResponseFormat: format})
	if request.Text == nil || request.Text.Format == nil {
		t.Fatal("missing text.format")
	}
	if got := request.Text.Format; got.Type != "json_schema" || got.Name != "lead_analysis" || !got.Strict || string(got.Schema) != string(format.JSONSchema) {
		t.Fatalf("format = %#v", got)
	}
}

func TestUnsupportedProviderRejectsResponseFormat(t *testing.T) {
	model := Model{Provider: "anthropic", API: "anthropic-messages", ID: "test"}
	if err := ValidateResponseFormat(model, &ResponseFormat{Type: ResponseFormatJSON}); !errors.Is(err, ErrResponseFormatUnsupported) {
		t.Fatalf("error = %v, want ErrResponseFormatUnsupported", err)
	}
}

func TestResponseFormatIsClonedAcrossOptionConversions(t *testing.T) {
	format := &ResponseFormat{
		Type: ResponseFormatJSONSchema, Name: "result", Strict: true,
		JSONSchema: json.RawMessage(`{"type":"object"}`),
	}
	converted := streamOptionsFromProvider(Model{}, ProviderStreamOptions{ResponseFormat: format}).providerStreamOptions(Model{})
	format.JSONSchema[2] = 'X'
	if string(converted.ResponseFormat.JSONSchema) != `{"type":"object"}` {
		t.Fatalf("converted schema mutated: %s", converted.ResponseFormat.JSONSchema)
	}
}

func TestSimpleOptionsCarryAndValidateResponseFormat(t *testing.T) {
	model := Model{Provider: "openai", API: "openai-responses", ID: "test"}
	format := &ResponseFormat{Type: ResponseFormatJSONSchema, Name: "result", Strict: true, JSONSchema: json.RawMessage(`{"type":"object"}`)}
	converted := BuildProviderStreamOptions(model, SimpleStreamOptions{ResponseFormat: format})
	if converted.ResponseFormat == nil || converted.ResponseFormat.Name != "result" {
		t.Fatalf("converted response format = %#v", converted.ResponseFormat)
	}
	format.JSONSchema[2] = 'X'
	if string(converted.ResponseFormat.JSONSchema) != `{"type":"object"}` {
		t.Fatalf("converted schema mutated: %s", converted.ResponseFormat.JSONSchema)
	}

	unsupported := Model{Provider: "anthropic", API: "anthropic-messages", ID: "test"}
	response := CompleteSimple(unsupported, Context{}, SimpleStreamOptions{ResponseFormat: &ResponseFormat{Type: ResponseFormatJSON}})
	if response.StopReason != StopReasonError || !strings.Contains(response.ErrorMessage, ErrResponseFormatUnsupported.Error()) {
		t.Fatalf("response = %#v", response)
	}
}

func TestResponseFormatOptionClonesSchema(t *testing.T) {
	format := ResponseFormat{Type: ResponseFormatJSONSchema, Name: "result", Strict: true, JSONSchema: json.RawMessage(`{"type":"object"}`)}
	options := NewStreamOptions(WithResponseFormat(format))
	format.JSONSchema[2] = 'X'
	if options.ResponseFormat == nil || string(options.ResponseFormat.JSONSchema) != `{"type":"object"}` {
		t.Fatalf("options response format = %#v", options.ResponseFormat)
	}
}
