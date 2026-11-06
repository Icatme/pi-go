package runtimeeval

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/superagent"
)

const mixedRounds = 6
const mixedCorrection = "Only B: use the corrected scope."

type mixedCall struct {
	Task, Work, Source, Event string
}

func (c mixedCall) key() string { return c.Task + "/" + c.Source + "/" + c.Event }

type mixedAdmission struct {
	Trigger  superagent.Trigger
	Targets  []string // Frozen expected recipients, never inferred from final reports.
	Accepted bool
}

type mixedCheckpoint struct {
	Tasks      map[string]string
	Admissions []mixedAdmission
	Reports    []superagent.Report
	Replies    []superagent.Reply
	Snapshots  map[string]agent.AgentSnapshot
}

type mixedHarness struct {
	dir, mode                                      string
	crash                                          bool
	entered                                        chan mixedCall
	releaseA, releaseB, routeEntered, routeRelease chan struct{}
	mu                                             sync.Mutex
	calls                                          int
	sawBatch                                       bool
}

func newMixedHarness(dir, mode string, crash bool) *mixedHarness {
	return &mixedHarness{dir: dir, mode: mode, crash: crash, entered: make(chan mixedCall, 2), releaseA: make(chan struct{}), releaseB: make(chan struct{}), routeEntered: make(chan struct{}), routeRelease: make(chan struct{})}
}

func mixedAnswer(value any) agent.AssistantStream {
	raw, _ := json.Marshal(value)
	m := agent.NewTextMessage(agent.RoleAssistant, string(raw))
	m.StopReason = agent.StopReasonStop
	return streamReply(m)
}

func (h *mixedHarness) count(req agent.ModelRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if len(raw) > ContextBytes {
		return errors.New("mixed script byte budget exceeded")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if h.calls > 512 {
		return errors.New("mixed script call budget exceeded")
	}
	return nil
}

func (h *mixedHarness) router(ctx context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
	if err := h.count(req); err != nil {
		return nil, err
	}
	var envelope struct {
		Input  superagent.MainInput   `json:"input"`
		Inputs []superagent.MainInput `json:"inputs"`
		Runs   []superagent.RunTarget `json:"runs"`
	}
	if err := json.Unmarshal([]byte(textOf(req.Messages[len(req.Messages)-1])), &envelope); err != nil {
		return nil, err
	}
	inputs := envelope.Inputs
	if len(inputs) > 1 {
		h.mu.Lock()
		h.sawBatch = true
		h.mu.Unlock()
	}
	if inputs == nil {
		inputs = []superagent.MainInput{envelope.Input}
	}
	var decisions []superagent.MainInputDecision
	for _, input := range inputs {
		d := superagent.MainDecision{Action: "ignore"}
		switch input.Kind {
		case "worker_result":
			if input.Report == nil {
				return nil, errors.New("missing report")
			}
			// Quote facts, including failure/review and commits, rather than turn
			// a returned Worker into an unconditional success claim.
			raw, _ := json.Marshal(input.Report)
			d = superagent.MainDecision{Action: "reply", Text: string(raw)}
		case "user":
			switch input.Trigger.ID {
			case "router-gate":
				close(h.routeEntered)
				select {
				case <-h.routeRelease:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			case "correction":
				var target superagent.RunTarget
				if err := json.Unmarshal([]byte(input.Trigger.Text), &target); err != nil {
					return nil, err
				}
				if !slices.Contains(envelope.Runs, target) {
					return nil, errors.New("correction target not offered")
				}
				d = superagent.MainDecision{Action: "steer", Target: &target, Text: mixedCorrection}
			default:
				if input.Trigger.Kind == "request" {
					d.Action = "new"
				}
			}
		case "task_input", "control_result":
		default:
			return nil, fmt.Errorf("unknown mixed input %s", input.Kind)
		}
		decisions = append(decisions, superagent.MainInputDecision{InputID: input.ID, Decision: d})
	}
	if envelope.Inputs == nil {
		return mixedAnswer(decisions[0].Decision), nil
	}
	slices.Reverse(decisions)
	return mixedAnswer(struct {
		Decisions []superagent.MainInputDecision `json:"decisions"`
	}{decisions}), nil
}

func (h *mixedHarness) model(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
	if err := h.count(req); err != nil {
		return nil, err
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == agent.RoleTool {
		var call mixedCall
		if err := json.Unmarshal([]byte(textOf(last)), &call); err != nil {
			return nil, err
		}
		if h.crash && call.Work == "A" && call.Event == "shared" {
			return nil, errors.New("injected offline model failure after committed write")
		}
		return mixedAnswer(call.Work), nil
	}
	if textOf(last) == mixedCorrection {
		return mixedAnswer("B corrected"), nil
	}
	var tr superagent.Trigger
	if err := json.Unmarshal([]byte(textOf(last)), &tr); err != nil {
		return nil, err
	}
	task, _ := last.Metadata["superagent.trigger_task"].(string)
	var work string
	for _, message := range req.Messages {
		if message.Metadata["superagent.trigger_task"] == task {
			var seed superagent.Trigger
			if err := json.Unmarshal([]byte(textOf(message)), &seed); err != nil {
				return nil, err
			}
			work = seed.Text
			break
		}
	}
	if task == "" || work == "" {
		return nil, errors.New("missing task-scoped seed")
	}
	call := mixedCall{task, work, tr.Source, tr.ID}
	raw, _ := json.Marshal(call)
	m := agent.NewTextMessage(agent.RoleAssistant, "")
	m.StopReason = agent.StopReasonToolUse
	m.ToolCalls = []agent.ToolCall{{ID: "write-" + tr.Source + "-" + tr.ID, Name: "write", Arguments: raw}}
	return streamReply(m), nil
}

func (h *mixedHarness) tool(ctx context.Context, tc agent.ToolExecutionContext) (agent.ToolResult, error) {
	var call mixedCall
	if err := json.Unmarshal(tc.ToolCall.Arguments, &call); err != nil {
		return agent.ToolResult{}, err
	}
	shared := call.Event == "shared"
	if shared && call.Work == "A" {
		h.entered <- call
		if h.crash {
			select {
			case <-h.releaseA:
			case <-ctx.Done():
				return agent.ToolResult{}, ctx.Err()
			}
		} else {
			// Deliberately uncooperative until cancellation: a real local write
			// happens after Abort, and must not be described as undone.
			<-ctx.Done()
		}
	}
	raw, _ := json.Marshal(call)
	// A separate fsynced local file is the effect oracle. These writes do not
	// contact any platform and are not a transaction with the journal.
	h.mu.Lock()
	f, err := os.OpenFile(filepath.Join(h.dir, "effects.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err == nil {
		_, err = f.Write(append(raw, '\n'))
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
	}
	h.mu.Unlock()
	if err != nil {
		return agent.ToolResult{}, err
	}
	if !executionreceipt.ReportLocalCommit(ctx) {
		return agent.ToolResult{}, errors.New("missing native receipt scope")
	}
	if shared && call.Work == "B" {
		h.entered <- call
		select {
		case <-h.releaseB:
		case <-ctx.Done():
			return agent.ToolResult{}, ctx.Err()
		}
	}
	return agent.ToolResult{Content: []agent.Part{agent.NewTextPart(string(raw))}}, nil
}

func (h *mixedHarness) open(t *testing.T, ctx context.Context) *superagent.SuperAgent {
	t.Helper()
	work, err := superagent.PiWorker(agent.AgentDefinition{Model: agent.StreamFunc(h.model), MaxTurns: 3, Tools: []agent.ToolDefinition{{Name: "write", Parameters: schema(`{"type":"object","properties":{"Task":{"type":"string"},"Work":{"type":"string"},"Source":{"type":"string"},"Event":{"type":"string"}},"required":["Task","Work","Source","Event"],"additionalProperties":false}`), Execute: h.tool}}})
	if err != nil {
		t.Fatal(err)
	}
	var a *superagent.SuperAgent
	path := filepath.Join(h.dir, "journal.jsonl")
	def := agent.AgentDefinition{Model: agent.StreamFunc(h.router)}
	if h.mode == loadModes[0] {
		route, e := superagent.PiMainRouter(def, ContextBytes)
		if e != nil {
			t.Fatal(e)
		}
		a, err = superagent.OpenMain(ctx, path, 3, route, work)
	} else {
		route, e := superagent.PiMainBatchRouter(def, ContextBytes)
		if e != nil {
			t.Fatal(e)
		}
		a, err = superagent.OpenMainBatch(ctx, path, 3, superagent.MainBatchOptions{MaxItems: 8, MaxInputBytes: LoadBatchBytes}, route, work)
	}
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mixedAwait(t *testing.T, ctx context.Context, reason string, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("%s: %v", reason, ctx.Err())
		}
	}
}

func mixedReceive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		var zero T
		return zero
	}
}

func mixedReports(a *superagent.SuperAgent, event string) []superagent.Report {
	var out []superagent.Report
	for _, r := range a.Status().Reports {
		if r.EventID == event {
			out = append(out, r)
		}
	}
	return out
}

func mixedEffects(t *testing.T, dir string) []mixedCall {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "effects.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []mixedCall
	s := bufio.NewScanner(f)
	for s.Scan() {
		var c mixedCall
		if err := json.Unmarshal(s.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func mixedSnapshot(t *testing.T, a *superagent.SuperAgent) map[string]agent.AgentSnapshot {
	t.Helper()
	out := map[string]agent.AgentSnapshot{}
	for _, task := range a.Status().Tasks {
		snapshot, ok, err := a.TaskSnapshot(task.ID)
		if err != nil || !ok {
			t.Fatalf("snapshot %s: %v %v", task.ID, ok, err)
		}
		out[task.ID] = snapshot
	}
	return out
}

// The two initial seeds are the only idle boundary. Thereafter B remains in
// flight while admissions, independent completions and A's failure overlap.
func mixedDrive(t *testing.T, ctx context.Context, h *mixedHarness, a *superagent.SuperAgent) mixedCheckpoint {
	t.Helper()
	cp := mixedCheckpoint{Tasks: map[string]string{}}
	submit := func(tr superagent.Trigger, targets []string, duplicate bool) {
		t.Helper()
		var ok bool
		var err error
		if tr.Source == "github" {
			ok, err = a.SubmitThread(tr)
		} else {
			ok, err = a.Submit(tr)
		}
		if err != nil || ok == duplicate {
			t.Fatalf("admit %s duplicate=%v: %v %v", tr.ID, duplicate, ok, err)
		}
		cp.Admissions = append(cp.Admissions, mixedAdmission{tr, slices.Clone(targets), ok})
	}
	for _, work := range []string{"A", "B"} {
		submit(superagent.Trigger{ID: "seed-" + work, Source: "im", Thread: "same-chat", Kind: "request", Text: work}, nil, false)
	}
	if err := a.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	for _, task := range a.Status().Tasks {
		var work string
		if err := json.Unmarshal([]byte(task.Summary), &work); err != nil {
			t.Fatal(err)
		}
		cp.Tasks[work] = task.ID
	}
	if len(cp.Tasks) != 2 || cp.Tasks["A"] == "" || cp.Tasks["B"] == "" {
		t.Fatal("seed assignment")
	}
	for i := range cp.Admissions {
		cp.Admissions[i].Targets = []string{cp.Tasks[cp.Admissions[i].Trigger.Text]}
	}
	targets := []string{cp.Tasks["A"], cp.Tasks["B"]}
	for _, target := range targets {
		if err := a.SetThreadSubscription(target, "github", "shared-pr", true); err != nil {
			t.Fatal(err)
		}
	}
	shared := superagent.Trigger{ID: "shared", Source: "github", Thread: "shared-pr", Kind: "signal", Text: "shared update"}
	submit(shared, targets, false)
	entered := map[string]bool{}
	for range 2 {
		call := mixedReceive(t, ctx, h.entered)
		entered[call.Work] = true
	}
	if !entered["A"] || !entered["B"] {
		t.Fatal("both fanout targets did not enter")
	}
	bTarget := superagent.RunTarget{TaskID: cp.Tasks["B"], Source: "github", EventID: "shared"}
	raw, _ := json.Marshal(bTarget)
	submit(superagent.Trigger{ID: "correction", Source: "im", Thread: "same-chat", Kind: "request", Text: string(raw)}, []string{""}, false)
	mixedAwait(t, ctx, "correction queued", func() bool { c := a.MainControls(); return len(c) == 1 && c[0].Status == "queued" })
	submit(superagent.Trigger{ID: "router-gate", Source: "im", Thread: "same-chat", Kind: "request", Text: "hold router"}, []string{""}, false)
	mixedReceive(t, ctx, h.routeEntered)
	// Backlog is admitted while the actual Pi main model is blocked.
	for i := 0; i < mixedRounds; i++ {
		update := shared
		update.ID = fmt.Sprintf("follow-%d", i)
		submit(update, targets, false)
		submit(update, targets, true)
	}
	aTarget := superagent.RunTarget{TaskID: cp.Tasks["A"], Source: "github", EventID: "shared"}
	wrong := aTarget
	wrong.EventID = "seed-A"
	if err := a.Abort(wrong); err == nil {
		t.Fatal("stale target canceled a later invocation")
	}
	if h.crash {
		close(h.releaseA)
	} else if err := a.Abort(aTarget); err != nil {
		t.Fatal(err)
	}
	mixedAwait(t, ctx, "A settles while router/B blocked", func() bool { return len(mixedReports(a, "shared")) == 1 })
	if calls := mixedReports(a, "shared"); calls[0].TaskID != cp.Tasks["A"] {
		t.Fatal("cancel/failure affected wrong fanout target")
	}
	if err := a.Abort(aTarget); err == nil {
		t.Fatal("completed target accepted abort")
	}
	close(h.routeRelease)
	// Completion-paced mixed traffic continues without waiting for global idle.
	// Each new task must finish while B's original body is still held.
	for i := 0; i < mixedRounds; i++ {
		id := fmt.Sprintf("independent-%d", i)
		submit(superagent.Trigger{ID: id, Source: "im", Thread: "same-chat", Kind: "request", Text: id}, nil, false)
		submit(superagent.Trigger{ID: fmt.Sprintf("noise-%d", i), Source: "im", Thread: "same-chat", Kind: "noise", Text: "non-actionable"}, []string{""}, false)
		submit(shared, targets, true)
		mixedAwait(t, ctx, id+" completes behind B", func() bool { return len(mixedReports(a, id)) == 1 })
		report := mixedReports(a, id)[0]
		if report.Status != "completed" || report.TaskID == cp.Tasks["A"] || report.TaskID == cp.Tasks["B"] {
			t.Fatal("independent task blocked/misrouted")
		}
		cp.Tasks[id] = report.TaskID
		for n := range cp.Admissions {
			if cp.Admissions[n].Trigger.ID == id {
				cp.Admissions[n].Targets = []string{report.TaskID}
			}
		}
		if len(mixedReports(a, "shared")) != 1 {
			t.Fatal("B left its gate before release")
		}
	}
	// Wait only for specific outbox observations; B is still active and its
	// correction and six serial follow-ups remain pending.
	mixedAwait(t, ctx, "checkpoint replies", func() bool { return len(a.Replies()) == 3+2*mixedRounds })
	h.mu.Lock()
	sawBatch := h.sawBatch
	h.mu.Unlock()
	if h.mode == loadModes[1] && !sawBatch {
		t.Fatal("backlog never reached a multi-input Pi batch")
	}
	cp.Reports, cp.Replies, cp.Snapshots = a.Status().Reports, a.Replies(), mixedSnapshot(t, a)
	return cp
}

func runMixedAcceptance(t *testing.T, mode string, crash bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	dir := t.TempDir()
	h := newMixedHarness(dir, mode, crash)
	var cp mixedCheckpoint
	var a *superagent.SuperAgent
	defer func() {
		cancel()
		if a != nil {
			_ = a.Close()
		}
	}()
	if crash {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestMixedRuntimeCrashChild$", "-test.timeout=80s")
		cmd.Env = append(os.Environ(), "PIGO_MIXED_CHILD_DIR="+dir, "PIGO_MIXED_CHILD_MODE="+mode)
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 73 {
			t.Fatalf("crash child exit: %v\n%s", err, out)
		}
		// os.Exit can bypass the race runtime's normal exit code. Never hide a
		// race warning (or another unexpected diagnostic) from the crash child.
		if len(out) != 0 {
			t.Fatalf("crash child diagnostics:\n%s", out)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &cp); err != nil {
			t.Fatal(err)
		}
		// This is a fresh coordinator after an ungraceful process exit. B's
		// committed file is real, but no terminal native receipt was persisted.
		a = h.open(t, ctx)
	} else {
		a = h.open(t, ctx)
		cp = mixedDrive(t, ctx, h, a)
		close(h.releaseB)
	}
	if err := a.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	validateMixed(t, a, cp, mixedEffects(t, dir), crash)
	oldReports := a.Status().Reports
	oldEffects := mixedEffects(t, dir)
	// Reconcile only after checking physical effects; old admitted follow-ups
	// must remain blocked, and neither committed write may be replayed.
	for _, work := range []string{"A", "B"} {
		if work == "B" && !crash {
			continue
		}
		if err := a.Reconcile(cp.Tasks[work], "github", "shared", "Verified the fixture's fsynced local write; do not repeat it."); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(oldReports, a.Status().Reports) || !reflect.DeepEqual(oldEffects, mixedEffects(t, dir)) {
		t.Fatal("reconciliation changed old reports or repeated effects")
	}
	before := len(oldEffects)
	for _, work := range []string{"A", "B"} {
		tr := superagent.Trigger{ID: "fresh-" + work, Source: "im", Thread: "same-chat", Kind: "request", Text: "fresh instruction"}
		if ok, err := a.SubmitTask(tr, cp.Tasks[work]); err != nil || !ok {
			t.Fatalf("fresh: %v %v", ok, err)
		}
	}
	if err := a.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	newEffects := mixedEffects(t, dir)
	if len(newEffects) != before+2 || !reflect.DeepEqual(oldEffects, newEffects[:before]) {
		t.Fatal("reconciliation replayed old effects")
	}
	if got := a.Status().Reports; len(got) != len(oldReports)+2 || !reflect.DeepEqual(oldReports, got[:len(oldReports)]) {
		t.Fatal("fresh instructions changed old terminal delivery evidence")
	}
	for _, work := range []string{"A", "B"} {
		r := mixedReports(a, "fresh-"+work)
		if len(r) != 1 || r[0].Status != "completed" || r[0].NeedsReview || r[0].TaskID != cp.Tasks[work] || len(r[0].Receipts) != 1 {
			t.Fatal("fresh work failed after reconciliation")
		}
		fact := r[0].Receipts[0]
		if fact.Token == 0 || fact.ParentToken != 0 || fact.Domain != "agent" || fact.ToolName != "write" || fact.CallID != "write-im-fresh-"+work || !fact.Committed || !fact.Completed {
			t.Fatal("fresh instruction lost native receipt")
		}
		want := mixedCall{cp.Tasks[work], work, "im", "fresh-" + work}
		if !slices.Contains(newEffects[before:], want) {
			t.Fatal("fresh effect identity does not match reconciled task")
		}
	}
	view, replies, controls, snapshots := a.Status(), a.Replies(), a.MainControls(), mixedSnapshot(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	calls := h.calls
	h.mu.Unlock()
	a = h.open(t, ctx)
	for _, admission := range cp.Admissions {
		var ok bool
		var err error
		if admission.Trigger.Source == "github" {
			ok, err = a.SubmitThread(admission.Trigger)
		} else {
			ok, err = a.Submit(admission.Trigger)
		}
		if ok || err != nil {
			t.Fatalf("duplicate after restart: %v %v", ok, err)
		}
	}
	if err := a.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	unchangedCalls := h.calls == calls
	h.mu.Unlock()
	if !unchangedCalls || !reflect.DeepEqual(view, a.Status()) || !reflect.DeepEqual(replies, a.Replies()) || !reflect.DeepEqual(controls, a.MainControls()) || !reflect.DeepEqual(snapshots, mixedSnapshot(t, a)) || len(mixedEffects(t, dir)) != before+2 {
		t.Fatal("completed replay changed evidence or executed work")
	}
}

func TestMixedRuntimeCrashChild(t *testing.T) {
	dir, mode := os.Getenv("PIGO_MIXED_CHILD_DIR"), os.Getenv("PIGO_MIXED_CHILD_MODE")
	if dir == "" {
		t.Skip("isolated crash helper")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	h := newMixedHarness(dir, mode, true)
	a := h.open(t, ctx)
	defer a.Close() // Intentionally bypassed by os.Exit after checkpoint sync.
	cp := mixedDrive(t, ctx, h, a)
	raw, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "checkpoint.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(73)
}
