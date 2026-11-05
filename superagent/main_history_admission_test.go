package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestMainHistoryAdmissionVisibility(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%v", batch), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.jsonl")
			entered := make(chan MainJob, 1)
			release := make(chan struct{})
			route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
				select {
				case entered <- job:
				case <-ctx.Done():
					return MainOutcome{}, ctx.Err()
				}
				select {
				case <-release:
				case <-ctx.Done():
					return MainOutcome{}, ctx.Err()
				}
				return MainOutcome{Decision: MainDecision{Action: "ignore"}, Snapshot: job.Snapshot}, nil
			}
			open := func() *SuperAgent {
				work := func(context.Context, Job) (Outcome, error) { panic("hidden input reached worker") }
				var a *SuperAgent
				var err error
				if batch {
					a, err = OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
						if len(job.Inputs) != 1 {
							return MainBatchOutcome{}, fmt.Errorf("unexpected inputs: %d", len(job.Inputs))
						}
						out, err := route(ctx, MainJob{SessionID: job.SessionID, Input: job.Inputs[0], History: job.History, Snapshot: job.Snapshot})
						return MainBatchOutcome{Snapshot: out.Snapshot, Decisions: []MainInputDecision{{InputID: job.Inputs[0].ID, Decision: out.Decision}}}, err
					}, work)
				} else {
					a, err = OpenMain(t.Context(), path, 1, route, work)
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { a.Close() })
				return a
			}
			a := open()
			policy := AdmissionPolicy{Version: "allow", Rules: []AdmissionRule{{Source: "im", Principal: "owner", Group: "team"}}}
			var hidden []string
			var envelopes []AdmissionEnvelope
			admitHidden := func(suffix string) {
				for _, reason := range []string{"policy_denied", "noise", "unassociated_signal", "no_explicit_intent"} {
					e := AdmissionEnvelope{Trigger: Trigger{ID: reason + suffix, Source: "im", Thread: "chat", Kind: "request", Text: "HIDDEN_" + reason + suffix}, Principal: "owner", Group: "team", ExplicitRequest: true}
					switch reason {
					case "policy_denied":
						e.Principal = "stranger"
					case "noise":
						e.Trigger.Kind, e.ExplicitRequest = "noise", false
					case "unassociated_signal":
						e.Trigger.Kind, e.ExplicitRequest, e.ToThread = "signal", false, true
					case "no_explicit_intent":
						e.ExplicitRequest = false
					}
					r, err := a.Admit(policy, e)
					if err != nil || r.Record.Decision != "ignored" {
						t.Fatalf("hidden admission: %+v %v", r, err)
					}
					hidden = append(hidden, r.DeliveryIDs...)
					envelopes = append(envelopes, e)
				}
				// Privileged and pre-gate journals have no AdmissionRecord, but
				// their noise must not become an indirect model input either.
				submit(t, a, Trigger{ID: "legacy-noise" + suffix, Source: "im", Thread: "chat", Kind: "noise", Text: "HIDDEN_legacy" + suffix})
				waitIdle(t, a)
				reports := a.ReportsSince(0)
				hidden = append(hidden, reports[len(reports)-1].DeliveryID)
			}
			admitHidden("-first")
			var public []HistoryRef
			for round := range 3 {
				if round == 1 {
					admitHidden("-cached")
				}
				if round == 2 {
					if err := a.Close(); err != nil {
						t.Fatal(err)
					}
					a = open()
				}
				e := AdmissionEnvelope{Trigger: Trigger{ID: fmt.Sprintf("public-%d", round), Source: "im", Thread: "chat", Kind: "request", Text: strings.Repeat("PUBLIC_汉🙂", 70)}, Principal: "owner", Group: "team", ExplicitRequest: true}
				r, err := a.Admit(policy, e)
				if err != nil || r.Record.Decision != "accepted" {
					t.Fatalf("allowed admission: %+v %v", r, err)
				}
				job := receive(t, entered)
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if job.Input.Trigger != e.Trigger || len(job.Input.HistoryRefs) != 2 {
					t.Fatalf("incorrect model input: %+v", job.Input)
				}
				public = append(public, HistoryRef{Domain: job.SessionID, Kind: "trigger", ID: r.DeliveryIDs[0]})
				checkHistoryAdmissionHidden(t, job.History, job.Input.HistoryRefs, hidden)
				for _, ref := range public {
					// Model action=ignore is distinct from admission rejection.
					data, _ := historyReadAll(t, job.History, ref, 1024, 1)
					if !strings.Contains(data, "PUBLIC_") {
						t.Fatal("accepted event lost after model ignored it")
					}
				}
				for _, env := range envelopes {
					duplicate, err := a.Admit(AdmissionPolicy{Version: "new-policy"}, env)
					if err != nil || !duplicate.Duplicate || duplicate.Record.Decision != "ignored" {
						t.Fatalf("visibility changed admission/dedup: %+v %v", duplicate, err)
					}
				}
				if got := a.Admissions(); len(got) != len(envelopes)+round+1 {
					t.Fatalf("audit lost records: %d", len(got))
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) || !bytes.Contains(after, []byte("HIDDEN_")) {
					t.Fatal("history rewrote or destroyed private audit data")
				}
				release <- struct{}{}
				waitIdle(t, a)
				if len(a.MainFailures()) != 0 || len(a.Status().Tasks) != 0 {
					t.Fatal("history inspection changed execution")
				}
			}
		})
	}
}

func checkHistoryAdmissionHidden(t *testing.T, h *MainHistory, roots []HistoryRef, hidden []string) {
	t.Helper()
	seen := map[HistoryRef]bool{}
	queue := append([]HistoryRef(nil), roots...)
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		data, links := historyReadAll(t, h, ref, 1024, 1)
		raw, _ := json.Marshal(links)
		if strings.Contains(data+string(raw), "HIDDEN_") {
			t.Errorf("admission-private text leaked through %v: %s %s", ref, data, raw)
		}
		for _, id := range hidden {
			if ref.ID == id && (ref.Kind == "trigger" || ref.Kind == "report" || ref.Kind == "thread") {
				t.Errorf("hidden admission reachable as %v", ref)
			}
		}
		for _, link := range links {
			queue = append(queue, link.Ref)
		}
	}
	for _, id := range hidden {
		for _, kind := range []string{"trigger", "report", "thread"} {
			page, err := h.Read(t.Context(), HistoryRequest{Ref: HistoryRef{Domain: h.domain, Kind: kind, ID: id}, MaxBytes: 512, MaxItems: 1})
			if !errors.Is(err, errHistoryUnavailable) || !reflect.DeepEqual(page, HistoryPage{}) {
				t.Errorf("guessed hidden %s %s readable: %+v %v", kind, id, page, err)
			}
		}
	}
}

func TestMainHistoryAdmissionPreservesAuthorizedFanout(t *testing.T) {
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "fanout.jsonl"), 2, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, id := range []string{"one", "two"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request", Text: "PUBLIC_" + id})
		waitIdle(t, a)
	}
	for _, task := range a.Status().Tasks {
		if err := a.SetThreadSubscription(task.ID, "git", "pr", true); err != nil {
			t.Fatal(err)
		}
	}
	policy := AdmissionPolicy{Version: "allow", Rules: []AdmissionRule{{Source: "git", Principal: "bot"}}}
	var accepted, hidden []string
	for _, association := range []string{"task", "thread"} {
		for _, allowed := range []bool{false, true} {
			e := AdmissionEnvelope{Trigger: Trigger{ID: fmt.Sprintf("%s-%v", association, allowed), Source: "git", Thread: "pr", Kind: "signal", Text: "PUBLIC_signal"}, Principal: "bot"}
			if association == "task" {
				e.TaskID = "task-1"
			} else {
				e.ToThread = true
			}
			if !allowed {
				e.Principal, e.Trigger.Text = "stranger", "HIDDEN_association"
			}
			r, err := a.Admit(policy, e)
			if err != nil {
				t.Fatal(err)
			}
			if allowed {
				accepted = append(accepted, r.DeliveryIDs...)
			} else {
				hidden = append(hidden, r.DeliveryIDs...)
			}
			waitIdle(t, a)
		}
	}
	a.mu.Lock()
	h := a.newMainHistory()
	a.mu.Unlock()
	defer h.close()
	roots := []HistoryRef{{Domain: h.domain, Kind: "task", ID: "task-1"}, {Domain: h.domain, Kind: "task", ID: "task-2"}}
	checkHistoryAdmissionHidden(t, h, roots, hidden)
	if len(accepted) != 3 {
		t.Fatal("authorized fanout was not preserved")
	}
	for _, id := range accepted {
		data, links := historyReadAll(t, h, HistoryRef{Domain: h.domain, Kind: "report", ID: id}, 4096, 32)
		var report Report
		if err := json.Unmarshal([]byte(data), &report); err != nil || report.Status != "completed" || report.TaskID == "" {
			t.Fatalf("authorized report lost: %s %v", data, err)
		}
		historyLink(t, links, "task", report.TaskID)
		historyLink(t, links, "trigger", "")
	}
}

func TestMainHistoryAdmissionCacheAndContinuations(t *testing.T) {
	a := continuationCatalog(t)
	for i := 1; i < 4; i++ {
		a.events = append(a.events, item{admission: i, trigger: Trigger{Source: "im", Thread: "chat", Text: "PUBLIC_" + strings.Repeat("x", 128)}})
	}
	first := a.newMainHistory()
	defer first.close()
	thread := HistoryRef{Domain: a.main.id, Kind: "thread", ID: "delivery-1"}
	p, err := first.Read(t.Context(), HistoryRequest{Ref: thread, MaxBytes: 512, MaxItems: 1})
	if err != nil || p.Next == "" {
		t.Fatalf("thread continuation: %+v %v", p, err)
	}
	index := len(a.events)
	a.events = append(a.events, item{admission: index, trigger: Trigger{Source: "im", Thread: "chat", Text: "HIDDEN_cache"}, admissionRecord: &AdmissionRecord{Decision: "ignored"}, task: "private-task"})
	// Even a descendant without its own record must use the root's decision.
	a.events = append(a.events, item{admission: index, trigger: a.events[index].trigger, task: "private-task"})
	a.tasks["private-task"] = &task{summary: "HIDDEN_task"}
	a.reports = append(a.reports, Report{DeliveryID: deliveryID(index + 1), TaskID: "private-task", Text: "HIDDEN_report"})
	next := a.newMainHistory()
	defer next.close()
	if !reflect.DeepEqual(first.nodes, next.nodes) {
		t.Fatal("hidden records changed the public graph or its revisions")
	}
	req := HistoryRequest{Ref: thread, Cursor: p.Next, MaxBytes: 512, MaxItems: 1}
	oldPage, oldErr := first.Read(t.Context(), req)
	newPage, newErr := next.Read(t.Context(), req)
	if oldErr != nil || newErr != nil || !reflect.DeepEqual(oldPage, newPage) {
		t.Fatalf("hidden append changed a valid continuation: %v %v", oldErr, newErr)
	}
	for _, event := range []int{index, index + 1, -1, len(a.events)} {
		if a.historyVisible(event) {
			t.Fatalf("private or unknown event visible: %d", event)
		}
	}
	a.accepted = map[[2]string]int{{"im", "hidden"}: index}
	for _, id := range []string{"hidden", "missing"} {
		input := MainInput{Trigger: Trigger{Source: "im", ID: id}, HistoryRefs: []HistoryRef{thread}}
		a.addHistoryRefs(&input)
		if len(input.HistoryRefs) != 0 {
			t.Fatalf("offered refs for private or unknown input: %+v", input)
		}
	}
}

func TestMainHistoryAdmissionLegacyJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	a, err := Open(t.Context(), path, 1, func(context.Context, Trigger, []TaskInfo) (Route, error) {
		return Route{Action: "ignore"}, nil
	}, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for i, kind := range []string{"noise", "signal"} {
		submit(t, a, Trigger{ID: fmt.Sprint(i), Source: "im", Thread: "chat", Kind: kind, Text: map[string]string{"noise": "HIDDEN_legacy", "signal": "PUBLIC_legacy"}[kind]})
	}
	waitIdle(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 1, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.mu.Lock()
	h := b.newMainHistory()
	b.mu.Unlock()
	defer h.close()
	ref := HistoryRef{Domain: h.domain, Kind: "trigger", ID: "delivery-2"}
	checkHistoryAdmissionHidden(t, h, []HistoryRef{ref}, []string{"delivery-1"})
	data, _ := historyReadAll(t, h, ref, 4096, 32)
	if !strings.Contains(data, "PUBLIC_legacy") || len(b.ReportsSince(0)) != 2 {
		t.Fatal("upgrade lost privileged public history or host audit")
	}
}

func TestPiMainHistoryAdmissionToolBoundary(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%v", batch), func(t *testing.T) {
			var thread HistoryRef
			var inputID string
			calls := 0
			def := agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
				calls++
				raw, _ := json.Marshal(req.Messages)
				if strings.Contains(string(raw), "HIDDEN_") {
					t.Error("native model received admission-private text")
				}
				if calls == 1 {
					return historyToolAnswer(thread, "", 4096), nil
				}
				page := lastHistoryPage(t, req)
				if calls == 2 {
					if len(page.Links) != 1 || page.Links[0].Ref.ID != "delivery-2" {
						return nil, fmt.Errorf("hidden or missing thread links: %+v", page)
					}
					return historyToolAnswer(page.Links[0].Ref, "", 4096), nil
				}
				if !strings.Contains(page.Data, "PUBLIC_") {
					return nil, fmt.Errorf("wrong public trigger: %+v", page)
				}
				if batch {
					return offlineAnswer(`{"decisions":[{"input_id":"` + inputID + `","decision":{"action":"ignore"}}]}`), nil
				}
				return offlineAnswer(`{"action":"ignore"}`), nil
			})}
			path := filepath.Join(t.TempDir(), "native.jsonl")
			var a *SuperAgent
			var err error
			if batch {
				infer, e := PiMainBatchHistoryRouter(def, 1<<20)
				if e != nil {
					t.Fatal(e)
				}
				a, err = OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
					thread, inputID = job.Inputs[0].HistoryRefs[1], job.Inputs[0].ID
					return infer(ctx, job)
				}, mainWorker)
			} else {
				infer, e := PiMainHistoryRouter(def, 1<<20)
				if e != nil {
					t.Fatal(e)
				}
				a, err = OpenMain(t.Context(), path, 1, func(ctx context.Context, job MainJob) (MainOutcome, error) {
					thread = job.Input.HistoryRefs[1]
					return infer(ctx, job)
				}, mainWorker)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			e := AdmissionEnvelope{Trigger: Trigger{ID: "denied", Source: "im", Thread: "chat", Kind: "request", Text: "HIDDEN_native"}, Principal: "stranger", ExplicitRequest: true}
			if _, err := a.Admit(AdmissionPolicy{Version: "deny"}, e); err != nil {
				t.Fatal(err)
			}
			submit(t, a, Trigger{ID: "allowed", Source: "im", Thread: "chat", Kind: "request", Text: "PUBLIC_native"})
			waitIdle(t, a)
			if calls != 3 || len(a.MainFailures()) != 0 {
				t.Fatalf("native history flow failed: calls=%d failures=%+v", calls, a.MainFailures())
			}
			if bytes.Contains(a.main.snapshot, []byte("HIDDEN_")) || !bytes.Contains(a.main.snapshot, []byte("history_read")) {
				t.Fatal("persisted snapshot leaked input or did not exercise native history")
			}
		})
	}
}
