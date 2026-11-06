package superagent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplyChannelCursorAndDeadline(t *testing.T) {
	a, path, id := replyJournal(t)
	d, e := a.BeginReplyDelivery(id)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range []ReplyDeliveryResult{
		{Status: "delivered", MessageID: "m", RetryAt: 1},
		{Status: "unknown", Cursor: "cursor"},
		{Status: "not_delivered", Note: "Known absence", RetryAt: -1},
		{Status: "not_delivered", Note: "Known absence", RetryAt: 253402300800000},
	} {
		if e := a.ResolveReplyDelivery(d, r); e == nil {
			t.Fatalf("invalid receipt accepted: %+v", r)
		}
	}
	result := ReplyDeliveryResult{Status: "not_delivered", Note: "Known absence", RetryAt: time.Now().Add(time.Hour).UnixMilli()}
	if e := a.ResolveReplyDelivery(d, result); e != nil {
		t.Fatal(e)
	}
	if _, e := a.BeginReplyDelivery(id); !errors.Is(e, ErrReplyRateLimited) {
		t.Fatalf("rate bypass: %v", e)
	}
	altered := result
	altered.RetryAt = 0
	if e := a.ResolveReplyDelivery(d, altered); e == nil {
		t.Fatal("deadline erased")
	}
	a.Close()
	reopened, e := openState(t.Context(), path, false)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if _, e := reopened.BeginReplyDelivery(id); !errors.Is(e, ErrReplyRateLimited) {
		t.Fatal("replay lost deadline")
	}
}
func TestReplyCursorMonotonicEnrichment(t *testing.T) {
	a, path, id := replyJournal(t)
	d, e := a.BeginReplyDelivery(id)
	if e != nil {
		t.Fatal(e)
	}
	first := ReplyDeliveryResult{Status: "delivered", MessageID: "m"}
	if e := a.ResolveReplyDelivery(d, first); e != nil {
		t.Fatal(e)
	}
	full := first
	full.Cursor = "opaque-page-2"
	if e := a.ResolveReplyDelivery(d, full); e != nil {
		t.Fatal(e)
	}
	if e := a.ResolveReplyDelivery(d, first); e != nil {
		t.Fatal(e)
	}
	if a.Replies()[0].Attempts[0].Cursor != full.Cursor {
		t.Fatal("cursor erased")
	}
	full.Cursor = "conflict"
	if e := a.ResolveReplyDelivery(d, full); e == nil {
		t.Fatal("cursor replaced")
	}
	// Copy and replay the exact cursor transition without modifying the source.
	target := filepath.Join(t.TempDir(), "replay.jsonl")
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(target, data, 0600); e != nil {
		t.Fatal(e)
	}
	b, e := openState(t.Context(), target, false)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if b.Replies()[0].Attempts[0].Cursor != "opaque-page-2" {
		t.Fatal("cursor lost on replay")
	}
}

func TestLegacyAdmissionIdentityEquivalence(t *testing.T) {
	legacy := AdmissionEnvelope{Trigger: Trigger{ID: "message-1", Source: "im:42", Thread: "chat", Kind: "request", Text: "Hello"}, Principal: "7", Group: "42", ExplicitRequest: true}
	explicit := legacy
	explicit.MessageID = legacy.Trigger.ID
	if !sameAdmissionEnvelope(legacy, explicit) {
		t.Fatal("equivalent legacy platform ID changed admission")
	}
	explicit.MessageID = "different-platform-message"
	if sameAdmissionEnvelope(legacy, explicit) {
		t.Fatal("inferred new platform association for legacy event")
	}
}
