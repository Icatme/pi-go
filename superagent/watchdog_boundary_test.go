package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
)

func TestWatchdogAppendFailureAndMixedEnvelope(t *testing.T) {
	a, _ := watchdogAgent(t, mainWorker)
	clock := newWatchdogClock()
	opts := watchdogOptions(clock)
	checkWatchdog(t, a, opts)
	before := a.WatchdogStatus()
	a.mu.Lock()
	mixed := transition{State: "received", Trigger: &Trigger{ID: "evil"}, Watchdog: &watchdogTransition{}}
	if err := a.commit(mixed); err == nil {
		t.Fatal("mixed envelope admitted")
	}
	if err := a.apply(mixed, true); err == nil {
		t.Fatal("mixed replay admitted")
	}
	a.mu.Unlock()
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if _, err := a.CheckWatchdog(t.Context(), opts); err == nil {
		t.Fatal("closed store accepted check")
	}
	if !reflect.DeepEqual(before, a.WatchdogStatus()) || a.fatal == nil {
		t.Fatal("failed append advanced cursor/diagnostics")
	}
}

func TestWatchdogModelFailureAndMemoryRoundTrip(t *testing.T) {
	a, p := watchdogAgent(t, mainWorker)
	submit(t, a, Trigger{ID: "one", Source: "im", Thread: "chat", Kind: "request", Text: "original user objective"})
	waitIdle(t, a)
	clock := newWatchdogClock()
	opts := watchdogOptions(clock)
	opts.Diagnose = func(_ context.Context, j WatchdogJob) (WatchdogOutcome, error) {
		if j.Tasks[0].Goal != "original user objective" || j.Tasks[0].Goal == j.Tasks[0].LastReport {
			t.Error("goal replaced by report")
		}
		return WatchdogOutcome{Snapshot: agent.AgentSnapshot{SessionID: j.SessionID, Metadata: map[string]any{"large": json.Number("1e500"), "integer": json.Number("9007199254740993")}}}, nil
	}
	checkWatchdog(t, a, opts)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), p, 1, bgMain, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	clock.advance(time.Minute)
	opts.Diagnose = func(_ context.Context, j WatchdogJob) (WatchdogOutcome, error) {
		if j.Snapshot.Metadata["large"] != json.Number("1e500") || j.Snapshot.Metadata["integer"] != json.Number("9007199254740993") {
			t.Error("model memory lost numeric fidelity")
		}
		return WatchdogOutcome{Snapshot: j.Snapshot, Diagnoses: []WatchdogDiagnosis{{TaskID: "task-1", Kind: "new", Reason: "start unrelated business"}}}, nil
	}
	bad := checkWatchdog(t, b, opts)
	if bad.ModelStatus != "failed" || bad.Findings[0].Diagnosis != nil {
		t.Fatal("invalid model action accepted")
	}
	clock.advance(time.Minute)
	opts.Diagnose = func(context.Context, WatchdogJob) (WatchdogOutcome, error) { panic("credential-canary") }
	panicResult := checkWatchdog(t, b, opts)
	if panicResult.ModelStatus != "failed" {
		t.Fatal("panic not recorded conservatively")
	}
	raw, _ := os.ReadFile(p)
	if bytes.Contains(raw, []byte("credential-canary")) {
		t.Fatal("private panic leaked")
	}
	if len(b.Status().Tasks) != 1 {
		t.Fatal("invalid model started work")
	}
}

func TestWatchdogOfflineRecheckRetainsUnknownEffectBarrier(t *testing.T) {
	var operation string
	backend := scriptedBackground{inspect: func(_ context.Context, id string) (background.Observation, error) {
		return background.Observation{ID: id, State: "running"}, nil
	}}
	path := t.TempDir() + "/state.jsonl"
	a, err := OpenMainBackground(t.Context(), path, 1, bgMain, func(ctx context.Context, j Job) (Outcome, error) {
		if j.Trigger.Source != backgroundSource {
			o, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{"secret":"private-arguments-canary"}`)})
			operation = o.ID
			if e != nil {
				return Outcome{}, e
			}
		}
		return mainWorker(ctx, j)
	}, backgroundOptions(backend))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "one", Source: "im", Thread: "chat", Kind: "request", Text: "authorized existing task"})
	waitIdle(t, a)
	clock := newWatchdogClock()
	opts := watchdogOptions(clock)
	opts.Probe = func(_ context.Context, o WatchdogOperation) (string, error) {
		if o.ID != operation || o.TaskID != "task-1" {
			t.Fatal("wrong probe target")
		}
		return "offline", nil
	}
	first := checkWatchdog(t, a, opts)
	if first.Findings[0].Class != "device_offline" {
		t.Fatalf("offline class: %+v", first)
	}
	opts.Probe = func(context.Context, WatchdogOperation) (string, error) { return "online", nil }
	clock.advance(time.Minute)
	online := checkWatchdog(t, a, opts)
	if online.Findings[0].Class != "tool_running" || online.Findings[0].PreviousID != first.Findings[0].ID {
		t.Fatal("reconnect did not recheck")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	unknown := scriptedBackground{inspect: func(_ context.Context, id string) (background.Observation, error) {
		return background.Observation{ID: id, State: "unknown"}, nil
	}}
	b, err := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, backgroundOptions(unknown))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	clock.advance(time.Minute)
	opts.Diagnose = func(_ context.Context, j WatchdogJob) (WatchdogOutcome, error) {
		raw, _ := json.Marshal(j)
		if bytes.Contains(raw, []byte("private-arguments-canary")) {
			t.Error("private arguments reached model")
		}
		return WatchdogOutcome{Snapshot: j.Snapshot, Diagnoses: []WatchdogDiagnosis{{TaskID: "task-1", Kind: "drift_suspected", Reason: "model attempts to override wait"}}}, nil
	}
	review := checkWatchdog(t, b, opts)
	if review.Findings[0].Class != "needs_review" || !b.BackgroundOperations()[0].NeedsReview {
		t.Fatal("model cleared unknown effect barrier")
	}
	if err = b.ReconcileBackground(operation, "assumed complete"); err == nil {
		t.Fatal("watchdog invented exit evidence")
	}
	opts.Probe = func(context.Context, WatchdogOperation) (string, error) {
		return "offline", errors.New("network timeout")
	}
	clock.advance(time.Minute)
	probeError := checkWatchdog(t, b, opts)
	if probeError.Findings[0].Task.Operations[0].Connectivity != "unknown" {
		t.Fatal("timeout inferred offline")
	}
}

func TestWatchdogCloseCancelsCallbackAndWaits(t *testing.T) {
	a, _ := watchdogAgent(t, mainWorker)
	clock := newWatchdogClock()
	opts := watchdogOptions(clock)
	entered, release := make(chan struct{}), make(chan struct{})
	opts.Diagnose = func(ctx context.Context, j WatchdogJob) (WatchdogOutcome, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return WatchdogOutcome{Snapshot: j.Snapshot}, nil
	}
	done := make(chan error, 1)
	go func() { _, e := a.CheckWatchdog(t.Context(), opts); done <- e }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- a.Close() }()
	select {
	case <-closed:
		t.Fatal("Close did not wait for diagnostic callback")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("callback cancellation: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if len(a.WatchdogStatus().Checks) != 0 {
		t.Fatal("canceled callback committed")
	}
}

func TestWatchdogIndependentClockAndRollback(t *testing.T) {
	a, _ := watchdogAgent(t, mainWorker)
	clock := newWatchdogClock()
	opts := watchdogOptions(clock)
	called := make(chan struct{}, 8)
	opts.Diagnose = func(_ context.Context, j WatchdogJob) (WatchdogOutcome, error) {
		called <- struct{}{}
		return WatchdogOutcome{Snapshot: j.Snapshot}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.RunWatchdog(ctx, opts) }()
	<-called
	// Wait for the first check to commit before rolling the clock backward.
	for len(a.WatchdogStatus().Checks) == 0 {
		time.Sleep(time.Millisecond)
	}
	clock.advance(-time.Hour)
	select {
	case <-called:
		t.Fatal("clock rollback caused duplicate patrol")
	case <-time.After(10 * time.Millisecond):
	}
	clock.advance(time.Hour + time.Minute)
	select {
	case <-called:
	case err := <-done:
		t.Fatalf("clock loop stopped: %v", err)
	case <-time.After(time.Second):
		t.Fatalf("independent clock did not wake: now=%v state=%+v", clock.Now(), a.WatchdogStatus())
	}
	cancel()
	<-done
}

func TestWatchdogReviewThenReconcileReady(t *testing.T) {
	a, p := watchdogAgent(t, func(_ context.Context, j Job) (Outcome, error) {
		o, _ := mainWorker(context.Background(), j)
		o.NeedsReview = true
		return o, nil
	})
	submit(t, a, Trigger{ID: "one", Source: "im", Thread: "chat", Kind: "request", Text: "original goal"})
	waitIdle(t, a)
	clock := newWatchdogClock()
	opts := watchdogOptions(clock)
	first := checkWatchdog(t, a, opts)
	if first.Findings[0].Class != "needs_review" {
		t.Fatal("review not visible")
	}
	if err := a.Reconcile("task-1", "im", "one", "host verified effects"); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	ready := checkWatchdog(t, a, opts)
	if ready.Findings[0].Class != "settled" || ready.Findings[0].Task.Status != "ready" {
		t.Fatalf("ready: %+v", ready)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), p, 1, bgMain, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	clock.advance(time.Minute)
	checkWatchdog(t, b, opts)
}

func TestWatchdogReplayRejectsInvalidChecks(t *testing.T) {
	a, p := watchdogAgent(t, mainWorker)
	submit(t, a, Trigger{ID: "one", Source: "im", Thread: "chat", Kind: "request", Text: "goal"})
	waitIdle(t, a)
	opts := watchdogOptions(newWatchdogClock())
	opts.Diagnose = func(_ context.Context, j WatchdogJob) (WatchdogOutcome, error) {
		return WatchdogOutcome{Snapshot: j.Snapshot}, nil
	}
	checkWatchdog(t, a, opts)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*transition){
		"missing memory":    func(tr *transition) { tr.Watchdog.Snapshot = nil },
		"unbound memory":    func(tr *transition) { tr.Watchdog.Snapshot = json.RawMessage(`{}`) },
		"foreign memory":    func(tr *transition) { tr.Watchdog.Snapshot = json.RawMessage(`{"sessionId":"other"}`) },
		"cursor":            func(tr *transition) { tr.Watchdog.Check.Cursor = 500 },
		"unsupported class": func(tr *transition) { tr.Watchdog.Check.Findings[0].Class = "new" },
		"evidence mutation": func(tr *transition) { tr.Watchdog.Check.Findings[0].Task.Goal = "forged goal" },
		"forged source with consistent digest": func(tr *transition) {
			task := &tr.Watchdog.Check.Findings[0].Task
			task.GoalDeliveryID = "delivery-999"
			task.Revision = watchdogTaskRevision(*task)
		},
		"forged report with consistent digest": func(tr *transition) {
			task := &tr.Watchdog.Check.Findings[0].Task
			task.LastReport = "invented evidence"
			task.Revision = watchdogTaskRevision(*task)
		},
		"mixed transition": func(tr *transition) { tr.Main = &mainTransition{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
			last := len(lines) - 1
			var line struct {
				Entry struct {
					Custom struct {
						Payload json.RawMessage `json:"payload"`
					} `json:"custom"`
				} `json:"entry"`
			}
			// Preserve the complete wire record, modifying only its custom payload.
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(lines[last], &envelope); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(lines[last], &line); err != nil {
				t.Fatal(err)
			}
			var tr transition
			if err := json.Unmarshal(line.Entry.Custom.Payload, &tr); err != nil {
				t.Fatal(err)
			}
			if tr.State != "watchdog_checked" {
				t.Fatal("fixture last record is not a check")
			}
			mutate(&tr)
			payload, _ := json.Marshal(tr)
			var entry map[string]json.RawMessage
			_ = json.Unmarshal(envelope["entry"], &entry)
			var custom map[string]json.RawMessage
			_ = json.Unmarshal(entry["custom"], &custom)
			custom["payload"] = payload
			entry["custom"], _ = json.Marshal(custom)
			envelope["entry"], _ = json.Marshal(entry)
			lines[last], _ = json.Marshal(envelope)
			path := t.TempDir() + "/bad.jsonl"
			if err := os.WriteFile(path, append(bytes.Join(lines, []byte{'\n'}), '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			b, err := OpenMain(t.Context(), path, 1, bgMain, mainWorker)
			if err == nil {
				b.Close()
				t.Fatal("invalid check replayed")
			}
		})
	}
}

func TestWatchdogModelCannotOverrideWaitFacts(t *testing.T) {
	for _, class := range []string{"settled", "queued", "tool_running", "device_offline", "needs_review"} {
		f := WatchdogFinding{Class: class, Diagnosis: &WatchdogDiagnosis{TaskID: "task-1", Kind: "idle_suspected", Reason: "untrusted model hypothesis"}}
		applyWatchdogHypothesis(&f)
		if f.Class != class {
			t.Fatalf("model replaced %s with %s", class, f.Class)
		}
	}
}
