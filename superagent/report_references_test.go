package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

// Exercise the real persistent coordinator in both routing modes, including
// immutable facts, old single-report consumers and ambiguous delivery recovery.
func TestReportReferencesMixedFactsAndRecovery(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "reports.jsonl")
			var refs []HistoryRef
			route := func(_ context.Context, job MainJob) (MainOutcome, error) {
				d := MainDecision{Action: "new"}
				if job.Input.Kind == "worker_result" {
					for _, ref := range job.Input.HistoryRefs {
						if ref.Kind == "report" {
							refs = append(refs, ref)
						}
					}
					d = MainDecision{Action: "reply", Text: "Worker outcome"}
					if len(refs) == 3 {
						d.ReportRefs = slices.Clone(refs)
					}
					// Mutable callback input must never replace the cited authoritative facts.
					job.Input.Report.Text = "FORGED"
					job.Input.Report.NeedsReview = false
					job.Input.Report.Receipts = nil
				} else if job.Input.Trigger.ID == "summary" {
					d = MainDecision{Action: "reply", Text: "Mixed results require review.", ReportRefs: slices.Clone(refs)}
				}
				return MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
			}
			worker := func(_ context.Context, job Job) (Outcome, error) {
				out := Outcome{Summary: "public " + job.Trigger.ID, Snapshot: agent.AgentSnapshot{SessionID: job.TaskID, Messages: []agent.Message{agent.NewUserTextMessage("PRIVATE_TRANSCRIPT")}}}
				switch job.Trigger.ID {
				case "partial":
					out.Status = "completed_with_tool_errors"
					out.NeedsReview = true
					out.Receipts = []ToolReceipt{{Token: 1, Domain: "agent", CallID: "write", ToolName: "write", Completed: true, Committed: true}}
				case "failed":
					out.NeedsReview = true
					out.Receipts = []ToolReceipt{{Token: 2, Domain: "agent", CallID: "write", ToolName: "write", Committed: true}}
					return out, errors.New("PRIVATE_FAILURE")
				}
				return out, nil
			}
			open := func() *SuperAgent {
				var a *SuperAgent
				var err error
				if batch {
					a, err = OpenMainBatch(t.Context(), path, 2, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
						out := MainBatchOutcome{Snapshot: job.Snapshot}
						for _, input := range job.Inputs {
							r, e := route(ctx, MainJob{SessionID: job.SessionID, Input: input, Snapshot: job.Snapshot, History: job.History})
							if e != nil {
								return out, e
							}
							out.Decisions = append(out.Decisions, MainInputDecision{InputID: input.ID, Decision: r.Decision})
						}
						return out, nil
					}, worker)
				} else {
					a, err = OpenMain(t.Context(), path, 2, route, worker)
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { a.Close() })
				return a
			}
			a := open()
			for _, id := range []string{"success", "partial", "failed", "summary"} {
				submit(t, a, Trigger{ID: id, Source: "im", Thread: "same", Kind: "request", Text: id})
				waitIdle(t, a)
			}
			if len(a.MainFailures()) != 0 {
				t.Fatal(a.MainFailures())
			}
			replies := a.Replies()
			if len(replies) != 4 || len(replies[0].Reports) != 0 || replies[0].Report == nil || len(replies[2].Reports) != 3 || replies[2].Report == nil || replies[3].Report != nil || len(replies[3].Reports) != 3 {
				t.Fatalf("reply compatibility: %+v", replies)
			}
			expected := a.ReportsSince(0)[:3]
			for _, reply := range replies[2:] {
				for i, citation := range reply.Reports {
					if citation.Ref != refs[i] || !reflect.DeepEqual(citation.Report, expected[i]) {
						t.Fatalf("facts: %+v != %+v", citation, expected[i])
					}
				}
			}
			raw, _ := json.Marshal(replies)
			for _, canary := range []string{"PRIVATE_TRANSCRIPT", "PRIVATE_FAILURE", "FORGED"} {
				if strings.Contains(string(raw), canary) {
					t.Fatal("private or forged facts exposed", string(raw))
				}
			}
			replies[3].Reports[1].Report.Receipts[0].Committed = false
			replies[3].Reports[0].Ref.ID = "mutated"
			if !a.Replies()[3].Reports[1].Report.Receipts[0].Committed || a.Replies()[3].Reports[0].Ref != refs[0] {
				t.Fatal("citations alias coordinator")
			}
			delivery, err := a.BeginReplyDelivery(replies[3].ID)
			if err != nil {
				t.Fatal(err)
			}
			want := a.Replies()
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			a = open()
			waitIdle(t, a)
			if !reflect.DeepEqual(want, a.Replies()) {
				t.Fatal("restart changed citation or delivery facts")
			}
			if _, err := a.BeginReplyDelivery(replies[3].ID); err == nil {
				t.Fatal("uncertain reply resent")
			}
			if err := a.ResolveReplyDelivery(delivery, ReplyDeliveryResult{Status: "delivered", MessageID: "message-1"}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"success", "partial", "failed", "summary"} {
				if accepted, err := a.Submit(Trigger{ID: id, Source: "im", Thread: "same", Kind: "request", Text: id}); err != nil || accepted {
					t.Fatalf("duplicate: %v %v", accepted, err)
				}
			}
			waitIdle(t, a)
			if len(a.Replies()) != 4 || len(a.ReportsSince(0)) != 4 {
				t.Fatal("duplicate produced facts or replies")
			}
		})
	}
}

func TestReportReferencesStrictPiDecision(t *testing.T) {
	ref := `{"domain":"main","kind":"report","id":"delivery-1"}`
	valid := `{"action":"reply","text":"facts","report_refs":[` + ref + `]}`
	d, err := parseMainDecision(valid)
	if err != nil || len(d.ReportRefs) != 1 {
		t.Fatalf("valid references: %+v %v", d, err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"kind":"report"`, `"kind":"task"`, 1),
		strings.Replace(valid, `"domain":"main"`, `"domain":"main","domain":"other"`, 1),
		strings.Replace(valid, `"domain"`, `"Domain"`, 1),
		strings.Replace(valid, `"id":"delivery-1"`, `"id":"delivery-1","report":{"status":"completed"}`, 1),
		strings.Replace(valid, `[`+ref+`]`, `null`, 1),
		strings.Replace(valid, `[`+ref+`]`, `[]`, 1),
		strings.Replace(valid, `[`+ref+`]`, `[`+ref+`,`+ref+`]`, 1),
		strings.Replace(valid, `"report_refs"`, `"Report_refs"`, 1),
		`{"action":"ignore","report_refs":[` + ref + `]}`,
		strings.Replace(valid, `"action":"reply"`, `"action":"reply","report_refs":[]`, 1),
	} {
		if _, err := parseMainDecision(raw); err == nil {
			t.Fatalf("invalid reference accepted: %s", raw)
		}
	}
}
