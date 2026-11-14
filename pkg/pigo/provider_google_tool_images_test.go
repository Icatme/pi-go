package pigo

import (
	"encoding/json"
	"testing"
)

func TestGoogleToolImagesStayWithParallelFunctionResponses(t *testing.T) {
	enabled := true
	model := Model{Compat: &GoogleGenerativeAICompat{SupportsMultimodalFunctionResponses: &enabled}, ID: "gemini-3.1-pro-preview", API: "google-generative-ai", Provider: "google", Input: []InputType{InputText, InputImage}}
	ctx := Context{Messages: []Message{UserMessage{Content: "compare"}, AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID, StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "call_a", Name: "photo", Arguments: map[string]any{}}, ToolCall{ID: "call_b", Name: "photo", Arguments: map[string]any{}}}}, ToolResultMessage{ToolCallID: "call_a", ToolName: "photo", Content: []ContentBlock{TextContent{Text: "A"}, ImageContent{MIMEType: "image/png", Data: "YQ=="}}}, ToolResultMessage{ToolCallID: "call_b", ToolName: "photo", Content: []ContentBlock{TextContent{Text: "B"}, ImageContent{MIMEType: "image/jpeg", Data: "Yg=="}}}}}
	req, err := buildGoogleRequest(model, ctx, ProviderStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	json.Unmarshal(raw, &wire)
	contents := wire["contents"].([]any)
	parts := contents[len(contents)-1].(map[string]any)["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("images detached from results: %s", raw)
	}
	for i, want := range []string{"YQ==", "Yg=="} {
		fr := parts[i].(map[string]any)["functionResponse"].(map[string]any)
		if fr["id"] != []string{"call_a", "call_b"}[i] || fr["name"] != "photo" || fr["response"].(map[string]any)["output"] != []string{"A", "B"}[i] {
			t.Fatalf("identity/text lost: %s", raw)
		}
		nested, ok := fr["parts"].([]any)
		if !ok || len(nested) != 1 {
			t.Fatalf("missing scoped media: %s", raw)
		}
		media := nested[0].(map[string]any)["inlineData"].(map[string]any)
		if media["mimeType"] != []string{"image/png", "image/jpeg"}[i] {
			t.Fatalf("MIME changed: %s", raw)
		}
		data := media["data"]
		if data != want {
			t.Fatalf("wrong owner image %d: %s", i, raw)
		}
	}
}

func TestGoogleToolImageCompatibilityAndCopies(t *testing.T) {
	enabled, disabled := true, false
	for _, tc := range []struct {
		name           string
		flag           *bool
		supportsImages bool
		nested         int
		siblings       int
	}{
		{"legacy nil", nil, true, 0, 3}, {"explicit legacy", &disabled, true, 0, 3}, {"nested", &enabled, true, 2, 1}, {"text only model", &enabled, false, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := Model{API: "google-generative-ai", Compat: &GoogleGenerativeAICompat{SupportsMultimodalFunctionResponses: tc.flag}}
			if tc.supportsImages {
				model.Input = []InputType{InputImage}
			}
			message := ToolResultMessage{ToolCallID: "call_a", ToolName: "photo", IsError: true, Content: []ContentBlock{TextContent{Text: "failure with evidence"}, ImageContent{MIMEType: "image/png", Data: "YQ=="}, ImageContent{MIMEType: "image/webp", Data: "Yg=="}}}
			before, _ := json.Marshal(message)
			parts := convertGoogleToolResultContent(model, message)
			if len(parts) != tc.siblings || len(parts[0].FunctionResponse.Parts) != tc.nested || parts[0].FunctionResponse.ID != "call_a" || parts[0].FunctionResponse.Name != "photo" || parts[0].FunctionResponse.Response["error"] != "failure with evidence" {
				t.Fatalf("bad shape: %+v", parts)
			}
			if tc.nested == 2 && (parts[0].FunctionResponse.Parts[0].InlineData.Data != "YQ==" || parts[0].FunctionResponse.Parts[1].InlineData.Data != "Yg==") {
				t.Fatal("media order changed")
			}
			after, _ := json.Marshal(message)
			if string(before) != string(after) {
				t.Fatal("history mutated")
			}
			copied := cloneCompat(model.Compat).(*GoogleGenerativeAICompat)
			if tc.flag == nil {
				if copied.SupportsMultimodalFunctionResponses != nil {
					t.Fatal("nil changed")
				}
			} else {
				*copied.SupportsMultimodalFunctionResponses = !*tc.flag
				if *copied.SupportsMultimodalFunctionResponses == *tc.flag {
					t.Fatal("capability pointer aliases")
				}
			}
		})
	}
}

func TestGoogleToolImagesWithoutCompatRetainLegacy(t *testing.T) {
	model := Model{API: "google-generative-ai", Input: []InputType{InputImage}}
	parts := convertGoogleToolResultContent(model, ToolResultMessage{ToolCallID: "a", ToolName: "image", Content: []ContentBlock{ImageContent{MIMEType: "image/png", Data: "YQ=="}}})
	if len(parts) != 2 || len(parts[0].FunctionResponse.Parts) != 0 || parts[1].InlineData == nil {
		t.Fatal("nil compat changed default wire")
	}
}
