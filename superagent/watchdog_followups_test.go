package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
)

func followupFixture(t *testing.T) (*SuperAgent, string, *refreshBackend, WatchdogRefreshRequest, WatchdogOptions) {
	t.Helper()
	b := &refreshBackend{}
	path := t.TempDir() + "/followups.jsonl"
	a, err := OpenMainBackground(t.Context(), path, 1, bgMain, refreshWorker, refreshOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	submit(t, a, Trigger{ID: "start", Source: "im", Thread: "chat", Kind: "request", Text: "existing request"})
	waitIdle(t, a)
	waitBG(t, a, func(o BackgroundOperation) bool { return o.Status == "needs_review" })
	opts := watchdogOptions(newWatchdogClock())
	opts.Policy.Interval = time.Millisecond
	c := checkWatchdog(t, a, opts)
	op := a.BackgroundOperations()[0]
	f := c.Findings[0]
	r := WatchdogRefreshRequest{FindingID: f.ID, TaskID: op.TaskID, Revision: f.Task.Revision, OperationID: op.ID, Origin: op.Origin, DeliveryID: op.DeliveryID}
	return a, path, b, r, opts
}

func TestWatchdogFollowupsDueBoundaryAndPending(t *testing.T) {
	a, _, _, r := refreshFixture(t)
	i, err := a.RefreshWatchdog(t.Context(), r, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if got := a.watchdogFollowupsDue(i.RecheckAt - 1); len(got) != 0 {
		t.Fatal("early followup")
	}
	got := a.watchdogFollowupsDue(i.RecheckAt)
	if len(got) != 1 || got[0].Request != r || got[0].ObservedAt != i.RecheckAt || got[0].Class != "needs_review" {
		t.Fatalf("due: %+v", got)
	}
	a.watchdog.Interventions[0].State = "pending"
	if len(a.watchdogFollowupsDue(i.RecheckAt+1)) != 0 {
		t.Fatal("pending resumed")
	}
	a.watchdog.Interventions[0] = i
}

func TestWatchdogFollowupsRuntimeAndRestart(t *testing.T) {
	a, path, b, r, opts := followupFixture(t)
	b.set(background.Observation{}, errors.New("private-followup-error"))
	i, err := a.RefreshWatchdog(t.Context(), r, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.UnixMilli(i.RecheckAt)))
	before := a.BackgroundOperations()
	starts, stops, inspects := b.counts()
	probes, models := 0, 0
	opts.Probe = func(context.Context, WatchdogOperation) (string, error) { probes++; return "unknown", nil }
	opts.Diagnose = func(_ context.Context, j WatchdogJob) (WatchdogOutcome, error) {
		models++
		return WatchdogOutcome{Snapshot: agent.AgentSnapshot{SessionID: j.SessionID}}, nil
	}
	opts.Clock.(*watchdogClock).advance(time.Millisecond)
	checkWatchdog(t, a, opts)
	p := a.WatchdogFollowupsSince(0, 1)
	if len(p.Followups) != 1 || p.Cursor != 1 || p.HasMore || p.Followups[0].Request != r || p.Followups[0].Source != "coordinator_observation" {
		t.Fatalf("page %+v", p)
	}
	if probes != 1 || models != 1 {
		t.Fatalf("extra callbacks %d %d", probes, models)
	}
	s, e, n := b.counts()
	if s != starts || e != stops || n != inspects || !reflect.DeepEqual(before, a.BackgroundOperations()) {
		t.Fatal("followup changed backend")
	}
	p.Followups[0].Operation.State = "forged"
	saved := a.WatchdogFollowupsSince(0, 0)
	assertFollowupPrivacy(t, saved)
	if saved.Followups[0].Operation.State == "forged" {
		t.Fatal("page aliases state")
	}
	if a.backgroundInUse() != 1 {
		t.Fatal("released quota")
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, refreshOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	opts.Clock.(*watchdogClock).advance(time.Millisecond)
	checkWatchdog(t, reopened, opts)
	if !reflect.DeepEqual(saved, reopened.WatchdogFollowupsSince(0, 0)) {
		t.Fatal("replayed or lost followup")
	}
	if len(reopened.WatchdogFollowupsSince(1, 1).Followups) != 0 {
		t.Fatal("exclusive cursor repeated")
	}
	if reopened.WatchdogFollowupsSince(100, 1).Cursor != 100 {
		t.Fatal("rewound future cursor")
	}
}

func TestWatchdogFollowupsValidationAndBatch(t *testing.T) {
	a, _, _, r := refreshFixture(t)
	i, err := a.RefreshWatchdog(t.Context(), r, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Synthetic completed records exercise pure prefix selection independently
	// of the normal task cursor without making any external calls.
	original := a.watchdog.Interventions
	a.watchdog.Interventions = append([]WatchdogIntervention(nil), original...)
	defer func() { a.watchdog.Interventions = original }()
	for n := 0; n < 4; n++ {
		x := i
		x.ID = i.ID + string(rune('a'+n))
		a.watchdog.Interventions = append(a.watchdog.Interventions, x)
	}
	a.watchdog.Interventions[0].State = "pending"
	a.watchdog.Interventions[1].RecheckAt = i.RecheckAt + 10
	due := a.watchdogFollowupsDue(i.RecheckAt)
	if len(due) != 2 || due[0].InterventionID != a.watchdog.Interventions[2].ID || due[1].InterventionID != a.watchdog.Interventions[3].ID {
		t.Fatalf("prefix %+v", due)
	}
	tr := transition{State: "watchdog_checked_followups", Watchdog: &watchdogTransition{Followups: &watchdogFollowupBatch{ObservedAt: i.RecheckAt, Followups: due}}}
	if err = a.validateWatchdogFollowups(tr); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*watchdogFollowupBatch){
		"dropped":   func(b *watchdogFollowupBatch) { b.Followups = b.Followups[:1] },
		"duplicate": func(b *watchdogFollowupBatch) { b.Followups[1] = b.Followups[0] },
		"order":     func(b *watchdogFollowupBatch) { b.Followups[0], b.Followups[1] = b.Followups[1], b.Followups[0] },
		"early":     func(b *watchdogFollowupBatch) { b.ObservedAt-- },
		"origin":    func(b *watchdogFollowupBatch) { b.Followups[0].Request.Origin.EventID = "forged" },
		"class":     func(b *watchdogFollowupBatch) { b.Followups[0].Class = "resolved" },
		"sequence":  func(b *watchdogFollowupBatch) { b.Followups[0].Sequence++ },
		"task":      func(b *watchdogFollowupBatch) { b.Followups[0].Task.LastStatus = "forged" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := cloneWatchdog(tr)
			mutate(bad.Watchdog.Followups)
			if a.validateWatchdogFollowups(bad) == nil {
				t.Fatal("accepted forged batch")
			}
		})
	}
	a.watchdog.followups = due
	defer func() { a.watchdog.followups = nil }()
	next := a.watchdogFollowupsDue(i.RecheckAt + 10)
	if len(next) != 2 || next[0].InterventionID != a.watchdog.Interventions[1].ID || next[0].Sequence != 3 {
		t.Fatalf("remaining %+v", next)
	}
}

func TestWatchdogFollowupObservesExitAndNewInput(t *testing.T) {
	a, _, b, r, opts := followupFixture(t)
	i, err := a.RefreshWatchdog(t.Context(), r, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	// Existing reconciliation commits the exit; followup does not inspect it.
	a.mu.Lock()
	op, _ := a.backgroundOperation(r.OperationID)
	terminal := observedBackground(op, background.Observation{ID: r.OperationID, State: "exited", Output: json.RawMessage(`{"text":"private-followup-output"}`), Receipts: []background.Receipt{{Token: 1, Domain: "agent", CallID: "private-followup-receipt", ToolName: "slow", Completed: true}}}, nil)
	err = a.commit(transition{State: "background_observed", Background: &backgroundTransition{Operation: terminal}})
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	if _, err = a.SubmitTask(Trigger{ID: "later", Source: "im", Thread: "chat", Kind: "request", Text: "new input"}, r.TaskID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	_, _, before := b.counts()
	a.mu.Lock()
	beforeTask := a.watchdogFollowupTask(r.TaskID)
	a.mu.Unlock()
	opts.Diagnose = func(_ context.Context, j WatchdogJob) (WatchdogOutcome, error) {
		if _, err := a.SubmitTask(Trigger{ID: "during-diagnosis", Source: "im", Thread: "chat", Kind: "request", Text: "new committed input during diagnosis"}, r.TaskID); err != nil {
			return WatchdogOutcome{}, err
		}
		waitIdle(t, a)
		return WatchdogOutcome{Snapshot: j.Snapshot}, nil
	}
	time.Sleep(time.Until(time.UnixMilli(i.RecheckAt)))
	opts.Clock.(*watchdogClock).advance(time.Millisecond)
	checkWatchdog(t, a, opts)
	p := a.WatchdogFollowupsSince(0, 0)
	if len(p.Followups) != 1 {
		t.Fatal("missing followup")
	}
	assertFollowupPrivacy(t, p)
	f := p.Followups[0]
	if f.Class != "operation_exited" || f.Request != r || f.Task.LatestInputDeliveryID == r.DeliveryID || f.Task.LastReportID == "" || f.Task.LatestInputDeliveryID == beforeTask.LatestInputDeliveryID {
		t.Fatalf("exit observation %+v", f)
	}
	_, _, after := b.counts()
	if after != before {
		t.Fatal("followup inspected")
	}
	if a.WatchdogStatus().Interventions[0] != i {
		t.Fatal("intervention mutated")
	}
}

func TestWatchdogFollowupFailedCommitAndCancellation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "append"}[fail], func(t *testing.T) {
			a, _, _, r, opts := followupFixture(t)
			i, err := a.RefreshWatchdog(t.Context(), r, refreshGrant)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Until(time.UnixMilli(i.RecheckAt)))
			before := a.WatchdogStatus()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if fail {
				a.mu.Lock()
				a.store = refreshFailStore{Journal: a.store, state: "watchdog_checked_followups"}
				a.mu.Unlock()
			} else {
				opts.Diagnose = func(_ context.Context, j WatchdogJob) (WatchdogOutcome, error) {
					cancel()
					return WatchdogOutcome{Snapshot: j.Snapshot}, nil
				}
			}
			opts.Clock.(*watchdogClock).advance(time.Millisecond)
			if _, err = a.CheckWatchdog(ctx, opts); err == nil {
				t.Fatal("expected failure")
			}
			if !reflect.DeepEqual(before, a.WatchdogStatus()) || len(a.WatchdogFollowupsSince(0, 0).Followups) != 0 {
				t.Fatal("failed check advanced evidence")
			}
		})
	}
}

func assertFollowupPrivacy(t *testing.T, p WatchdogFollowupPage) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-refresh-arguments", "private-followup-error", "private-followup-output", "private-followup-receipt", "runtime_id", "tools_completed", "receipts", "arguments"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("public followup leaked %s", secret)
		}
	}
}

func TestWatchdogFollowupIndependentOfTaskCursor(t *testing.T) {
	a, _, _, r, opts := followupFixture(t)
	for _, id := range []string{"two", "three", "four", "five"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: id, Kind: "request", Text: "another task"})
	}
	waitIdle(t, a)
	opts.Clock.(*watchdogClock).advance(time.Millisecond)
	c := checkWatchdog(t, a, opts)
	if c.Cursor != 2 || len(c.Findings) != 2 {
		t.Fatalf("fixture cursor %+v", c)
	}
	r.FindingID = c.Findings[0].ID
	r.Revision = c.Findings[0].Task.Revision
	i, err := a.RefreshWatchdog(t.Context(), r, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.UnixMilli(i.RecheckAt)))
	opts.Clock.(*watchdogClock).advance(time.Millisecond)
	c = checkWatchdog(t, a, opts)
	if c.Cursor != 4 || len(c.Findings) != 2 || c.Findings[0].Task.TaskID == r.TaskID || c.Findings[1].Task.TaskID == r.TaskID {
		t.Fatal("ordinary task cursor changed")
	}
	p := a.WatchdogFollowupsSince(0, 0)
	if len(p.Followups) != 1 || p.Followups[0].Request != r {
		t.Fatal("due original task skipped by normal cursor")
	}
}

func TestWatchdogFollowupPublicPageBounds(t *testing.T) {
	a, _ := watchdogAgent(t, mainWorker)
	a.mu.Lock()
	for n := 1; n <= 40; n++ {
		a.watchdog.followups = append(a.watchdog.followups, WatchdogFollowup{Sequence: uint64(n), ID: "followup/" + strconv.Itoa(n), Request: WatchdogRefreshRequest{Origin: RunTarget{TaskID: "original"}}, Operation: WatchdogOperation{State: "needs_review"}, Task: WatchdogFollowupTask{RunningOrigin: RunTarget{EventID: "original-run"}}})
	}
	a.mu.Unlock()
	for _, limit := range []int{0, -1, 32, 33, 1000} {
		p := a.WatchdogFollowupsSince(0, limit)
		if len(p.Followups) != 32 || p.Cursor != 32 || !p.HasMore {
			t.Fatalf("limit %d: %+v", limit, p)
		}
		for n, f := range p.Followups {
			if f.Sequence != uint64(n+1) {
				t.Fatal("noncontiguous first page")
			}
		}
		p.Followups[0].Request.Origin.TaskID = "mutated"
		p.Followups[0].Operation.State = "mutated"
		p.Followups[0].Task.RunningOrigin.EventID = "mutated"
		first := a.WatchdogFollowupsSince(0, 1)
		if len(first.Followups) != 1 || first.Cursor != 1 || !first.HasMore || first.Followups[0].Request.Origin.TaskID != "original" || first.Followups[0].Operation.State != "needs_review" || first.Followups[0].Task.RunningOrigin.EventID != "original-run" {
			t.Fatal("page bounds or nested fields not detached")
		}
		tail := a.WatchdogFollowupsSince(p.Cursor, limit)
		if len(tail.Followups) != 8 || tail.Cursor != 40 || tail.HasMore {
			t.Fatalf("tail %+v", tail)
		}
		for n, f := range tail.Followups {
			if f.Sequence != uint64(n+33) {
				t.Fatal("noncontiguous exclusive tail")
			}
		}
		empty := a.WatchdogFollowupsSince(tail.Cursor, limit)
		if len(empty.Followups) != 0 || empty.Cursor != 40 || empty.HasMore {
			t.Fatalf("end %+v", empty)
		}
	}
}

func TestWatchdogFollowupRunningDeliveryReplays(t *testing.T) {
	b := &refreshBackend{}
	started := make(chan struct{})
	worker := func(ctx context.Context, j Job) (Outcome, error) {
		if _, err := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "running-call", Arguments: json.RawMessage(`{}`)}); err != nil {
			return Outcome{}, err
		}
		close(started)
		<-ctx.Done()
		return Outcome{}, ctx.Err()
	}
	a, err := OpenMainBackground(t.Context(), t.TempDir()+"/running.jsonl", 1, bgMain, worker, refreshOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	submit(t, a, Trigger{ID: "original-running", Source: "im", Thread: "chat", Kind: "request", Text: "still running"})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	op := waitBG(t, a, func(o BackgroundOperation) bool { return o.Status == "needs_review" })
	opts := watchdogOptions(newWatchdogClock())
	opts.Policy.Interval = time.Millisecond
	c := checkWatchdog(t, a, opts)
	f := c.Findings[0]
	r := WatchdogRefreshRequest{FindingID: f.ID, TaskID: op.TaskID, Revision: f.Task.Revision, OperationID: op.ID, Origin: op.Origin, DeliveryID: op.DeliveryID}
	i, err := a.RefreshWatchdog(t.Context(), r, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.UnixMilli(i.RecheckAt)))
	opts.Clock.(*watchdogClock).advance(time.Millisecond)
	checkWatchdog(t, a, opts)
	saved := a.WatchdogFollowupsSince(0, 0)
	if len(saved.Followups) != 1 || saved.Followups[0].Task.RunningOrigin != op.Origin || saved.Followups[0].Task.RunningDeliveryID != op.DeliveryID {
		t.Fatalf("running evidence %+v", saved)
	}
	a.mu.Lock()
	if a.tasks[op.TaskID].active == nil {
		a.mu.Unlock()
		t.Fatal("fixture worker no longer active")
	}
	prefix, _, _ := writeGrowthJournal(t, a.store)
	a.mu.Unlock()
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	var reruns atomic.Int32
	reopened, err := OpenMainBackground(t.Context(), prefix, 1, bgMain, func(context.Context, Job) (Outcome, error) {
		reruns.Add(1)
		return Outcome{}, errors.New("original must not rerun")
	}, refreshOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved, reopened.WatchdogFollowupsSince(0, 0)) {
		reopened.Close()
		t.Fatal("replay required activeRun or changed running evidence")
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if reruns.Load() != 0 {
		t.Fatal("replay reran original worker")
	}
}
