package pigo

import (
	"errors"
	"strings"
	"unicode/utf8"
)

type kiroSystemPromptPolicy string

const (
	kiroSystemPromptReject kiroSystemPromptPolicy = "reject"
	// Explicit semantic downgrade: NOT native system/developer role support.
	kiroSystemPromptPrependFirstUser kiroSystemPromptPolicy = "prepend_first_user"
)

var errKiroSystemPolicy = errors.New("kiro: unsupported system prompt policy or content")

// prepareKiroSystemPrompt lowers only an explicit Context.SystemPrompt, only
// with opt-in. It returns whether lowering occurred. No history repair, role
// inference or system-message folding occurs. The returned first message/slice
// is detached; other messages remain read-only shared inputs until serialization.
func prepareKiroSystemPrompt(ctx Context, policy kiroSystemPromptPolicy, maxBytes int) (Context, bool, error) {
	zero := Context{}
	if policy != "" && policy != kiroSystemPromptReject && policy != kiroSystemPromptPrependFirstUser {
		return zero, false, errKiroSystemPolicy
	}
	if maxBytes < 2 || maxBytes > 1<<20 || len(ctx.Messages) > 4096 {
		return zero, false, errKiroSystemPolicy
	}
	if ctx.SystemPrompt == "" {
		return ctx, false, nil
	}
	if policy != kiroSystemPromptPrependFirstUser || len(ctx.Messages) == 0 || len(ctx.SystemPrompt) > maxBytes || !utf8.ValidString(ctx.SystemPrompt) {
		return zero, false, errKiroSystemPolicy
	}
	var first UserMessage
	switch m := ctx.Messages[0].(type) {
	case UserMessage:
		first = m
	case *UserMessage:
		if m == nil {
			return zero, false, errKiroSystemPolicy
		}
		first = *m
	default:
		return zero, false, errKiroSystemPolicy
	}
	remaining := maxBytes - len(ctx.SystemPrompt)
	if remaining < 2 {
		return zero, false, errKiroSystemPolicy
	}
	remaining -= 2
	consume := func(text string) bool {
		if len(text) > remaining || !utf8.ValidString(text) {
			return false
		}
		remaining -= len(text)
		return true
	}
	var text string
	switch content := first.Content.(type) {
	case string:
		if !consume(content) {
			return zero, false, errKiroSystemPolicy
		}
		text = content
	case []ContentBlock:
		blocksLeft := maxKiroTextProjectionBlocks
		var ok bool
		text, ok = kiroProjectionText(content, consume, &blocksLeft)
		if !ok {
			return zero, false, errKiroSystemPolicy
		}
	default:
		return zero, false, errKiroSystemPolicy
	}
	// Do not turn an originally invalid empty user message into a valid one.
	if strings.TrimSpace(text) == "" {
		return zero, false, errKiroSystemPolicy
	}
	first.Content = ctx.SystemPrompt + "\n\n" + text
	out := ctx
	out.SystemPrompt = ""
	out.Messages = append([]Message(nil), ctx.Messages...)
	out.Messages[0] = first
	return out, true, nil
}
