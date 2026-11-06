package channel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/superagent"
	"github.com/Icatme/pi-go/superagent/channel"
)

// Exercise the real channel boundary and public history together: an allowed
// original author must not make a denied editor's text visible to a later editor.
func TestGitHubEditedCommentHistoryAdmissionComposition(t *testing.T) {
	for _, event := range []string{"issue_comment", "pull_request_review_comment"} {
		for _, batch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/batch=%v", event, batch), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "main.jsonl")
				var hidden []string
				observed := make(chan string, 2)
				inspect := func(ctx context.Context, input superagent.MainInput, history *superagent.MainHistory) error {
					if len(input.HistoryRefs) != 2 || !strings.HasPrefix(input.Trigger.Text, "PUBLIC_") {
						return fmt.Errorf("incorrect editor input: %+v", input)
					}
					domain := input.HistoryRefs[0].Domain
					for _, id := range hidden {
						for _, kind := range []string{"trigger", "thread", "report"} {
							page, err := history.Read(ctx, superagent.HistoryRequest{Ref: superagent.HistoryRef{Domain: domain, Kind: kind, ID: id}, MaxBytes: 1024, MaxItems: 1})
							if err == nil || page.Ref != (superagent.HistoryRef{}) || page.Data != "" || len(page.Links) != 0 || page.Next != "" {
								return fmt.Errorf("hidden editor/noise reference readable: %s/%s", kind, id)
							}
						}
					}
					queue := append([]superagent.HistoryRef(nil), input.HistoryRefs...)
					seen := map[superagent.HistoryRef]bool{}
					var all strings.Builder
					for len(queue) != 0 {
						ref := queue[0]
						queue = queue[1:]
						if seen[ref] {
							continue
						}
						seen[ref] = true
						for cursor := ""; ; {
							page, err := history.Read(ctx, superagent.HistoryRequest{Ref: ref, Cursor: cursor, MaxBytes: 1024, MaxItems: 1})
							if err != nil {
								return err
							}
							all.WriteString(page.Data)
							links, err := json.Marshal(page.Links)
							if err != nil {
								return err
							}
							all.Write(links)
							for _, link := range page.Links {
								queue = append(queue, link.Ref)
								for _, id := range hidden {
									if link.Ref.ID == id {
										return fmt.Errorf("hidden editor/noise link: %+v", link)
									}
								}
							}
							if page.Next == "" {
								break
							}
							cursor = page.Next
						}
					}
					if strings.Contains(all.String(), "HIDDEN_") || !strings.Contains(all.String(), "PUBLIC_first") {
						return fmt.Errorf("history lost allowed edit or exposed hidden text: %s", all.String())
					}
					observed <- input.Trigger.Text
					return nil
				}
				open := func() *superagent.SuperAgent {
					worker := func(context.Context, superagent.Job) (superagent.Outcome, error) {
						return superagent.Outcome{}, fmt.Errorf("unexpected worker execution")
					}
					var a *superagent.SuperAgent
					var err error
					if batch {
						a, err = superagent.OpenMainBatch(t.Context(), path, 1, superagent.MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, func(ctx context.Context, job superagent.MainBatchJob) (superagent.MainBatchOutcome, error) {
							out := superagent.MainBatchOutcome{Snapshot: job.Snapshot}
							for _, input := range job.Inputs {
								if err := inspect(ctx, input, job.History); err != nil {
									return out, err
								}
								out.Decisions = append(out.Decisions, superagent.MainInputDecision{InputID: input.ID, Decision: superagent.MainDecision{Action: "ignore"}})
							}
							return out, nil
						}, worker)
					} else {
						a, err = superagent.OpenMain(t.Context(), path, 1, func(ctx context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
							return superagent.MainOutcome{Decision: superagent.MainDecision{Action: "ignore"}, Snapshot: job.Snapshot}, inspect(ctx, job.Input, job.History)
						}, worker)
					}
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { a.Close() })
					return a
				}
				a := open()
				sim := newSimulator("")
				c := adapter(t, a, channel.GitHub, sim)
				deniedID, deniedBody := editedComment(event, "denied-editor", "8", "7", "81")
				deniedBody = bytes.ReplaceAll(deniedBody, []byte("please attach task-from-text"), []byte("HIDDEN_denied_editor"))
				denied, err := c.Admit(deniedID, deniedBody)
				if err != nil || denied.Record.Decision != "ignored" || denied.Record.Reason != "policy_denied" || denied.Record.Envelope.Principal != "8" {
					t.Fatalf("denied editor: %+v %v", denied, err)
				}
				hidden = append(hidden, denied.DeliveryIDs...)
				noise, err := a.Admit(config(channel.GitHub).Policy, superagent.AdmissionEnvelope{Trigger: superagent.Trigger{ID: "noise", Source: "github:42", Thread: deniedID.Thread, Kind: "noise", Text: "HIDDEN_noise"}, Principal: "7", Group: "42"})
				if err != nil || noise.Record.Reason != "noise" {
					t.Fatalf("noise admission: %+v %v", noise, err)
				}
				hidden = append(hidden, noise.DeliveryIDs...)
				for _, round := range []string{"first", "reopened"} {
					if round == "reopened" {
						if err := a.Close(); err != nil {
							t.Fatal(err)
						}
						a = open()
						c = adapter(t, a, channel.GitHub, sim)
						changed := config(channel.GitHub)
						changed.Policy.Version = "now-allows-editor"
						changed.Policy.Rules = append(changed.Policy.Rules, superagent.AdmissionRule{Source: "github:42", Principal: "8", Group: "42"})
						nowAllowed, err := channel.New(a, changed, sim)
						if err != nil {
							t.Fatal(err)
						}
						if replay, err := nowAllowed.Admit(deniedID, deniedBody); err != nil || !replay.Duplicate || replay.Record.Decision != "ignored" || replay.Record.Envelope.Principal != "8" {
							t.Fatalf("changed policy rewrote editor admission: %+v %v", replay, err)
						}
					}
					id, body := editedComment(event, "allowed-"+round, "7", "8", "82")
					body = bytes.ReplaceAll(body, []byte("please attach task-from-text"), []byte("PUBLIC_"+round))
					if accepted, err := c.Admit(id, body); err != nil || accepted.Record.Decision != "accepted" || accepted.Record.Envelope.Principal != "7" || accepted.Record.Envelope.TaskID != "" || accepted.Record.Envelope.ToThread {
						t.Fatalf("allowed editor: %+v %v", accepted, err)
					}
					if err := a.WaitIdle(t.Context()); err != nil {
						t.Fatal(err)
					}
					if failures := a.MainFailures(); len(failures) != 0 {
						t.Fatalf("composition failed: %+v", failures)
					}
					select {
					case got := <-observed:
						if got != "PUBLIC_"+round {
							t.Fatalf("unexpected router input: %s", got)
						}
					default:
						t.Fatal("allowed editor did not reach public history")
					}
				}
				journal, err := os.ReadFile(path)
				if err != nil || !bytes.Contains(journal, []byte("HIDDEN_denied_editor")) || !bytes.Contains(journal, []byte("HIDDEN_noise")) || len(a.Admissions()) != 4 || len(a.ReportsSince(0)) != 4 {
					t.Fatalf("private admission audit was not preserved: admissions=%d reports=%d error=%v", len(a.Admissions()), len(a.ReportsSince(0)), err)
				}
				if len(a.Status().Tasks) != 0 || len(a.Replies()) != 0 {
					t.Fatal("history inspection or payload claims created authority/effects")
				}
				if sends, lookups := sim.counts(); sends != 0 || lookups != 0 {
					t.Fatal("offline history inspection used the transport")
				}
			})
		}
	}
}
