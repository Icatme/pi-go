//go:build linux && (amd64 || arm64)

package device_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/superagent"
	"github.com/Icatme/pi-go/superagent/device"
	"github.com/Icatme/pi-go/superagent/sharedsqlite"
)

type durableWorker struct {
	mu          sync.Mutex
	completions []superagent.BackgroundCompletion
	startErr    error
	quotaDenied bool
	followups   int
}

func (f *durableChain) coordinator(journal *sharedsqlite.Journal, client *device.Client, mode string, observed *durableWorker) *superagent.SuperAgent {
	f.t.Helper()
	worker := func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
		raw, _ := json.Marshal(job)
		if bytes.Contains(raw, []byte(durableCanary)) {
			return superagent.Outcome{}, errors.New("private native result reached Worker")
		}
		observed.mu.Lock()
		defer observed.mu.Unlock()
		switch job.Trigger.ID {
		case "launch":
			args, _ := json.Marshal(map[string]string{"mode": mode})
			_, observed.startErr = job.Background.Start(ctx, superagent.BackgroundStart{Tool: "work", CallID: "original-call", Arguments: args})
		case "quota-probe":
			_, err := job.Background.Start(ctx, superagent.BackgroundStart{Tool: "work", CallID: "quota-call", Arguments: json.RawMessage(`{}`)})
			observed.quotaDenied = err != nil
		case "followup":
			observed.followups++
		}
		if job.BackgroundCompletion != nil {
			observed.completions = append(observed.completions, *job.BackgroundCompletion)
		}
		return superagent.Outcome{Summary: "handled", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}, nil
	}
	router := func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
		action := "ignore"
		if j.Input.Kind == "user" {
			action = "new"
		}
		return superagent.MainOutcome{Decision: superagent.MainDecision{Action: action}, Snapshot: j.Snapshot}, nil
	}
	opts := superagent.BackgroundOptions{MaxConcurrent: 1, PollInterval: time.Minute, Tools: []superagent.BackgroundTool{{Name: "work", Scope: client.Scope(), PolicyRevision: durableBinding.PolicyRevision, Backend: client, Authorize: func(_ context.Context, a superagent.BackgroundAuthorization) error {
		if journal.Context().Err() != nil || a.Scope != client.Scope() || a.DeliveryID == "" || a.Target.TaskID == "" {
			return errors.New("current coordinator provenance required")
		}
		return nil
	}}}}
	a, err := superagent.OpenMainJournal(f.ctx, journal, superagent.MainJournalOptions{Parallel: 1, Router: router, Worker: worker, Background: &opts})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = a.Close() })
	return a
}
func (f *durableChain) submit(a *superagent.SuperAgent, id string) {
	f.t.Helper()
	if _, err := a.Submit(superagent.Trigger{ID: id, Source: "fixture", Thread: id, Kind: "request", Text: "fixture input"}); err != nil {
		f.t.Fatal(err)
	}
	if err := a.WaitIdle(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}
func (f *durableChain) refresh(a *superagent.SuperAgent, id string) superagent.BackgroundOperation {
	f.t.Helper()
	if err := a.RefreshBackground(f.ctx, id); err != nil {
		f.t.Fatal(err)
	}
	for _, o := range a.BackgroundOperations() {
		if o.ID == id {
			return o
		}
	}
	f.t.Fatal("lost original identity")
	return superagent.BackgroundOperation{}
}
func (f *durableChain) terminal(c *device.Client, id string) background.Observation {
	f.t.Helper()
	var o background.Observation
	f.wait(func() bool {
		var err error
		o, err = c.Inspect(f.ctx, id)
		if err != nil {
			f.t.Fatal(err)
		}
		return o.State == "exited"
	})
	return o
}
func (f *durableChain) assertPrivate(a *superagent.SuperAgent, j *sharedsqlite.Journal, o superagent.BackgroundOperation) {
	f.t.Helper()
	snapshot, _, err := a.TaskSnapshot(o.TaskID)
	if err != nil {
		f.t.Fatal(err)
	}
	for name, value := range map[string]any{"journal": j.Log(), "snapshot": snapshot, "operations": a.BackgroundOperations(), "reports": a.ReportsSince(0), "replies": a.Replies()} {
		raw, _ := json.Marshal(value)
		if bytes.Contains(raw, []byte(durableCanary)) {
			f.t.Fatal("canary leaked to", name)
		}
	}
	f.mu.Lock()
	raw := append([]byte(nil), f.wire.Bytes()...)
	f.mu.Unlock()
	if bytes.Contains(raw, []byte(durableCanary)) {
		f.t.Fatal("private output leaked to HTTP")
	}
	raw, err = os.ReadFile(filepath.Join(f.dir, "device.jsonl"))
	if err != nil {
		f.t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(durableCanary)) {
		f.t.Fatal("private output leaked to device registry")
	}
}

func TestDurableChainEndpointCrashSurvivesWithoutRedispatch(t *testing.T) {
	for _, lostAck := range []bool{false, true} {
		t.Run(map[bool]string{false: "acknowledged", true: "lost_ack"}[lostAck], func(t *testing.T) {
			f := newDurableChain(t, "")
			j := f.current
			drop := ""
			if lostAck {
				drop = "after"
			}
			c := f.client(j.Epoch(), "start", drop)
			seen := &durableWorker{}
			a := f.coordinator(j, c, "hold", seen)
			f.submit(a, "launch")
			original := a.BackgroundOperations()[0]
			dir := f.entered()
			seen.mu.Lock()
			startErr := seen.startErr
			seen.mu.Unlock()
			if (startErr != nil) != lostAck {
				t.Fatalf("lost acknowledgment result: %v", startErr)
			}
			if original.CallID != "original-call" || original.Scope != c.Scope() {
				t.Fatal("original provenance changed")
			}
			if !lostAck {
				if _, err := a.SubmitTask(superagent.Trigger{ID: "followup", Source: "fixture", Thread: "launch", Kind: "request", Text: "while helper runs"}, original.TaskID); err != nil {
					t.Fatal(err)
				}
				if err := a.WaitIdle(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			f.p.kill(t)
			f.startEndpoint(false, "")
			current := f.client(j.Epoch(), "", "")
			pending, err := current.Inspect(f.ctx, original.ID)
			if err != nil || pending.State != "unknown" {
				t.Fatalf("restart invented liveness: %+v %v", pending, err)
			}
			if durableExists(filepath.Join(dir, "finished")) {
				t.Fatal("endpoint death terminated live helper")
			}
			// The coordinator still points at the dead endpoint. No automatic
			// fallback/rebinding occurs: host replacement uses exactly the same grant.
			pendingOp := f.refresh(a, original.ID)
			if !pendingOp.NeedsReview || pendingOp.Exited {
				t.Fatal("offline operation lost review/quota")
			}
			if err := a.ReconcileBackground(original.ID, "no proof"); err == nil {
				t.Fatal("unknown reconciled")
			}
			f.submit(a, "quota-probe")
			seen.mu.Lock()
			denied := seen.quotaDenied
			seen.mu.Unlock()
			if !denied {
				t.Fatal("unknown released coordinator quota")
			}
			if err := durableMark(filepath.Join(dir, "release")); err != nil {
				t.Fatal(err)
			}
			native := f.terminal(current, original.ID)
			if len(native.Output) != 0 || len(native.Receipts) != 1 || !native.Receipts[0].Committed || !native.Receipts[0].Completed {
				t.Fatalf("terminal fact projection: %+v", native)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "observation.json"))
			if err != nil || !bytes.Contains(raw, []byte(durableCanary)) {
				t.Fatal("fixture never produced private output", err)
			}
			if err = a.Close(); err != nil {
				t.Fatal(err)
			}
			j = f.nextJournal()
			c = f.client(j.Epoch(), "", "")
			a = f.coordinator(j, c, "hold", seen)
			f.wait(func() bool { return f.refresh(a, original.ID).Exited })
			if err = a.WaitIdle(f.ctx); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				f.refresh(a, original.ID)
			}
			if err = a.WaitIdle(f.ctx); err != nil {
				t.Fatal(err)
			}
			got := a.BackgroundOperations()[0]
			if got.ID != original.ID || got.TaskID != original.TaskID || got.DeliveryID != original.DeliveryID || got.CallID != original.CallID || got.CompletionEventID != original.ID {
				t.Fatal("completion lost original identity")
			}
			seen.mu.Lock()
			completions := append([]superagent.BackgroundCompletion(nil), seen.completions...)
			followups := seen.followups
			seen.mu.Unlock()
			if len(completions) != 1 || completions[0].OperationID != original.ID || len(completions[0].Output) != 0 {
				t.Fatalf("completion was lost, repeated or leaked: %+v", completions)
			}
			if !lostAck && followups != 1 {
				t.Fatal("worker unavailable during native body")
			}
			f.mu.Lock()
			starts := 0
			for _, cmd := range f.commands {
				if cmd.Action == "start" {
					starts++
				}
				if cmd.Action == "inspect" && cmd.ID != original.ID {
					t.Error("recovery inspected another ID")
				}
			}
			f.mu.Unlock()
			if starts != 1 || f.physicalStarts() != 1 {
				t.Fatalf("redispatched: wire=%d physical=%d", starts, f.physicalStarts())
			}
			f.assertPrivate(a, j, got)
		})
	}
}

func TestDurableChainTakeoverFencesOldOwnerAndNativeEpoch(t *testing.T) {
	for _, mode := range []string{"fence", "late"} {
		t.Run(mode, func(t *testing.T) {
			f := newDurableChain(t, "")
			first := f.current
			old := f.client(first.Epoch(), "", "")
			seen := &durableWorker{}
			a := f.coordinator(first, old, mode, seen)
			f.submit(a, "launch")
			original := a.BackgroundOperations()[0]
			dir := f.entered()
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			next := f.nextJournal()
			if next.Epoch() <= first.Epoch() {
				t.Fatal("epoch did not advance")
			}
			// These precede any replacement device command. A persisted maximum
			// alone would still admit the old owner at this point.
			if _, err := old.Describe(f.ctx); !errors.Is(err, device.ErrDenied) {
				t.Fatal("stale describe", err)
			}
			if _, err := old.Inspect(f.ctx, original.ID); !errors.Is(err, device.ErrDenied) {
				t.Fatal("stale inspect", err)
			}
			if err := old.Stop(f.ctx, original.ID); !errors.Is(err, device.ErrDenied) {
				t.Fatal("stale stop", err)
			}
			if err := old.Start(f.ctx, durableRequest(old, "stale-new", mode)); !errors.Is(err, device.ErrDenied) {
				t.Fatal("stale start", err)
			}
			f.p.kill(t)
			f.startEndpoint(false, "")
			current := f.client(next.Epoch(), "", "")
			a = f.coordinator(next, current, mode, seen)
			pending := f.refresh(a, original.ID)
			if pending.Exited || !pending.NeedsReview {
				t.Fatal("reopened helper invented exit")
			}
			if err := a.ReconcileBackground(original.ID, "no exit"); err == nil {
				t.Fatal("unknown reconciled")
			}
			f.submit(a, "quota-probe")
			seen.mu.Lock()
			denied := seen.quotaDenied
			seen.mu.Unlock()
			if !denied {
				t.Fatal("unknown released quota")
			}
			if mode == "late" {
				f.wait(func() bool { return durableExists(filepath.Join(dir, "committed")) })
				if err := a.StopBackground(f.ctx, original.ID); err != nil {
					t.Fatal(err)
				}
				if f.refresh(a, original.ID).Exited {
					t.Fatal("cancellation acknowledged as body exit")
				}
			}
			if err := durableMark(filepath.Join(dir, "release")); err != nil {
				t.Fatal(err)
			}
			f.wait(func() bool { return f.refresh(a, original.ID).Exited })
			if err := a.WaitIdle(f.ctx); err != nil {
				t.Fatal(err)
			}
			o := a.BackgroundOperations()[0]
			if len(o.Receipts) != 1 || !o.Receipts[0].Completed || o.Receipts[0].Committed != (mode == "late") || !o.NeedsReview {
				t.Fatalf("lost terminal/review facts: %+v", o)
			}
			if mode == "fence" && (!durableExists(filepath.Join(dir, "denied")) || durableExists(filepath.Join(dir, "committed"))) {
				t.Fatal("old native epoch gained new-owner authority")
			}
			epochs, err := os.ReadFile(filepath.Join(dir, "authorized-epochs"))
			if err != nil {
				t.Fatal(err)
			}
			if len(strings.Fields(string(epochs))) == 0 {
				t.Fatal("missing native authority observations")
			}
			for _, epoch := range strings.Fields(string(epochs)) {
				if epoch != strconv.FormatInt(first.Epoch(), 10) {
					t.Fatal("native callback adopted replacement epoch", epoch)
				}
			}
			if mode == "late" && !o.StopRequested {
				t.Fatal("late commit lost stop intent")
			}
			if err := a.ReconcileBackground(original.ID, "fixture terminal facts reviewed"); err != nil {
				t.Fatal(err)
			}
			if f.physicalStarts() != 1 {
				t.Fatal("takeover relaunched operation")
			}
			f.assertPrivate(a, next, o)
		})
	}
}

func TestDurableChainLostTerminalReceiptSurvivesRestart(t *testing.T) {
	f := newDurableChain(t, "")
	j := f.current
	// Even a delayed initial watcher cannot learn terminal facts before restart.
	c := f.client(j.Epoch(), "inspect", "after")
	seen := &durableWorker{}
	a := f.coordinator(j, c, "hold", seen)
	f.submit(a, "launch")
	original := a.BackgroundOperations()[0]
	dir := f.entered()
	if err := durableMark(filepath.Join(dir, "release")); err != nil {
		t.Fatal(err)
	}
	f.wait(func() bool { return durableExists(filepath.Join(dir, "finished")) })
	// The endpoint observes and persists the committed terminal facts, but the
	// actual HTTP response is deliberately discarded after handling.
	dropped := f.client(j.Epoch(), "inspect", "after")
	if _, err := dropped.Inspect(f.ctx, original.ID); !errors.Is(err, device.ErrUnavailable) {
		t.Fatal("missing terminal receipt loss", err)
	}
	seen.mu.Lock()
	before := len(seen.completions)
	seen.mu.Unlock()
	if a.BackgroundOperations()[0].Exited || before != 0 {
		t.Fatal("terminal evidence reached coordinator before recovery")
	}
	f.p.kill(t)
	f.startEndpoint(false, "")
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	j = f.nextJournal()
	c = f.client(j.Epoch(), "", "")
	a = f.coordinator(j, c, "hold", seen)
	f.wait(func() bool { return f.refresh(a, original.ID).Exited })
	for range 3 {
		f.refresh(a, original.ID)
	}
	if err := a.WaitIdle(f.ctx); err != nil {
		t.Fatal(err)
	}
	o := a.BackgroundOperations()[0]
	if o.CompletionEventID != original.ID || len(o.Receipts) != 1 || !o.Receipts[0].Committed || !o.Receipts[0].Completed {
		t.Fatalf("lost terminal evidence after receipt loss: %+v", o)
	}
	seen.mu.Lock()
	count := len(seen.completions)
	seen.mu.Unlock()
	if count != 1 || f.physicalStarts() != 1 {
		t.Fatal("terminal receipt loss duplicated completion or native execution")
	}
	f.assertPrivate(a, j, o)
}

func TestDurableChainStartCrashBoundariesRemainUnknown(t *testing.T) {
	for _, fault := range []string{"before-start", "after-start", "helper-crash", "http-before"} {
		t.Run(fault, func(t *testing.T) {
			endpointFault := fault
			if fault == "helper-crash" || fault == "http-before" {
				endpointFault = ""
			}
			f := newDurableChain(t, endpointFault)
			j := f.current
			mode := "fence" // Hold the body through takeover until explicit release.
			if fault == "helper-crash" {
				mode = "crash"
			}
			drop := ""
			if fault == "http-before" {
				drop = "before"
			}
			c := f.client(j.Epoch(), "start", drop)
			seen := &durableWorker{}
			a := f.coordinator(j, c, mode, seen)
			f.submit(a, "launch")
			o := a.BackgroundOperations()[0]
			if fault == "after-start" || fault == "helper-crash" {
				f.entered()
			}
			f.p.kill(t)
			f.startEndpoint(false, "")
			current := f.client(j.Epoch(), "", "")
			observed, err := current.Inspect(f.ctx, o.ID)
			if err != nil || observed.State != "unknown" {
				t.Fatalf("uncertain start became absence/exit: %+v %v", observed, err)
			}
			if err = a.Close(); err != nil {
				t.Fatal(err)
			}
			j = f.nextJournal()
			current = f.client(j.Epoch(), "", "")
			a = f.coordinator(j, current, mode, seen)
			if f.refresh(a, o.ID).Exited {
				t.Fatal("recovery invented native exit")
			}
			f.submit(a, "quota-probe")
			seen.mu.Lock()
			denied := seen.quotaDenied
			seen.mu.Unlock()
			if !denied {
				t.Fatal("uncertain start freed quota")
			}
			if err = a.ReconcileBackground(o.ID, "process death is not an exit receipt"); err == nil {
				t.Fatal("unproven exit reconciled")
			}
			want := 0
			if fault == "after-start" || fault == "helper-crash" {
				want = 1
			}
			if f.physicalStarts() != want {
				t.Fatal("incorrect physical dispatch count")
			}
			if fault == "helper-crash" {
				if err = current.Start(f.ctx, durableRequest(current, "replacement", "hold")); err == nil {
					t.Fatal("helper crash freed durable capacity")
				}
				if f.physicalStarts() != 1 {
					t.Fatal("new identity bypassed unknown quota")
				}
			}
		})
	}
}

func TestDurableChainDirectInspectDoesNotRedeliverDeviceStop(t *testing.T) {
	f := newDurableChain(t, "before-stop")
	c := f.client(f.current.Epoch(), "", "")
	r := durableRequest(c, "direct-stop", "late")
	if err := c.Start(f.ctx, r); err != nil {
		t.Fatal(err)
	}
	dir := f.entered()
	if err := c.Stop(f.ctx, r.ID); err == nil {
		t.Fatal("missing stop crash")
	}
	f.p.kill(t)
	f.startEndpoint(false, "")
	c = f.client(f.current.Epoch(), "", "")
	for range 3 {
		o, err := c.Inspect(f.ctx, r.ID)
		if err != nil || o.State != "unknown" {
			t.Fatalf("inspect lost uncertain identity: %+v %v", o, err)
		}
	}
	if durableExists(filepath.Join(dir, "stop.json")) || durableExists(filepath.Join(dir, "committed")) {
		t.Fatal("read-only device inspection implicitly requested stop")
	}
	if err := c.Stop(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	f.wait(func() bool { return durableExists(filepath.Join(dir, "committed")) })
	if err := durableMark(filepath.Join(dir, "release")); err != nil {
		t.Fatal(err)
	}
	o := f.terminal(c, r.ID)
	if !o.Receipts[0].Committed || !o.Receipts[0].Completed || f.physicalStarts() != 1 {
		t.Fatal("explicit same-ID stop lost late completion")
	}
}

func TestDurableChainStopBoundaries(t *testing.T) {
	for _, fault := range []string{"before-stop", "after-stop", "http-before", "http-after"} {
		t.Run(fault, func(t *testing.T) {
			endpointFault := fault
			if fault == "http-before" || fault == "http-after" {
				endpointFault = ""
			}
			f := newDurableChain(t, endpointFault)
			first := f.current
			drop := ""
			if fault == "http-before" {
				drop = "before"
			}
			if fault == "http-after" {
				drop = "after"
			}
			c := f.client(first.Epoch(), "stop", drop)
			seen := &durableWorker{}
			a := f.coordinator(first, c, "late", seen)
			f.submit(a, "launch")
			o := a.BackgroundOperations()[0]
			dir := f.entered()
			if err := a.StopBackground(f.ctx, o.ID); err == nil {
				t.Fatal("missing injected stop failure")
			}
			pending := a.BackgroundOperations()[0]
			if !pending.StopRequested || !pending.NeedsReview || pending.Exited {
				t.Fatal("stop error erased review or claimed exit")
			}
			f.p.kill(t)
			f.startEndpoint(false, "")
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			next := f.nextJournal()
			current := f.client(next.Epoch(), "", "")
			a = f.coordinator(next, current, "late", seen)
			// M4 replay's watcher reissues durable stop intent. M7 Inspect alone
			// deliberately does not authorize or send a new Stop.
			f.wait(func() bool { return durableExists(filepath.Join(dir, "stop.json")) })
			f.wait(func() bool { return durableExists(filepath.Join(dir, "committed")) })
			if f.refresh(a, o.ID).Exited {
				t.Fatal("stop marker implied body exit")
			}
			if err := durableMark(filepath.Join(dir, "release")); err != nil {
				t.Fatal(err)
			}
			f.wait(func() bool { return f.refresh(a, o.ID).Exited })
			if err := a.WaitIdle(f.ctx); err != nil {
				t.Fatal(err)
			}
			got := a.BackgroundOperations()[0]
			if !got.StopRequested || !got.NeedsReview || !got.Receipts[0].Committed || !got.Receipts[0].Completed {
				t.Fatalf("stop lost late facts: %+v", got)
			}
			if err := current.Stop(f.ctx, o.ID); err != nil {
				t.Fatal("terminal duplicate stop", err)
			}
			if again := f.terminal(current, o.ID); !again.Receipts[0].Committed {
				t.Fatal("terminal stop erased commit")
			}
			if f.physicalStarts() != 1 {
				t.Fatal("stop/recovery dispatched again")
			}
		})
	}
	t.Run("prestart_tombstone", func(t *testing.T) {
		f := newDurableChain(t, "")
		c := f.client(f.current.Epoch(), "", "")
		r := durableRequest(c, "never-started", "hold")
		if err := c.Stop(f.ctx, r.ID); err != nil {
			t.Fatal(err)
		}
		f.p.kill(t)
		f.startEndpoint(false, "")
		c = f.client(f.current.Epoch(), "", "")
		if err := c.Start(f.ctx, r); err == nil {
			t.Fatal("stopped identity dispatched")
		}
		o, err := c.Inspect(f.ctx, r.ID)
		if err != nil || o.State != "unknown" || f.physicalStarts() != 0 {
			t.Fatal("tombstone invented exit or execution", err)
		}
	})
}
