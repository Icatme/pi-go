package superagent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
)

func TestReportReferencesFrozenViewAndBatchAtomicity(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			entered := make(chan MainJob, 1)
			refs := make(chan []HistoryRef, 1)
			route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
				d := MainDecision{Action: "ignore"}
				if job.Input.Trigger.ID == "seed" && job.Input.Kind == "user" {
					d.Action = "new"
				}
				if job.Input.Trigger.ID == "summary" {
					entered <- job
					select {
					case d.ReportRefs = <-refs:
					case <-ctx.Done():
						return MainOutcome{}, ctx.Err()
					}
					d.Action = "reply"
					d.Text = "must not commit"
				}
				return MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
			}
			var a *SuperAgent
			var err error
			path := filepath.Join(t.TempDir(), "frozen.jsonl")
			if batch {
				a, err = OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
					out := MainBatchOutcome{Snapshot: job.Snapshot}
					for _, input := range job.Inputs {
						r, e := route(ctx, MainJob{SessionID: job.SessionID, Input: input, Snapshot: job.Snapshot, History: job.History})
						if e != nil {
							return out, e
						}
						out.Decisions = append(out.Decisions, MainInputDecision{InputID: input.ID, Decision: r.Decision})
					}
					return out, nil
				}, mainWorker)
			} else {
				a, err = OpenMain(t.Context(), path, 1, route, mainWorker)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "seed", Source: "im", Thread: "chat", Kind: "request"})
			waitIdle(t, a)
			task := a.Status().Tasks[0].ID
			submit(t, a, Trigger{ID: "summary", Source: "im", Thread: "chat", Kind: "request"})
			job := receive(t, entered)
			if ok, err := a.SubmitTask(Trigger{ID: "late", Source: "im", Thread: "chat", Kind: "request"}, task); err != nil || !ok {
				t.Fatal(ok, err)
			}
			late := waitBatchBackgroundReport(t, a, "late")
			refs <- []HistoryRef{{Domain: job.SessionID, Kind: "report", ID: late.DeliveryID}}
			waitIdle(t, a)
			if len(a.MainFailures()) != 1 || len(a.Replies()) != 0 {
				t.Fatalf("unoffered live report accepted: %+v", a.Replies())
			}
			if _, err := job.History.Read(t.Context(), HistoryRequest{Ref: job.Input.HistoryRefs[0], MaxBytes: 4096, MaxItems: 8}); err == nil {
				t.Fatal("returned callback view remains readable")
			}
		})
	}
	// Direct persisted-group counterexample: the valid first reply must not escape
	// a second invalid citation, even though both have individually valid texts.
	a, _ := threadStoppedFixture(t)
	a.mu.Lock()
	defer a.mu.Unlock()
	for a.main.cursor < len(a.main.inbox) {
		input := a.main.inbox[a.main.cursor].input
		if err := a.commit(transition{State: "main_decided", Main: &mainTransition{InputID: input.ID, DecisionID: "main-decision-" + input.ID, Decision: MainDecision{Action: "ignore"}, Snapshot: json.RawMessage(`{"sessionId":"` + a.main.id + `"}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	// Append two plain inputs without any running scheduler.
	for _, id := range []string{"summary-a", "summary-b"} {
		if err := a.commit(transition{State: "received", Index: len(a.events), Trigger: &Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"}}); err != nil {
			t.Fatal(err)
		}
	}
	m := &mainTransition{Snapshot: json.RawMessage(`{"sessionId":"` + a.main.id + `"}`)}
	for i, item := range a.main.inbox[a.main.cursor:] {
		d := MainDecision{Action: "reply", Text: "reply"}
		if i == 1 {
			d.ReportRefs = []HistoryRef{{Domain: a.main.id, Kind: "report", ID: "delivery-999"}}
		}
		m.Batch = append(m.Batch, mainTransition{InputID: item.input.ID, DecisionID: "main-decision-" + item.input.ID, ReplyID: "reply-" + item.input.ID, Decision: d})
	}
	cursor, n := a.main.cursor, len(a.main.replies)
	if err := a.commit(transition{State: "main_batch_decided", Main: m}); err == nil {
		t.Fatal("invalid batch committed")
	}
	if a.main.cursor != cursor || len(a.main.replies) != n {
		t.Fatal("invalid batch partially applied")
	}
}

func TestReportReferencesVisibilityReplayAndCopy(t *testing.T) {
	// Resolve directly from durable state, including a two-target fanout identity.
	a := &SuperAgent{main: mainState{id: "domain"}}
	for i := 0; i < 5; i++ {
		a.events = append(a.events, item{admission: i, trigger: Trigger{Kind: "request"}})
	}
	a.events[1].trigger.Kind = "noise"
	a.events[2].admissionRecord = &AdmissionRecord{Decision: "ignored"}
	a.events[4].admission = 3
	for i := 0; i < 5; i++ {
		a.reports = append(a.reports, Report{DeliveryID: deliveryID(i), Source: "github", EventID: "same", TaskID: fmt.Sprint(i), Status: "completed"})
	}
	refs := []HistoryRef{{Domain: "domain", Kind: "report", ID: "delivery-4"}, {Domain: "domain", Kind: "report", ID: "delivery-5"}}
	got, err := a.resolveReportReferences(refs)
	if err != nil || len(got) != 2 || got[0].Report.TaskID == got[1].Report.TaskID {
		t.Fatalf("fanout: %+v %v", got, err)
	}
	for _, id := range []string{"delivery-2", "delivery-3", "delivery-0", "delivery-01", "delivery-+1", "1", "delivery-6", "delivery-9223372036854775808"} {
		if _, err := a.resolveReportReferences([]HistoryRef{{Domain: "domain", Kind: "report", ID: id}}); err == nil {
			t.Fatal("invalid/hidden report accepted", id)
		}
	}
	for _, invalid := range [][]HistoryRef{{{Domain: "foreign", Kind: "report", ID: "delivery-1"}}, {{Domain: "domain", Kind: "task", ID: "delivery-1"}}, {refs[0], refs[0]}, {}, slices.Repeat(refs, 9)} {
		if _, err := a.resolveReportReferences(invalid); err == nil {
			t.Fatal("invalid list accepted", invalid)
		}
	}
	a.reports[0].Background = &BackgroundCompletion{Output: json.RawMessage(`"PRIVATE_OUTPUT"`), Receipts: []background.Receipt{{Token: 1, Committed: true}}}
	got, err = a.resolveReportReferences([]HistoryRef{{Domain: "domain", Kind: "report", ID: "delivery-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Report.Background.Output != nil {
		t.Fatal("private background output exposed")
	}
	copied := copyReportCitations(got)
	copied[0].Report.Background.Receipts[0].Committed = false
	if !got[0].Report.Background.Receipts[0].Committed || !a.reports[0].Background.Receipts[0].Committed {
		t.Fatal("background receipt alias")
	}
}

func TestReportReferencesAllPiAdapters(t *testing.T) {
	for _, mode := range []string{"single", "batch", "history", "batch-history"} {
		t.Run(mode, func(t *testing.T) {
			var refs []HistoryRef
			def := agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
				var payload struct {
					Input  MainInput   `json:"input"`
					Inputs []MainInput `json:"inputs"`
				}
				if err := json.Unmarshal([]byte(messageText(req.Messages[len(req.Messages)-1])), &payload); err != nil {
					return nil, err
				}
				inputs := payload.Inputs
				if inputs == nil {
					inputs = []MainInput{payload.Input}
				}
				var ds []MainInputDecision
				for _, input := range inputs {
					d := MainDecision{Action: "new"}
					if input.Kind == "worker_result" {
						for _, ref := range input.HistoryRefs {
							if ref.Kind == "report" {
								refs = append(refs, ref)
							}
						}
						d = MainDecision{Action: "reply", Text: "Authoritative facts attached", ReportRefs: slices.Clone(refs)}
					}
					ds = append(ds, MainInputDecision{InputID: input.ID, Decision: d})
				}
				var raw []byte
				if strings.Contains(mode, "batch") {
					raw, _ = json.Marshal(map[string]any{"decisions": ds})
				} else {
					raw, _ = json.Marshal(ds[0].Decision)
				}
				return offlineAnswer(string(raw)), nil
			})}
			path := filepath.Join(t.TempDir(), "pi.jsonl")
			var a *SuperAgent
			var err error
			if strings.Contains(mode, "batch") {
				var r MainBatchRouter
				if mode == "batch-history" {
					r, err = PiMainBatchHistoryRouter(def, 128<<10)
				} else {
					r, err = PiMainBatchRouter(def, 128<<10)
				}
				if err == nil {
					a, err = OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, r, mainWorker)
				}
			} else {
				var r MainRouter
				if mode == "history" {
					r, err = PiMainHistoryRouter(def, 128<<10)
				} else {
					r, err = PiMainRouter(def, 128<<10)
				}
				if err == nil {
					a, err = OpenMain(t.Context(), path, 1, r, mainWorker)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			for _, id := range []string{"one", "two"} {
				submit(t, a, Trigger{ID: id, Source: "im", Thread: "same", Kind: "request"})
				waitIdle(t, a)
			}
			replies := a.Replies()
			if len(a.MainFailures()) != 0 || len(replies) != 2 || len(replies[1].Reports) != 2 {
				t.Fatalf("adapter: %+v %+v", replies, a.MainFailures())
			}
			for i, c := range replies[1].Reports {
				if !reflect.DeepEqual(c.Report, a.ReportsSince(0)[i]) {
					t.Fatal("adapter invented citation")
				}
			}
		})
	}
}
