package pigo

import (
	"reflect"
	"strings"
	"testing"
)

func TestKiroToolProjectionUserTextBlocksMatchString(t *testing.T) {
	for _, pointerUser := range []bool{false, true} {
		ctx, schemas := kiroToolProjectionFixture()
		want, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096)
		if err != nil {
			t.Fatal(err)
		}
		tail := &TextContent{Text: " fixture"}
		user := UserMessage{Content: []ContentBlock{TextContent{Text: "find"}, tail}}
		if pointerUser {
			ctx.Messages[0] = &user
		} else {
			ctx.Messages[0] = user
		}
		got, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("text blocks changed chronology or tool evidence: %#v, %v", got, err)
		}
		if tail.Text != " fixture" || len(user.Content.([]ContentBlock)) != 2 {
			t.Fatal("caller-owned content mutated")
		}
		tail.Text = "changed after projection"
		if got.History[0].User.Content != "find fixture" {
			t.Fatal("projection retained mutable input")
		}
	}
}

func TestKiroToolProjectionCurrentUserTextBlocks(t *testing.T) {
	ctx, schemas := kiroToolProjectionFixture()
	ctx.Messages = []Message{UserMessage{Content: []ContentBlock{
		&TextContent{Text: "  first"}, TextContent{Text: "\nsecond  "},
	}}}
	got, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 1, 1, 4096)
	if err != nil || len(got.History) != 0 || got.Current.Content != "  first\nsecond  " ||
		got.Current.Context == nil || len(got.Current.Context.Tools) != 1 {
		t.Fatalf("current user content/catalog changed: %#v, %v", got, err)
	}
}

func TestKiroToolProjectionRejectsUnsafeUserBlocks(t *testing.T) {
	for name, blocks := range map[string][]ContentBlock{
		"nil slice":        nil,
		"empty slice":      {},
		"whitespace":       {TextContent{Text: " \t"}, &TextContent{Text: "\n"}},
		"nil block":        {nil},
		"nil text pointer": {(*TextContent)(nil)},
		"signed text":      {TextContent{Text: "text", TextSignature: "signature"}},
		"image":            {TextContent{Text: "prefix"}, ImageContent{Data: "fixture", MIMEType: "image/png"}},
		"thinking":         {ThinkingContent{Thinking: "private reasoning"}},
		"tool":             {ToolCall{ID: "unexpected", Name: "lookup", RawArguments: "{}"}},
		"invalid utf8":     {TextContent{Text: string([]byte{0xff})}},
		"byte limit":       {TextContent{Text: strings.Repeat("x", 4097)}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, schemas := kiroToolProjectionFixture()
			ctx.Messages[0] = UserMessage{Content: blocks}
			got, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096)
			if err == nil || !reflect.DeepEqual(got, kiroToolProjection{}) {
				t.Fatalf("unsupported input partially returned: %#v, %v", got, err)
			}
		})
	}
}

func TestKiroToolProjectionUserBlocksShareAggregateLimit(t *testing.T) {
	// The fixture has two assistant blocks and one result block, so only
	// 4093 user blocks fit. Empty blocks still count toward the work limit.
	for _, count := range []int{4093, 4094, 4097} {
		ctx, schemas := kiroToolProjectionFixture()
		blocks := make([]ContentBlock, count)
		for i := range blocks {
			blocks[i] = TextContent{}
		}
		blocks[0] = TextContent{Text: "find fixture"}
		ctx.Messages[0] = UserMessage{Content: blocks}
		got, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096)
		if count == 4093 {
			if err != nil || got.History[0].User.Content != "find fixture" {
				t.Fatalf("exact aggregate boundary rejected: %#v, %v", got, err)
			}
		} else if err == nil || !reflect.DeepEqual(got, kiroToolProjection{}) {
			t.Fatalf("aggregate block limit ignored (%d): %#v, %v", count, got, err)
		}
	}
}
