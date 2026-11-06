package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/agent/session"
)

func TestWatchdogRefreshRevalidatesAfterAuthorization(t *testing.T) {
	a, _, b, r := refreshFixture(t)
	_, _, before := b.counts()
	_, err := a.RefreshWatchdog(t.Context(), r, func(context.Context, WatchdogRefreshRequest) error {
		clock := newWatchdogClock()
		clock.advance(time.Minute)
		checkWatchdog(t, a, watchdogOptions(clock))
		return nil
	})
	_, _, after := b.counts()
	if err == nil || after != before || len(a.WatchdogStatus().Interventions) != 0 {
		t.Fatal("obsolete host decision dispatched")
	}
}

func TestWatchdogRefreshConcurrentDuplicateAndStaleResult(t *testing.T) {
	a, _, b, r := refreshFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	b.mu.Lock()
	b.inspect = func(context.Context, string) (background.Observation, error) {
		close(entered)
		<-release
		return background.Observation{ID: r.OperationID, State: "exited"}, nil
	}
	b.mu.Unlock()
	var grants atomic.Int32
	grant := func(context.Context, WatchdogRefreshRequest) error { grants.Add(1); return nil }
	result := make(chan WatchdogIntervention, 1)
	errs := make(chan error, 1)
	go func() { o, e := a.RefreshWatchdog(t.Context(), r, grant); result <- o; errs <- e }()
	<-entered
	duplicate, err := a.RefreshWatchdog(t.Context(), r, grant)
	if err != nil || duplicate.State != "pending" || grants.Load() != 1 {
		t.Fatalf("duplicate reservation: %+v %v", duplicate, err)
	}
	clock := newWatchdogClock()
	clock.advance(time.Minute)
	checkWatchdog(t, a, watchdogOptions(clock))
	close(release)
	finished := <-result
	if err = <-errs; err != nil {
		t.Fatal(err)
	}
	if finished.Result != "stale" || a.BackgroundOperations()[0].Exited || !a.BackgroundOperations()[0].NeedsReview {
		t.Fatal("stale authorization applied terminal result")
	}
}

func TestWatchdogRefreshCanceledPendingNeverResumes(t *testing.T) {
	a, path, b, r := refreshFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	b.mu.Lock()
	b.inspect = func(ctx context.Context, id string) (background.Observation, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return background.Observation{ID: id, State: "exited"}, nil
	}
	b.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, e := a.RefreshWatchdog(t.Context(), r, refreshGrant); done <- e }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- a.Close() }()
	select {
	case <-closed:
		t.Fatal("Close did not await Inspect")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("not canceled: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.inspect = nil
	b.mu.Unlock()
	reopened, err := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, refreshOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	o, err := reopened.RefreshWatchdog(t.Context(), r, func(context.Context, WatchdogRefreshRequest) error { t.Error("replayed authorizer"); return nil })
	if err != nil || o.State != "pending" || o.Result != "" || reopened.BackgroundOperations()[0].Exited {
		t.Fatalf("pending replay: %+v %v", o, err)
	}
}

func TestWatchdogRefreshRejectsOldRuntimeFinding(t *testing.T) {
	a, path, b, r := refreshFixture(t)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, refreshOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.RefreshWatchdog(t.Context(), r, refreshGrant); err == nil {
		t.Fatal("old runtime finding authorized dispatch")
	}
	clock := newWatchdogClock()
	clock.advance(time.Minute)
	finding := checkWatchdog(t, reopened, watchdogOptions(clock)).Findings[0]
	r.FindingID = finding.ID
	r.Revision = finding.Task.Revision
	if result, err := reopened.RefreshWatchdog(t.Context(), r, refreshGrant); err != nil || result.Result != "unknown" {
		t.Fatalf("new host decision after restart: %+v %v", result, err)
	}
}

func TestWatchdogRefreshFailureKeepsBarriersAndRedacts(t *testing.T) {
	for _, kind := range []string{"error", "panic", "identity", "receipt-regression", "unknown-partial", "failed-exit"} {
		t.Run(kind, func(t *testing.T) {
			a, _, b, r := refreshFixture(t)
			switch kind {
			case "error":
				b.set(background.Observation{}, errors.New("private-backend-canary"))
			case "panic":
				b.mu.Lock()
				b.inspect = func(context.Context, string) (background.Observation, error) { panic("private-backend-canary") }
				b.mu.Unlock()
			case "identity":
				b.mu.Lock()
				b.inspect = func(context.Context, string) (background.Observation, error) {
					return background.Observation{ID: "foreign", State: "exited"}, nil
				}
				b.mu.Unlock()
			case "receipt-regression":
				b.set(background.Observation{State: "unknown", Receipts: []background.Receipt{{Token: 1, Domain: "agent", ToolName: "private-receipt-canary", Committed: true}}}, nil)
				if err := a.RefreshBackground(t.Context(), r.OperationID); err != nil {
					t.Fatal(err)
				}
				b.set(background.Observation{State: "exited"}, nil)
			case "unknown-partial":
				b.set(background.Observation{State: "unknown", Receipts: []background.Receipt{{Token: 1, Domain: "agent", ToolName: "private-receipt-canary", Committed: true}}}, nil)
			case "failed-exit":
				b.set(background.Observation{State: "exited", Failed: true}, nil)
			}
			result, err := a.RefreshWatchdog(t.Context(), r, refreshGrant)
			if err != nil {
				t.Fatal(err)
			}
			op := a.BackgroundOperations()[0]
			if !op.NeedsReview || !result.Operation.NeedsReview {
				t.Fatal("uncertain/failed result cleared review")
			}
			if kind != "failed-exit" && op.Exited {
				t.Fatal("uncertain result proved exit")
			}
			if kind == "unknown-partial" && (len(op.Receipts) != 1 || !op.Receipts[0].Committed) {
				t.Fatal("lost partial committed fact")
			}
			raw, _ := json.Marshal(a.WatchdogStatus())
			if bytes.Contains(raw, []byte("private-")) {
				t.Fatalf("private state leaked: %s", raw)
			}
			if err = a.ReconcileBackground(r.OperationID, "cannot prove exit"); kind != "failed-exit" && err == nil {
				t.Fatal("uncertainty reconciled")
			}
		})
	}
}

type refreshFailStore struct {
	journalStore
	state string
}

func (s refreshFailStore) AppendEntry(lane string, e session.NewEntry) (session.Entry, error) {
	var tr transition
	if e.Custom != nil {
		_ = json.Unmarshal(e.Custom.Payload, &tr)
	}
	if tr.State == s.state {
		return session.Entry{}, errors.New("fixture write failure")
	}
	return s.journalStore.AppendEntry(lane, e)
}
func TestWatchdogRefreshAppendFailures(t *testing.T) {
	for _, state := range []string{"watchdog_refresh_reserved", "watchdog_refresh_finished"} {
		t.Run(state, func(t *testing.T) {
			a, _, b, r := refreshFixture(t)
			_, _, before := b.counts()
			b.set(background.Observation{State: "exited"}, nil)
			a.mu.Lock()
			a.store = refreshFailStore{a.store, state}
			a.mu.Unlock()
			if _, err := a.RefreshWatchdog(t.Context(), r, refreshGrant); err == nil {
				t.Fatal("failed append succeeded")
			}
			_, _, after := b.counts()
			want := before
			if state == "watchdog_refresh_finished" {
				want++
			}
			if after != want {
				t.Fatalf("calls %d want %d", after, want)
			}
			if a.BackgroundOperations()[0].Exited || !a.BackgroundOperations()[0].NeedsReview {
				t.Fatal("uncommitted terminal facts applied")
			}
			records := a.WatchdogStatus().Interventions
			if state == "watchdog_refresh_reserved" && len(records) != 0 || state == "watchdog_refresh_finished" && (len(records) != 1 || records[0].State != "pending") {
				t.Fatalf("uncommitted result: %+v", records)
			}
		})
	}
}

func TestWatchdogRefreshReplayRejectsForgedRecords(t *testing.T) {
	a, path, _, r := refreshFixture(t)
	if _, err := a.RefreshWatchdog(t.Context(), r, refreshGrant); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"result-without-reservation", "duplicate-reservation", "duplicate-result", "mixed", "origin", "result-state", "unknown-clears-review", "followup", "before-reservation-time", "invalid-operation", "check-carries-intervention"} {
		t.Run(kind, func(t *testing.T) {
			lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
			out := make([][]byte, 0, len(lines)+1)
			for _, line := range lines {
				var item session.LogItem
				if err := json.Unmarshal(line, &item); err != nil {
					t.Fatal(err)
				}
				if item.Entry == nil || item.Entry.Custom == nil {
					out = append(out, line)
					continue
				}
				var tr transition
				_ = json.Unmarshal(item.Entry.Custom.Payload, &tr)
				if tr.State == "watchdog_refresh_reserved" {
					if kind == "result-without-reservation" {
						continue
					}
					if kind == "duplicate-reservation" {
						out = append(out, line)
					}
				}
				if tr.State == "watchdog_refresh_finished" {
					i := tr.Watchdog.Intervention
					switch kind {
					case "duplicate-result":
						out = append(out, line)
					case "mixed":
						tr.Background = &backgroundTransition{}
					case "origin":
						i.Request.Origin.EventID = "different"
					case "result-state":
						i.Result = "retry"
					case "unknown-clears-review":
						i.Operation.NeedsReview = false
					case "followup":
						i.RecheckCondition = "retry_automatically"
					case "before-reservation-time":
						i.ObservedAt = 0
						i.RecheckAt = a.watchdog.Policy.Interval.Milliseconds()
					case "invalid-operation":
						tr.Watchdog.Operation = &BackgroundOperation{ID: "unbound"}
					case "check-carries-intervention":
						tr.State = "watchdog_checked"
					}
					item.Entry.Custom.Payload, _ = json.Marshal(tr)
					line, _ = json.Marshal(item)
				}
				out = append(out, line)
			}
			bad := strings.Join([]string{t.TempDir(), "bad.jsonl"}, "/")
			if err := os.WriteFile(bad, append(bytes.Join(out, []byte{'\n'}), '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			b, err := OpenMainBackground(t.Context(), bad, 1, bgMain, mainWorker, refreshOptions(&refreshBackend{}))
			if err == nil {
				b.Close()
				t.Fatal("forged intervention replayed")
			}
		})
	}
}

func TestWatchdogRefreshDoesNotRegressConcurrentObservation(t *testing.T) {
	a, _, b, r := refreshFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	b.mu.Lock()
	b.inspect = func(_ context.Context, id string) (background.Observation, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return background.Observation{ID: id, State: "unknown"}, nil
		}
		return background.Observation{ID: id, State: "exited"}, nil
	}
	b.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := a.RefreshWatchdog(t.Context(), r, refreshGrant); done <- err }()
	<-entered
	if err := a.RefreshBackground(t.Context(), r.OperationID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	before := a.BackgroundOperations()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, a.BackgroundOperations()) || a.WatchdogStatus().Interventions[0].Result != "stale" {
		t.Fatal("late unknown regressed completion")
	}
}

func TestWatchdogRefreshReplayRejectsNewerTaskFacts(t *testing.T) {
	for _, stage := range []string{"reservation", "result"} {
		for _, change := range []string{"admitted-input", "routed-input", "operation-facts"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				a, _, _, r := refreshFixture(t)
				a.mu.Lock()
				defer a.mu.Unlock()
				op, _ := a.backgroundOperation(r.OperationID)
				record := WatchdogIntervention{ID: r.FindingID + "/refresh/" + r.OperationID, Request: r, State: "pending", Operation: publicWatchdogOperation(op), ObservedAt: 1, RecheckAt: 1 + a.watchdog.Policy.Interval.Milliseconds(), RecheckCondition: "inspect_outcome_unrecorded"}
				reservation := transition{State: "watchdog_refresh_reserved", Watchdog: &watchdogTransition{Intervention: &record}}
				if stage == "result" {
					if err := a.apply(reservation, true); err != nil {
						t.Fatal(err)
					}
				}
				// Use actual replay entry points under the coordinator lock. Unlike editing
				// raw lines, this cannot accidentally pass on a structural log-chain error.
				switch change {
				case "admitted-input", "routed-input":
					index := len(a.events)
					trigger := Trigger{ID: "newer", Source: "im", Thread: "chat", Kind: "request", Text: "changed request"}
					if err := a.apply(transition{State: "received", Index: index, Task: r.TaskID, Trigger: &trigger}, true); err != nil {
						t.Fatal(err)
					}
					if change == "routed-input" {
						if err := a.apply(transition{State: "queued", Index: index, Task: r.TaskID}, true); err != nil {
							t.Fatal(err)
						}
					}
				case "operation-facts":
					updated := op
					updated.Status = "running"
					updated.NeedsReview = false
					if err := a.apply(transition{State: "background_observed", Background: &backgroundTransition{Operation: updated}}, true); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "reservation" {
					if err := a.apply(reservation, true); err == nil {
						t.Fatal("replay accepted obsolete reservation")
					}
					return
				}
				terminal := op
				terminal.Status = "exited"
				terminal.Exited = true
				terminal.NeedsReview = false
				terminal.CompletionEventID = op.ID
				record.State = "completed"
				record.Result = "exited"
				record.Operation = publicWatchdogOperation(terminal)
				record.RecheckCondition = "new_finding_and_host_authorization"
				if err := a.apply(transition{State: "watchdog_refresh_finished", Watchdog: &watchdogTransition{Intervention: &record, Operation: &terminal}}, true); err == nil {
					t.Fatal("replay applied obsolete terminal result")
				}
			})
		}
	}
}
