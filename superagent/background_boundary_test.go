package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/agent/session"
)

type scriptedBackground struct {
	start   func(context.Context, background.Request) error
	inspect func(context.Context, string) (background.Observation, error)
	stop    func(context.Context, string) error
}

func (b scriptedBackground) Start(c context.Context, r background.Request) error {
	if b.start != nil {
		return b.start(c, r)
	}
	return nil
}
func (b scriptedBackground) Inspect(c context.Context, id string) (background.Observation, error) {
	if b.inspect != nil {
		return b.inspect(c, id)
	}
	return background.Observation{ID: id, State: "unknown"}, nil
}
func (b scriptedBackground) Stop(c context.Context, id string) error {
	if b.stop != nil {
		return b.stop(c, id)
	}
	return nil
}

func TestBackgroundReservationBeforeDispatchAndStopBeforeAcknowledgment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "before.jsonl")
	entered, release := make(chan string, 1), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var a *SuperAgent
	var mu sync.Mutex
	stopped := false
	b := scriptedBackground{start: func(_ context.Context, r background.Request) error {
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if !bytes.Contains(raw, []byte(`"state":"background_reserved"`)) || !bytes.Contains(raw, []byte(r.ID)) || !bytes.Contains(raw, []byte(`"scope":"workspace-A"`)) {
			return errors.New("dispatch before durable reservation")
		}
		if len(a.BackgroundOperations()) != 1 {
			return errors.New("reservation not visible outside lock")
		}
		entered <- r.ID
		<-release
		return nil
	}, stop: func(context.Context, string) error { mu.Lock(); stopped = true; mu.Unlock(); return nil }, inspect: func(_ context.Context, id string) (background.Observation, error) {
		mu.Lock()
		defer mu.Unlock()
		state := "running"
		if stopped {
			state = "exited"
		}
		return background.Observation{ID: id, State: state, Failed: stopped}, nil
	}}
	result := make(chan error, 1)
	work := func(ctx context.Context, j Job) (Outcome, error) {
		_, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{}`)})
		result <- e
		return mainWorker(ctx, j)
	}
	var err error
	a, err = OpenMainBackground(t.Context(), path, 1, bgMain, work, backgroundOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	var id string
	select {
	case id = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch missing")
	}
	if err = a.StopBackground(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err = <-result; err != nil {
		t.Fatalf("acknowledged start rejected after stop: %v", err)
	}
	final := waitBG(t, a, func(o BackgroundOperation) bool { return o.Exited })
	waitIdle(t, a)
	if !final.Started || !final.StopRequested || !final.NeedsReview {
		t.Fatalf("stop facts: %+v", final)
	}
}

func TestBackgroundAuthorizationQuotaAndStaleCapabilities(t *testing.T) {
	b := newBarrierBackground()
	defer func() {
		select {
		case <-b.release:
		default:
			close(b.release)
		}
	}()
	caps := make(chan *BackgroundControl, 3)
	errs := make(chan error, 3)
	opts := backgroundOptions(b)
	opts.Tools[0].Authorize = func(_ context.Context, auth BackgroundAuthorization) error {
		if strings.Contains(string(auth.Start.Arguments), "deny") {
			return errors.New("denied")
		}
		return nil
	}
	work := func(ctx context.Context, j Job) (Outcome, error) {
		caps <- j.Background
		arg := `{}`
		if j.Trigger.ID == "denied" {
			arg = `{"deny":true}`
		}
		_, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "same-id", Arguments: json.RawMessage(arg)})
		errs <- e
		return mainWorker(ctx, j)
	}
	a, e := OpenMainBackground(t.Context(), filepath.Join(t.TempDir(), "auth.jsonl"), 2, bgMain, work, opts)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	for _, id := range []string{"denied", "first", "quota"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request", Text: id})
		waitIdle(t, a)
		err := <-errs
		cap := <-caps
		if id == "first" && err != nil || id != "first" && err == nil {
			t.Fatalf("authorization/quota %s: %v", id, err)
		}
		if _, e = cap.Start(t.Context(), BackgroundStart{Tool: "slow", CallID: "stale", Arguments: json.RawMessage(`{}`)}); e == nil {
			t.Fatal("stale launch capability")
		}
		if id != "denied" {
			op := a.BackgroundOperations()[0]
			if _, e = cap.Stop(t.Context(), op.ID); e == nil {
				t.Fatal("stale stop capability")
			}
		}
	}
	if len(a.BackgroundOperations()) != 1 || b.calls != 1 {
		t.Fatal("denial or quota crossed dispatch")
	}
}

func TestBackgroundLargeReceiptsPrivateAndDetached(t *testing.T) {
	var release bool
	var mu sync.Mutex
	b := scriptedBackground{inspect: func(_ context.Context, id string) (background.Observation, error) {
		mu.Lock()
		defer mu.Unlock()
		if !release {
			return background.Observation{ID: id, State: "running"}, nil
		}
		o := background.Observation{ID: id, State: "exited", Output: json.RawMessage(`{"private":"NEVER PUBLIC"}`)}
		for i := uint64(1); i <= 32; i++ {
			o.Receipts = append(o.Receipts, background.Receipt{Token: i, Domain: "agent", CallID: strings.Repeat("x", 4096), ToolName: "slow", Committed: true, Completed: true})
		}
		return o, nil
	}}
	opid := make(chan string, 1)
	work := func(ctx context.Context, j Job) (Outcome, error) {
		if j.Trigger.ID == "first" {
			o, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{"private":"SECRET ARG"}`)})
			if e != nil {
				return Outcome{}, e
			}
			opid <- o.ID
		}
		return mainWorker(ctx, j)
	}
	path := filepath.Join(t.TempDir(), "large.jsonl")
	a, e := OpenMainBackground(t.Context(), path, 1, bgMain, work, backgroundOptions(b))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	id := <-opid
	waitIdle(t, a)
	mu.Lock()
	release = true
	mu.Unlock()
	waitBG(t, a, func(o BackgroundOperation) bool { return o.Exited })
	waitIdle(t, a)
	reports := a.ReportsSince(0)
	found := false
	for _, r := range reports {
		raw, _ := json.Marshal(r)
		if bytes.Contains(raw, []byte("NEVER PUBLIC")) || bytes.Contains(raw, []byte("SECRET ARG")) {
			t.Fatal("private data exposed")
		}
		if r.Background != nil {
			found = true
			r.Background.OperationID = "forged"
			r.Background.Receipts[0].Committed = false
		}
	}
	if !found {
		t.Fatal("missing authoritative completion report")
	}
	for _, r := range a.ReportsSince(0) {
		if r.Background != nil && (r.Background.OperationID != id || !r.Background.Receipts[0].Committed) {
			t.Fatal("mutable report aliases")
		}
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	restored, e := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, backgroundOptions(b))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	waitIdle(t, restored)
	if len(restored.BackgroundOperations()) != 1 || len(restored.ReportsSince(0)) != 2 {
		t.Fatal("replay duplicated completion")
	}
}

func TestBackgroundRecoveryEveryReservedJournalBoundary(t *testing.T) {
	b := newBarrierBackground()
	starts := make(chan string, 1)
	work := func(ctx context.Context, j Job) (Outcome, error) {
		if j.Trigger.ID == "first" {
			o, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{}`)})
			if e != nil {
				return Outcome{}, e
			}
			starts <- o.ID
		}
		return mainWorker(ctx, j)
	}
	path := filepath.Join(t.TempDir(), "source.jsonl")
	a, e := OpenMainBackground(t.Context(), path, 1, bgMain, work, backgroundOptions(b))
	if e != nil {
		t.Fatal(e)
	}
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	id := <-starts
	waitIdle(t, a)
	if e = a.StopBackground(t.Context(), id); e != nil {
		t.Fatal(e)
	}
	close(b.release)
	waitBG(t, a, func(o BackgroundOperation) bool { return o.Exited })
	waitIdle(t, a)
	if e = a.ReconcileBackground(id, "Verified real exit and committed write."); e != nil {
		t.Fatal(e)
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var prefix []byte
	reserved := false
	for _, line := range bytes.SplitAfter(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		prefix = append(prefix, line...)
		var log session.LogItem
		if json.Unmarshal(line, &log) != nil || log.Entry == nil || log.Entry.Custom == nil {
			continue
		}
		var tr transition
		if json.Unmarshal(log.Entry.Custom.Payload, &tr) != nil {
			t.Fatal("decode")
		}
		reserved = reserved || tr.State == "background_reserved"
		if !reserved {
			continue
		}
		t.Run(tr.State+log.Entry.ID, func(t *testing.T) {
			cut := filepath.Join(t.TempDir(), "cut.jsonl")
			if e = os.WriteFile(cut, prefix, 0600); e != nil {
				t.Fatal(e)
			}
			noStart := scriptedBackground{start: func(context.Context, background.Request) error {
				t.Error("replayed Start")
				return errors.New("must not dispatch")
			}}
			c, err := OpenMainBackground(t.Context(), cut, 1, bgMain, mainWorker, backgroundOptions(noStart))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			waitIdle(t, c)
			ops := c.BackgroundOperations()
			if len(ops) != 1 || ops[0].ID != id {
				t.Fatal("operation identity changed")
			}
			if !ops[0].Exited && !ops[0].NeedsReview {
				t.Fatal("unverified restart released barrier")
			}
			count := 0
			for _, r := range c.ReportsSince(0) {
				if r.Source == backgroundSource {
					count++
				}
			}
			if count > 1 {
				t.Fatal("duplicate completion")
			}
		})
	}
}

func TestBackgroundKnownBackendRestartResumesObservationOnly(t *testing.T) {
	b := newBarrierBackground()
	defer func() {
		select {
		case <-b.release:
		default:
			close(b.release)
		}
	}()
	starts := make(chan string, 1)
	work := func(ctx context.Context, j Job) (Outcome, error) {
		o, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{}`)})
		starts <- o.ID
		if e != nil {
			return Outcome{}, e
		}
		return mainWorker(ctx, j)
	}
	path := filepath.Join(t.TempDir(), "known.jsonl")
	a, e := OpenMainBackground(t.Context(), path, 1, bgMain, work, backgroundOptions(b))
	if e != nil {
		t.Fatal(e)
	}
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	id := <-starts
	waitIdle(t, a)
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	c, e := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, backgroundOptions(b))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	observed := waitBG(t, c, func(o BackgroundOperation) bool { return o.Status == "running" && !o.NeedsReview })
	if observed.ID != id {
		t.Fatal("identity changed")
	}
	close(b.release)
	waitBG(t, c, func(o BackgroundOperation) bool { return o.Exited })
	waitIdle(t, c)
	if b.calls != 1 || len(c.ReportsSince(0)) != 2 {
		t.Fatal("replayed Start or completion")
	}
}

func TestBackgroundPartialCommitSurvivesUnknownAndRejectsRegression(t *testing.T) {
	var mu sync.Mutex
	mode := 0
	receipt := background.Receipt{Token: 1, Domain: "agent", CallID: "call", ToolName: "slow", Committed: true}
	b := scriptedBackground{inspect: func(_ context.Context, id string) (background.Observation, error) {
		mu.Lock()
		defer mu.Unlock()
		o := background.Observation{ID: id, State: "running"}
		switch mode {
		case 1:
			o.Receipts = []background.Receipt{receipt}
		case 2:
			o.State = "unknown"
		case 3:
			o.State = "exited"
		case 4:
			r := receipt
			r.Completed = true
			o.State = "exited"
			o.Receipts = []background.Receipt{r}
		}
		return o, nil
	}}
	started := make(chan string, 1)
	work := func(ctx context.Context, j Job) (Outcome, error) {
		if j.Trigger.ID == "first" {
			o, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{}`)})
			if e != nil {
				return Outcome{}, e
			}
			started <- o.ID
		}
		return mainWorker(ctx, j)
	}
	opts := backgroundOptions(b)
	opts.PollInterval = time.Minute
	a, e := OpenMainBackground(t.Context(), filepath.Join(t.TempDir(), "facts.jsonl"), 1, bgMain, work, opts)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	id := <-started
	waitIdle(t, a)
	for _, m := range []int{1, 2, 3, 4} {
		mu.Lock()
		mode = m
		mu.Unlock()
		if e = a.RefreshBackground(t.Context(), id); e != nil {
			t.Fatal(e)
		}
		o := a.BackgroundOperations()[0]
		if len(o.Receipts) != 1 || !o.Receipts[0].Committed {
			t.Fatalf("lost durable commit at mode %d: %+v", m, o)
		}
		if m == 3 && o.Exited {
			t.Fatal("regressed facts released quota")
		}
		if m == 4 && !o.Exited {
			t.Fatal("verified exit not accepted")
		}
	}
	waitIdle(t, a)
}

func TestBackgroundWorkerAbortDoesNotStopOperation(t *testing.T) {
	b := newBarrierBackground()
	defer func() {
		select {
		case <-b.release:
		default:
			close(b.release)
		}
	}()
	started := make(chan string, 1)
	work := func(ctx context.Context, j Job) (Outcome, error) {
		o, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{}`)})
		if e != nil {
			return Outcome{}, e
		}
		started <- o.ID
		<-ctx.Done()
		return Outcome{}, ctx.Err()
	}
	a, e := OpenMainBackground(t.Context(), filepath.Join(t.TempDir(), "abort.jsonl"), 1, bgMain, work, backgroundOptions(b))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	id := <-started
	o := a.BackgroundOperations()[0]
	if e = a.Abort(o.Origin); e != nil {
		t.Fatal(e)
	}
	waitIdle(t, a)
	select {
	case <-b.stopped:
		t.Fatal("Worker Abort stopped independent operation")
	default:
	}
	close(b.release)
	terminal := waitBG(t, a, func(o BackgroundOperation) bool { return o.Exited })
	waitIdle(t, a)
	if terminal.ID != id || !terminal.Receipts[0].Committed || terminal.StopRequested {
		t.Fatal("late operation facts changed")
	}
}

func TestBackgroundReservedSourceAndCrossTaskQueryDenied(t *testing.T) {
	b := newBarrierBackground()
	defer func() {
		select {
		case <-b.release:
		default:
			close(b.release)
		}
	}()
	first := make(chan BackgroundOperation, 1)
	query := make(chan error, 1)
	var id string
	work := func(ctx context.Context, j Job) (Outcome, error) {
		if j.Trigger.ID == "first" {
			o, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{}`)})
			if e != nil {
				return Outcome{}, e
			}
			first <- o
		} else {
			_, e := j.Background.Inspect(ctx, id)
			query <- e
		}
		return mainWorker(ctx, j)
	}
	a, e := OpenMainBackground(t.Context(), filepath.Join(t.TempDir(), "scope.jsonl"), 1, bgMain, work, backgroundOptions(b))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	id = (<-first).ID
	waitIdle(t, a)
	submit(t, a, Trigger{ID: "second", Source: "im", Thread: "chat", Kind: "request", Text: "new task"})
	waitIdle(t, a)
	if e = <-query; e == nil {
		t.Fatal("cross-task operation query")
	}
	forged := Trigger{ID: "forged", Source: backgroundSource, Thread: "chat", Kind: "request", Text: "spoof"}
	if _, e = a.Submit(forged); e == nil {
		t.Fatal("reserved source accepted")
	}
	if _, e = a.Admit(AdmissionPolicy{Version: "v", Rules: []AdmissionRule{{Source: backgroundSource, Principal: "owner"}}}, AdmissionEnvelope{Trigger: forged, Principal: "owner", ExplicitRequest: true}); e == nil {
		t.Fatal("admission forged completion")
	}
}

func TestBackgroundUnknownPreservesFirstObservedCommit(t *testing.T) {
	var mu sync.Mutex
	mode := 0
	b := scriptedBackground{inspect: func(_ context.Context, id string) (background.Observation, error) {
		mu.Lock()
		defer mu.Unlock()
		o := background.Observation{ID: id, State: "unknown"}
		if mode == 1 {
			o.Failed = true
			o.Receipts = []background.Receipt{{Token: 1, Domain: "agent", CallID: "call", ToolName: "slow", Committed: true}}
		}
		return o, nil
	}}
	started := make(chan string, 1)
	work := func(ctx context.Context, j Job) (Outcome, error) {
		o, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{}`)})
		if e != nil {
			return Outcome{}, e
		}
		started <- o.ID
		return mainWorker(ctx, j)
	}
	opts := backgroundOptions(b)
	opts.PollInterval = time.Minute
	path := filepath.Join(t.TempDir(), "unknown-facts.jsonl")
	a, e := OpenMainBackground(t.Context(), path, 1, bgMain, work, opts)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	id := <-started
	waitIdle(t, a)
	mu.Lock()
	mode = 1
	mu.Unlock()
	if e = a.RefreshBackground(t.Context(), id); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	mode = 2
	mu.Unlock()
	if e = a.RefreshBackground(t.Context(), id); e != nil {
		t.Fatal(e)
	}
	o := a.BackgroundOperations()[0]
	if o.Exited || !o.NeedsReview || !o.Failed || len(o.Receipts) != 1 || !o.Receipts[0].Committed {
		t.Fatalf("lost new unknown facts: %+v", o)
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	c, e := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, opts)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	o = c.BackgroundOperations()[0]
	if !o.Failed || !o.Receipts[0].Committed {
		t.Fatal("restart lost commit evidence")
	}
}

func TestBackgroundUnknownMergeRetainsParentOrder(t *testing.T) {
	incoming := []background.Receipt{{Token: 99, Domain: "agent", ToolName: "parent"}, {Token: 1, ParentToken: 99, Domain: "agent", ToolName: "child", Committed: true}}
	got, ok := mergeUnknownBackgroundReceipts(nil, incoming)
	if !ok || !validBackgroundObservation(background.Observation{ID: "op", State: "unknown", Receipts: got}, "op") || got[0].Token != 99 || !got[1].Committed {
		t.Fatalf("valid parent order lost: %+v", got)
	}
	old := []background.Receipt{{Token: 99, Domain: "agent", ToolName: "parent", Committed: true}}
	got, ok = mergeUnknownBackgroundReceipts(old, incoming)
	if !ok || len(got) != 2 || !got[0].Committed || !got[1].Committed {
		t.Fatal("partial union lost facts")
	}
}

func TestBackgroundCompletionIgnoresChangedSubscriptions(t *testing.T) {
	b := newBarrierBackground()
	defer func() {
		select {
		case <-b.release:
		default:
			close(b.release)
		}
	}()
	starts := make(chan BackgroundOperation, 1)
	completed := make(chan Job, 2)
	work := func(ctx context.Context, j Job) (Outcome, error) {
		if j.Trigger.ID == "first" {
			o, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "original", Arguments: json.RawMessage(`{}`)})
			if e != nil {
				return Outcome{}, e
			}
			starts <- o
		}
		if j.BackgroundCompletion != nil {
			completed <- j
		}
		return mainWorker(ctx, j)
	}
	a, e := OpenMainBackground(t.Context(), filepath.Join(t.TempDir(), "owner.jsonl"), 2, bgMain, work, backgroundOptions(b))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	op := <-starts
	waitIdle(t, a)
	submit(t, a, Trigger{ID: "second", Source: "im", Thread: "chat", Kind: "request", Text: "another task"})
	waitIdle(t, a)
	tasks := a.Status().Tasks
	other := tasks[1].ID
	if e = a.SetThreadSubscription(op.TaskID, "im", "chat", false); e != nil {
		t.Fatal(e)
	}
	if e = a.SetThreadSubscription(other, "im", "chat", true); e != nil {
		t.Fatal(e)
	}
	close(b.release)
	waitBG(t, a, func(o BackgroundOperation) bool { return o.Exited })
	waitIdle(t, a)
	select {
	case j := <-completed:
		if j.TaskID != op.TaskID || j.BackgroundCompletion.CallID != "original" {
			t.Fatal("completion followed new subscription")
		}
	default:
		t.Fatal("original completion missing")
	}
	select {
	case <-completed:
		t.Fatal("completion fanned out")
	default:
	}
}
