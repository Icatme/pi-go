package pigo

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestKiroSystemPolicyExplicitLoweringAndOwnership(t *testing.T) {
	for _, blocks := range []bool{false, true} {
		user := UserMessage{Content: "question"}
		if blocks {
			user.Content = []ContentBlock{TextContent{Text: "ques"}, &TextContent{Text: "tion"}}
		}
		ctx := Context{SystemPrompt: "policy", Messages: []Message{&user}}
		result, lowered, err := prepareKiroSystemPrompt(ctx, kiroSystemPromptPrependFirstUser, 16)
		if err != nil || !lowered || result.SystemPrompt != "" || result.Messages[0].(UserMessage).Content != "policy\n\nquestion" {
			t.Fatal("explicit lowering", err)
		}
		result.Messages[0] = UserMessage{Content: "changed"}
		if ctx.SystemPrompt != "policy" || ctx.Messages[0] != &user {
			t.Fatal("mutated original")
		}
		again, lowered, err := prepareKiroSystemPrompt(ctx, kiroSystemPromptPrependFirstUser, 16)
		if err != nil || !lowered || again.Messages[0].(UserMessage).Content != "policy\n\nquestion" {
			t.Fatal("repeated input duplicated prefix")
		}
		if _, _, err := prepareKiroSystemPrompt(ctx, kiroSystemPromptPrependFirstUser, 15); err == nil {
			t.Fatal("decoded budget exceeded")
		}
	}
}

func TestKiroSystemPolicyRejectsUnapprovedOrUnsupportedSemantics(t *testing.T) {
	for _, policy := range []kiroSystemPromptPolicy{"", kiroSystemPromptReject, "future-policy"} {
		out, lowered, err := prepareKiroSystemPrompt(Context{SystemPrompt: "policy", Messages: []Message{UserMessage{Content: "question"}}}, policy, 100)
		if err == nil || lowered || !reflect.DeepEqual(out, Context{}) {
			t.Fatal("unapproved lowering")
		}
	}
	for _, first := range []Message{
		UserMessage{Content: " \n"},
		UserMessage{Content: []ContentBlock{TextContent{Text: "q", TextSignature: "opaque"}}},
		UserMessage{Content: []ContentBlock{ImageContent{Data: "x"}}},
		UserMessage{Content: string([]byte{255})},
		AssistantMessage{}, SystemMessage{}, (*UserMessage)(nil),
	} {
		out, lowered, err := prepareKiroSystemPrompt(Context{SystemPrompt: "policy", Messages: []Message{first}}, kiroSystemPromptPrependFirstUser, 100)
		if err == nil || lowered || !reflect.DeepEqual(out, Context{}) {
			t.Fatal("unsupported first message lowered")
		}
	}
	empty := Context{Messages: []Message{UserMessage{Content: "q"}}}
	if _, lowered, err := prepareKiroSystemPrompt(empty, "", 100); err != nil || lowered {
		t.Fatal("empty system")
	}
	if _, _, err := prepareKiroSystemPrompt(empty, "future-policy", 100); err == nil {
		t.Fatal("unknown policy silently accepted")
	}
}

func TestKiroSystemPolicyRequestTextAndTools(t *testing.T) {
	for _, tools := range []bool{false, true} {
		ctx := Context{SystemPrompt: "policy", Messages: []Message{UserMessage{Content: "question"}}}
		var schemas map[string]json.RawMessage
		if tools {
			catalog, s := kiroToolProjectionFixture()
			ctx.Tools = catalog.Tools
			schemas = s
		}
		options := kiroHTTPRequestFixture()
		if r, err := newKiroOfflineHTTPRequest(context.Background(), ctx, schemas, options); err == nil || r != nil {
			t.Fatal("default request silently lowered")
		}
		options.SystemPromptPolicy = kiroSystemPromptPrependFirstUser
		r, err := newKiroOfflineHTTPRequest(context.Background(), ctx, schemas, options)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		var payload kiroRequestEnvelope
		if json.Unmarshal(raw, &payload) != nil || payload.ConversationState.CurrentMessage.User.Content != "policy\n\nquestion" {
			t.Fatal("wire lowering changed")
		}
		if strings.Count(string(raw), "policy") != 1 || ctx.SystemPrompt != "policy" {
			t.Fatal("prefix count/ownership")
		}
	}
}

func TestKiroSystemPolicyHistoryAndCredentialPreflight(t *testing.T) {
	ctx, schemas := kiroToolProjectionFixture()
	ctx.SystemPrompt = "policy"
	options := kiroHTTPRequestFixture()
	options.SystemPromptPolicy = kiroSystemPromptPrependFirstUser
	r, err := newKiroOfflineHTTPRequest(context.Background(), ctx, schemas, options)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	raw, _ := io.ReadAll(r.Body)
	var payload kiroRequestEnvelope
	if json.Unmarshal(raw, &payload) != nil || payload.ConversationState.History[0].User.Content != "policy\n\nfind fixture" || payload.ConversationState.CurrentMessage.User.Content != "" {
		t.Fatal("system moved to tool-result carrier")
	}
	// Rejected policy/role never reaches credential resolution.
	calls := 0
	options.SystemPromptPolicy = ""
	r, err = newKiroOfflineRequestWithCredentialSource(context.Background(), ctx, schemas, options, func(context.Context, kiroCredentialScope) (kiroScopedCredential, error) {
		calls++
		return kiroScopedCredential{}, nil
	})
	if err == nil || r != nil || calls != 0 {
		t.Fatal("unapproved lowering resolved credentials")
	}
}
