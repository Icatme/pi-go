package pigo

import "testing"

func TestOpenAICodexToolResultOutputWithTextAndImage(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}

	output := buildOpenAICodexToolResultOutput([]ContentBlock{
		TextContent{Text: "Diameter is 100 pixels."},
		ImageContent{Data: "abcd", MIMEType: "image/png"},
	}, *model)

	parts, ok := output.([]map[string]any)
	if !ok {
		t.Fatalf("expected structured mixed output, got %T", output)
	}
	if len(parts) != 2 {
		t.Fatalf("expected text + image output parts, got %#v", parts)
	}
	if parts[0]["type"] != "input_text" || parts[0]["text"] != "Diameter is 100 pixels." {
		t.Fatalf("expected first part to be original text, got %#v", parts[0])
	}
	if parts[1]["type"] != "input_image" {
		t.Fatalf("expected second part to be image, got %#v", parts[1])
	}
}

func TestAnthropicToolResultContentWithTextAndImage(t *testing.T) {
	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	output := convertToolResultContent([]ContentBlock{
		TextContent{Text: "Diameter is 100 pixels."},
		ImageContent{Data: "abcd", MIMEType: "image/png"},
	}, *model)

	parts, ok := output.([]any)
	if !ok {
		t.Fatalf("expected structured mixed content, got %T", output)
	}
	if len(parts) != 2 {
		t.Fatalf("expected text + image content blocks, got %#v", parts)
	}
	textBlock, ok := parts[0].(anthropicTextBlock)
	if !ok || textBlock.Text != "Diameter is 100 pixels." {
		t.Fatalf("expected first block to be original text, got %#v", parts[0])
	}
	imageBlock, ok := parts[1].(anthropicImageBlock)
	if !ok || imageBlock.Source.MediaType != "image/png" || imageBlock.Source.Data != "abcd" {
		t.Fatalf("expected second block to be image, got %#v", parts[1])
	}
}
