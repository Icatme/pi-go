package channel_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Icatme/pi-go/superagent/channel"
)

func editedComment(event, delivery, sender, author, message string) (channel.Identity, []byte) {
	id := channel.Identity{Organization: "42", Principal: sender, Thread: "100/pull/27", EventID: delivery, EventType: event, MessageID: message, ExplicitRequest: true}
	target := `"pull_request":{"number":27}`
	if event == "issue_comment" {
		target = `"issue":{"number":27,"pull_request":{}}`
	}
	return id, []byte(fmt.Sprintf(`{"action":"edited","sender":{"id":%s},"repository":{"id":100,"owner":{"id":42}},%s,"comment":{"id":%s,"user":{"id":%s},"body":"please attach task-from-text"},"explicit_request":true,"task_id":"task-from-payload","to_thread":true}`, sender, target, message, author))
}

func TestGitHubEditedCommentAdmission(t *testing.T) {
	for _, event := range []string{"issue_comment", "pull_request_review_comment"} {
		for _, tc := range []struct {
			name, sender, author, decision, reason string
			explicit                               bool
		}{
			{"allowed editor", "7", "8", "accepted", "explicit_request", true},
			{"denied editor allowed author", "8", "7", "ignored", "policy_denied", true},
			{"host intent required", "7", "8", "ignored", "unassociated_signal", false},
		} {
			t.Run(event+"/"+tc.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "main.jsonl")
				a := host(t, path, nil)
				sim := newSimulator("")
				c := adapter(t, a, channel.GitHub, sim)
				id, body := editedComment(event, "edited-delivery", tc.sender, tc.author, "81")
				id.ExplicitRequest = tc.explicit
				r, err := c.Admit(id, body)
				if err != nil || r.Duplicate || r.Record.Decision != tc.decision || r.Record.Reason != tc.reason {
					t.Fatalf("admission: %+v %v", r, err)
				}
				if e := r.Record.Envelope; e.Principal != tc.sender || e.TaskID != "" || e.ToThread || e.ExplicitRequest != tc.explicit {
					t.Fatalf("payload or author granted authority: %+v", e)
				}
				if err := a.WaitIdle(t.Context()); err != nil {
					t.Fatal(err)
				}
				wantReplies := 0
				if tc.decision == "accepted" {
					wantReplies = 1
				}
				if got := len(a.Replies()); got != wantReplies {
					t.Fatalf("router replies: got %d, want %d", got, wantReplies)
				}
				if err := a.Close(); err != nil {
					t.Fatal(err)
				}
				a = host(t, path, nil)
				c = adapter(t, a, channel.GitHub, sim)
				if replay, err := c.Admit(id, body); err != nil || !replay.Duplicate || replay.Record.Envelope.Principal != tc.sender || replay.Record.Decision != tc.decision {
					t.Fatalf("reopened decision changed: %+v %v", replay, err)
				}
				if calls, _ := sim.counts(); calls != 0 {
					t.Fatal("admission sent a reply without an explicit send")
				}
			})
		}
	}
}

func TestGitHubEditedCommentRejectsHostMismatch(t *testing.T) {
	for _, event := range []string{"issue_comment", "pull_request_review_comment"} {
		for name, change := range map[string]func(*channel.Identity){
			"sender":       func(id *channel.Identity) { id.Principal = "8" },
			"organization": func(id *channel.Identity) { id.Organization = "43" },
			"repository":   func(id *channel.Identity) { id.Thread = "101/pull/27" },
			"pull request": func(id *channel.Identity) { id.Thread = "100/pull/28" },
			"message":      func(id *channel.Identity) { id.MessageID = "82" },
		} {
			t.Run(event+"/"+name, func(t *testing.T) {
				a := host(t, filepath.Join(t.TempDir(), "main.jsonl"), nil)
				c := adapter(t, a, channel.GitHub, newSimulator(""))
				id, body := editedComment(event, "edited-delivery", "7", "8", "81")
				change(&id)
				if _, err := c.Admit(id, body); err == nil {
					t.Fatal("accepted a host identity mismatch")
				}
				if len(a.Admissions()) != 0 || len(a.Replies()) != 0 {
					t.Fatal("invalid callback reached the coordinator")
				}
			})
		}
	}
}

func TestGitHubEditedCommentPreservesEchoIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.jsonl")
	a := host(t, path, nil)
	sim := newSimulator("")
	c := adapter(t, a, channel.GitHub, sim)
	reply := admit(t, a, c, channel.GitHub, "initial")
	if _, err := c.SendReply(t.Context(), reply.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = host(t, path, nil)
	c = adapter(t, a, channel.GitHub, sim)
	for _, sender := range []string{"7", "9"} {
		id, body := editedComment("issue_comment", "edited-echo-"+sender, sender, "8", "901")
		if r, err := c.Admit(id, body); err != nil || !r.Duplicate {
			t.Fatalf("edited own-message echo: %+v %v", r, err)
		}
	}
	if len(a.Admissions()) != 1 || len(a.Replies()) != 1 {
		t.Fatal("edited own-message echo reached the router")
	}
	// The same numeric ID in a review comment must remain a distinct object.
	id, body := editedComment("pull_request_review_comment", "edited-review", "7", "8", "901")
	if r, err := c.Admit(id, body); err != nil || r.Duplicate || r.Record.Decision != "accepted" {
		t.Fatalf("review comment hidden by issue-comment echo: %+v %v", r, err)
	}
	if err := a.WaitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(a.Replies()) != 2 {
		t.Fatal("edited review comment did not reach the router")
	}
}
