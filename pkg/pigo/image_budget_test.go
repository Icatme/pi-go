package pigo

import (
	"errors"
	"reflect"
	"testing"
)

func TestImageBudgetProtectsInputAndToolAssociations(t *testing.T) {
	img := ImageContent{Data: "png", MIMEType: "image/png"}
	input := []Message{
		UserMessage{Content: []ContentBlock{img}},
		AssistantMessage{Content: []ContentBlock{ToolCall{ID: "call", Name: "screenshot", Arguments: map[string]any{"n": 1}}}},
		ToolResultMessage{ToolCallID: "call", ToolName: "screenshot", IsError: true, Details: map[string]any{"x": 1}, Content: []ContentBlock{TextContent{Text: "old"}, img, img}},
		UserMessage{Content: []ContentBlock{img}},
		ToolResultMessage{ToolCallID: "current", Content: []ContentBlock{img}},
	}
	before := cloneMessages(input)
	out, err := ApplyImageBudget(input, ImageBudget{MaxImages: 4, PruneBefore: 3})
	if err != nil {
		t.Fatal(err)
	}
	want := cloneMessages(input)
	old := want[2].(ToolResultMessage)
	old.Content[1] = TextContent{Text: "(tool image omitted by host image budget)"}
	want[2] = old
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("unexpected output: %#v", out)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("source mutated")
	}
	out[2].(ToolResultMessage).Content[0] = TextContent{Text: "changed copy"}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("changed tool content aliases source")
	}

}

func TestImageBudgetProtectedOverflowIsAtomic(t *testing.T) {
	img := ImageContent{Data: "png", MIMEType: "image/png"}
	input := []Message{ToolResultMessage{Content: []ContentBlock{img}}, UserMessage{Content: []ContentBlock{img, img}}, AssistantMessage{Content: []ContentBlock{img}}}
	before := cloneMessages(input)
	out, err := ApplyImageBudget(input, ImageBudget{MaxImages: 2, PruneBefore: 1})
	var exceeded *ImageBudgetExceededError
	if out != nil || !errors.As(err, &exceeded) || exceeded.Limit != 2 || exceeded.Protected != 3 {
		t.Fatalf("out=%v error=%v", out, err)
	}
	if !reflect.DeepEqual(before, input) {
		t.Fatal("source mutated on error")
	}
}

func TestImageBudgetConfigurationAndDisabled(t *testing.T) {
	input := []Message{ToolResultMessage{Content: []ContentBlock{ImageContent{Data: "png", MIMEType: "image/png"}, ImageContent{Data: "png", MIMEType: "image/png"}}}}
	for _, b := range []ImageBudget{{}, {MaxImages: 2}, {MaxImages: 3, PruneBefore: 1}} {
		out, err := ApplyImageBudget(input, b)
		if err != nil || !reflect.DeepEqual(out, input) {
			t.Fatalf("%+v: %v %v", b, out, err)
		}
	}
	for _, b := range []ImageBudget{{MaxImages: -1}, {MaxImages: 1, PruneBefore: -1}, {MaxImages: 1, PruneBefore: 2}, {MaxImages: 1}} {
		if out, err := ApplyImageBudget(input, b); err == nil || out != nil {
			t.Fatalf("expected failure for %+v", b)
		}
	}
}

func TestImageBudgetManyScreenshotsAndIdempotence(t *testing.T) {
	input := make([]Message, 31)
	for i := range input {
		input[i] = ToolResultMessage{ToolCallID: "screenshot", Content: []ContentBlock{ImageContent{Data: "png", MIMEType: "image/png"}}}
	}
	budget := ImageBudget{MaxImages: 7, PruneBefore: 30}
	out, err := ApplyImageBudget(input, budget)
	if err != nil {
		t.Fatal(err)
	}
	for i, msg := range out {
		_, image := msg.(ToolResultMessage).Content[0].(ImageContent)
		if image != (i >= 24) {
			t.Fatalf("wrong retention at %d", i)
		}
	}
	again, err := ApplyImageBudget(out, budget)
	if err != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("not idempotent", err)
	}
}

func TestImageBudgetRejectsAmbiguousRepresentationsWhenEnabled(t *testing.T) {
	image := ImageContent{Data: "png", MIMEType: "image/png"}
	var nilUser *UserMessage
	for _, input := range [][]Message{
		{nil}, {nilUser}, {&UserMessage{Content: []ContentBlock{image}}},
		{UserMessage{Content: []any{map[string]any{"type": "image", "data": "png"}}}},
		{UserMessage{Content: []ContentBlock{&image}}},
		{ToolResultMessage{Content: []ContentBlock{nil}}},
	} {
		if out, err := ApplyImageBudget(input, ImageBudget{MaxImages: 1}); err == nil || out != nil {
			t.Fatalf("accepted ambiguous input: %#v", input)
		}
		if out, err := ApplyImageBudget(input, ImageBudget{}); err != nil || !reflect.DeepEqual(input, out) {
			t.Fatal("disabled policy changed input", err)
		}
	}
}
