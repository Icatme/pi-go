package superagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Icatme/pi-go/agent"
)

var ErrWatchdogBusy = errors.New("watchdog: an inspection is already running")

// WatchdogClock is independent of Worker/model scheduling and of storage lease
// time. A clock controls diagnostics only, never authority or execution expiry.
type WatchdogClock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}
type wallWatchdogClock struct{}

func (wallWatchdogClock) Now() time.Time                         { return time.Now() }
func (wallWatchdogClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// WatchdogPolicy is pinned at first use. Changing it requires a future explicit
// versioned migration; reopening with different thresholds fails closed.
type WatchdogPolicy struct {
	Revision  string        `json:"revision"`
	Interval  time.Duration `json:"interval"`
	BatchSize int           `json:"batch_size"`
	IdleAfter time.Duration `json:"idle_after"`
	IdleTurns uint64        `json:"idle_turns"`
}

// WatchdogOptions grants observation only. Callbacks must be read-only and
// honor cancellation. Do not close the coordinator from its own callback.
// There are deliberately no retry, cancel, steer, send or new-task actions.
type WatchdogOptions struct {
	Policy   WatchdogPolicy
	Clock    WatchdogClock
	Diagnose func(context.Context, WatchdogJob) (WatchdogOutcome, error)
	// Probe may check connectivity of an already admitted operation. Errors and
	// unknown are NOT offline evidence. It must not call Start, Stop or reconnect
	// in a way that dispatches work. Native authentication stays host-owned.
	Probe func(context.Context, WatchdogOperation) (string, error) // online, offline, unknown
}

type WatchdogOperation struct {
	ID           string `json:"id"`
	TaskID       string `json:"task_id"`
	DeliveryID   string `json:"delivery_id"`
	Tool         string `json:"tool"`
	State        string `json:"state"`
	NeedsReview  bool   `json:"needs_review"`
	Connectivity string `json:"connectivity"`
}

// WatchdogTask contains only bounded public facts. RuntimeID and counters are
// process-local evidence, never durable progress or proof of useful work.
// They cannot establish success, authorize an action or clear a review barrier.
type WatchdogTask struct {
	TaskID                string              `json:"task_id"`
	Goal                  string              `json:"goal"`
	GoalDeliveryID        string              `json:"goal_delivery_id"`
	LatestInput           string              `json:"latest_input"`
	LatestInputDeliveryID string              `json:"latest_input_delivery_id"`
	Status                string              `json:"status"`
	Pending               int                 `json:"pending"`
	Target                RunTarget           `json:"target"`
	DeliveryID            string              `json:"delivery_id,omitempty"`
	RuntimeID             string              `json:"runtime_id"`
	Turns                 uint64              `json:"turns"`
	ToolsStarted          uint64              `json:"tools_started"`
	ToolsCompleted        uint64              `json:"tools_completed"`
	LastReport            string              `json:"last_report,omitempty"`
	LastReportID          string              `json:"last_report_id,omitempty"`
	LastReportStatus      string              `json:"last_report_status,omitempty"`
	NeedsReview           bool                `json:"needs_review"`
	Operations            []WatchdogOperation `json:"operations,omitempty"`
	Revision              string              `json:"revision"`
}

type WatchdogJob struct {
	SessionID         string
	ProviderSessionID string
	Snapshot          agent.AgentSnapshot
	Tasks             []WatchdogTask
	Previous          []WatchdogFinding
	ObservedAt        int64
}

// Diagnosis is an untrusted hypothesis, not a control request. Only the two
// suspicion kinds are accepted; unknown tasks, duplicates and other verbs fail
// the entire model result. Deterministic wait/review facts always take priority.
type WatchdogDiagnosis struct {
	TaskID string `json:"task_id"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}
type WatchdogOutcome struct {
	Diagnoses []WatchdogDiagnosis
	Snapshot  agent.AgentSnapshot
}

type WatchdogFinding struct {
	ID         string       `json:"id"`
	PreviousID string       `json:"previous_id,omitempty"`
	Task       WatchdogTask `json:"task"`
	Class      string       `json:"class"` // settled, normal_slow, queued, tool_running, device_offline, needs_review, idle_suspected, drift_suspected
	// QuietSince means no observed tool completion or public terminal change,
	// not proof that no useful work occurred. Silent/slow work is never idle.
	QuietSince       int64              `json:"quiet_since"`
	TurnsWithoutTool uint64             `json:"turns_without_tool"`
	ObservedAt       int64              `json:"observed_at"`
	RecheckAt        int64              `json:"recheck_at"`
	Diagnosis        *WatchdogDiagnosis `json:"diagnosis,omitempty"`
	Stale            bool               `json:"stale"`
}

type WatchdogCheck struct {
	ID          string            `json:"id"`
	TaskCount   int               `json:"task_count"`
	Cursor      int               `json:"cursor"` // next task index; zero starts a new pass
	ObservedAt  int64             `json:"observed_at"`
	NextCheckAt int64             `json:"next_check_at"`
	Findings    []WatchdogFinding `json:"findings"`
	ModelStatus string            `json:"model_status"` // disabled, completed, failed
}
type WatchdogStatus struct {
	SessionID         string          `json:"session_id"`
	ProviderSessionID string          `json:"provider_session_id"`
	Policy            WatchdogPolicy  `json:"policy"`
	Cursor            int             `json:"cursor"`
	NextCheckAt       int64           `json:"next_check_at"`
	Running           bool            `json:"running"`
	Checks            []WatchdogCheck `json:"checks"`
}
type watchdogState struct {
	WatchdogStatus
	snapshot json.RawMessage
	latest   map[string]WatchdogFinding
}
type watchdogTransition struct {
	SessionID         string          `json:"session_id,omitempty"`
	ProviderSessionID string          `json:"provider_session_id,omitempty"`
	Policy            WatchdogPolicy  `json:"policy,omitempty"`
	Check             *WatchdogCheck  `json:"check,omitempty"`
	Snapshot          json.RawMessage `json:"snapshot,omitempty"`
}

func validWatchdogPolicy(p WatchdogPolicy) bool {
	return validIdentity(p.Revision) && p.Interval >= time.Millisecond && p.Interval <= 24*time.Hour && p.BatchSize >= 1 && p.BatchSize <= 32 && p.IdleAfter >= p.Interval && p.IdleAfter <= 30*24*time.Hour && p.IdleTurns >= 2 && p.IdleTurns <= 1_000_000
}
func watchdogOptionsChecked(o WatchdogOptions) (WatchdogOptions, error) {
	if !validWatchdogPolicy(o.Policy) {
		return o, errors.New("watchdog: invalid policy")
	}
	if o.Clock == nil {
		o.Clock = wallWatchdogClock{}
	} else {
		v := reflect.ValueOf(o.Clock)
		if v.Kind() == reflect.Pointer && v.IsNil() {
			return o, errors.New("watchdog: nil clock")
		}
	}
	return o, nil
}
func cloneWatchdog[T any](v T) T {
	b, _ := json.Marshal(v)
	var out T
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	_ = decoder.Decode(&out)
	return out
}
func (a *SuperAgent) WatchdogStatus() WatchdogStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneWatchdog(a.watchdog.WatchdogStatus)
}

// CheckWatchdog records at most one due, bounded inspection. A zero Check means
// not due; a committed check, cursor and model snapshot are never replayed.
// Uncommitted read-only inference may repeat after interruption. The coordinator
// owns the only writer and applies its existing lease fence to this commit.
func (a *SuperAgent) CheckWatchdog(ctx context.Context, options WatchdogOptions) (WatchdogCheck, error) {
	var empty WatchdogCheck
	if ctx == nil {
		return empty, errors.New("watchdog: context required")
	}
	o, err := watchdogOptionsChecked(options)
	if err != nil {
		return empty, err
	}
	now := o.Clock.Now().UnixMilli()
	if now < 0 || now > 1<<62 {
		return empty, errors.New("watchdog: invalid clock")
	}
	a.mu.Lock()
	if err = a.watchdogAllowed(ctx); err != nil {
		a.mu.Unlock()
		return empty, err
	}
	if a.watchdog.SessionID == "" {
		err = a.commit(transition{State: "watchdog_opened", Watchdog: &watchdogTransition{SessionID: "watchdog-" + rand.Text(), ProviderSessionID: rand.Text(), Policy: o.Policy}})
		if err != nil {
			a.mu.Unlock()
			return empty, err
		}
	}
	if a.watchdog.Policy != o.Policy {
		a.mu.Unlock()
		return empty, errors.New("watchdog: policy differs from durable configuration")
	}
	if a.watchdog.Running {
		a.mu.Unlock()
		return empty, ErrWatchdogBusy
	}
	if now < a.watchdog.NextCheckAt {
		a.mu.Unlock()
		return empty, nil
	}
	a.watchdog.Running = true
	a.wg.Add(1)
	defer func() { a.mu.Lock(); a.watchdog.Running = false; a.mu.Unlock(); a.wg.Done() }()
	owned, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(a.ctx, cancel)
	defer func() { stop(); cancel() }()
	count, start := len(a.taskIDs), a.watchdog.Cursor
	end := min(start+o.Policy.BatchSize, count)
	check := WatchdogCheck{ID: a.watchdog.SessionID + "/" + strconv.Itoa(len(a.watchdog.Checks)+1), TaskCount: count, ObservedAt: now, NextCheckAt: now + o.Policy.Interval.Milliseconds(), ModelStatus: "disabled"}
	if end < count {
		check.Cursor = end
	}
	job := WatchdogJob{SessionID: a.watchdog.SessionID, ProviderSessionID: a.watchdog.ProviderSessionID, ObservedAt: now, Snapshot: agent.AgentSnapshot{SessionID: a.watchdog.SessionID}}
	if len(a.watchdog.snapshot) > 0 {
		job.Snapshot, err = validateSnapshot(a.watchdog.snapshot, a.watchdog.SessionID, nil)
		if err != nil {
			a.mu.Unlock()
			return empty, err
		}
	}
	for _, id := range a.taskIDs[start:end] {
		job.Tasks = append(job.Tasks, a.watchdogTaskLocked(id))
		if previous, ok := a.watchdog.latest[id]; ok {
			job.Previous = append(job.Previous, cloneWatchdog(previous))
		}
	}
	snapshot := slices.Clone(a.watchdog.snapshot)
	a.mu.Unlock()
	// Connectivity evidence and model inference run outside the coordinator lock.
	for i := range job.Tasks {
		for k := range job.Tasks[i].Operations {
			op := &job.Tasks[i].Operations[k]
			if o.Probe != nil {
				op.Connectivity = callWatchdogProbe(owned, o.Probe, *op)
			}
		}
	}
	var diagnoses []WatchdogDiagnosis
	if o.Diagnose != nil {
		check.ModelStatus = "failed"
		outcome, e := callWatchdogModel(owned, o.Diagnose, cloneWatchdog(job))
		if e == nil && outcome.Snapshot.SessionID == job.SessionID && validWatchdogDiagnoses(outcome.Diagnoses, job.Tasks) {
			raw, _, e := encodeSnapshot(outcome.Snapshot, job.SessionID)
			if e == nil && len(raw) <= 1<<20 {
				snapshot = raw
				diagnoses = slices.Clone(outcome.Diagnoses)
				check.ModelStatus = "completed"
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err = a.watchdogAllowed(owned); err != nil {
		return empty, err
	}
	for i, task := range job.Tasks {
		f := watchdogFinding(task, a.watchdog.latest[task.TaskID], o.Policy, now)
		f.ID = check.ID + "/" + strconv.Itoa(i+1)
		f.RecheckAt = check.NextCheckAt
		f.Stale = task.Revision != a.watchdogTaskLocked(task.TaskID).Revision
		for _, d := range diagnoses {
			if d.TaskID == task.TaskID {
				d := d
				f.Diagnosis = &d
				break
			}
		}
		applyWatchdogHypothesis(&f)
		check.Findings = append(check.Findings, f)
	}
	err = a.commit(transition{State: "watchdog_checked", Watchdog: &watchdogTransition{Check: &check, Snapshot: snapshot}})
	if err != nil {
		return empty, err
	}
	return cloneWatchdog(check), nil
}

func (a *SuperAgent) watchdogAllowed(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.fatal != nil {
		return a.fatal
	}
	return a.ctx.Err()
}

// RunWatchdog is a blocking, independent clock loop; a host may run it in its
// own goroutine. Cancel ctx to stop diagnostics only. No model/provider is
// selected implicitly. Storage lease renewal always retains its own clock.
func (a *SuperAgent) RunWatchdog(ctx context.Context, options WatchdogOptions) error {
	if ctx == nil {
		return errors.New("watchdog: context required")
	}
	o, err := watchdogOptionsChecked(options)
	if err != nil {
		return err
	}
	for {
		if _, err = a.CheckWatchdog(ctx, o); err != nil {
			return err
		}
		a.mu.Lock()
		next := a.watchdog.NextCheckAt
		a.mu.Unlock()
		remaining := next - o.Clock.Now().UnixMilli()
		delay := o.Policy.Interval
		if remaining <= 0 {
			delay = time.Millisecond
		} else if remaining < o.Policy.Interval.Milliseconds() {
			delay = time.Duration(remaining) * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.ctx.Done():
			return a.ctx.Err()
		case <-o.Clock.After(delay):
		}
	}
}

func callWatchdogProbe(ctx context.Context, probe func(context.Context, WatchdogOperation) (string, error), o WatchdogOperation) (state string) {
	state = "unknown"
	defer func() {
		if recover() != nil {
			state = "unknown"
		}
	}()
	value, err := probe(ctx, o)
	if err == nil && (value == "online" || value == "offline") {
		return value
	}
	return state
}
func callWatchdogModel(ctx context.Context, model func(context.Context, WatchdogJob) (WatchdogOutcome, error), j WatchdogJob) (out WatchdogOutcome, err error) {
	defer func() {
		if recover() != nil {
			out = WatchdogOutcome{}
			err = errors.New("watchdog: diagnostic callback panicked")
		}
	}()
	return model(ctx, j)
}
func validWatchdogDiagnoses(ds []WatchdogDiagnosis, tasks []WatchdogTask) bool {
	if len(ds) > len(tasks) {
		return false
	}
	seen := map[string]bool{}
	allowed := map[string]bool{}
	for _, t := range tasks {
		allowed[t.TaskID] = true
	}
	for _, d := range ds {
		if !allowed[d.TaskID] || seen[d.TaskID] || (d.Kind != "idle_suspected" && d.Kind != "drift_suspected") || !validNote(d.Reason) {
			return false
		}
		seen[d.TaskID] = true
	}
	return true
}
func applyWatchdogHypothesis(f *WatchdogFinding) {
	if f.Diagnosis != nil && (f.Class == "normal_slow" || f.Class == "idle_suspected" || (f.Class == "settled" && f.Diagnosis.Kind == "drift_suspected")) {
		f.Class = f.Diagnosis.Kind
	}
}
func (a *SuperAgent) watchdogTaskLocked(id string) WatchdogTask {
	t := a.tasks[id]
	o := WatchdogTask{TaskID: id, Status: t.last, Pending: t.pending, RuntimeID: a.watchdogRuntime, NeedsReview: t.review != 0 || t.backgroundReview != 0}
	if r := t.active; r != nil {
		o.Status = r.phase()
		o.Target = r.target
		o.Turns = r.watchdogTurns
		o.ToolsStarted = r.watchdogToolsStarted
		o.ToolsCompleted = r.watchdogToolsCompleted
		if i, ok := a.deliveryIndex(r.target); ok {
			o.DeliveryID = deliveryID(i)
		}
	} else if t.running {
		o.Status = "running"
	} else if t.pending > 0 {
		o.Status = "queued"
	}
	for i, e := range a.events {
		if e.task != id || e.trigger.Source == backgroundSource {
			continue
		}
		if o.GoalDeliveryID == "" {
			o.Goal = brief(e.trigger.Text)
			o.GoalDeliveryID = deliveryID(i)
		}
		o.LatestInput = brief(e.trigger.Text)
		o.LatestInputDeliveryID = deliveryID(i)
	}
	for i := len(a.reports) - 1; i >= 0; i-- {
		r := a.reports[i]
		if r.TaskID == id {
			o.LastReport = brief(r.Text)
			o.LastReportID = r.DeliveryID
			o.LastReportStatus = r.Status
			break
		}
	}
	for _, b := range a.background.operations {
		if b.TaskID == id && (!b.Exited || b.NeedsReview) {
			o.Operations = append(o.Operations, WatchdogOperation{ID: b.ID, TaskID: id, DeliveryID: b.DeliveryID, Tool: b.Tool, State: b.Status, NeedsReview: b.NeedsReview, Connectivity: "unknown"})
			o.NeedsReview = o.NeedsReview || b.NeedsReview
		}
	}
	if o.NeedsReview {
		o.Status = "needs_review"
	}
	o.Revision = watchdogTaskRevision(o)
	return o
}
func watchdogTaskRevision(o WatchdogTask) string {
	o.Revision = ""
	o.Operations = slices.Clone(o.Operations)
	for i := range o.Operations {
		o.Operations[i].Connectivity = "unknown"
	}
	raw, _ := json.Marshal(o)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func watchdogFinding(task WatchdogTask, previous WatchdogFinding, p WatchdogPolicy, now int64) WatchdogFinding {
	f := WatchdogFinding{Task: task, PreviousID: previous.ID, ObservedAt: now, QuietSince: now, Class: "settled"}
	old := previous.Task
	if previous.ID != "" && old.RuntimeID == task.RuntimeID && old.Target == task.Target && old.DeliveryID == task.DeliveryID && old.LastReportID == task.LastReportID && old.ToolsCompleted == task.ToolsCompleted && task.Turns >= old.Turns && !previous.Stale {
		f.QuietSince = previous.QuietSince
		f.TurnsWithoutTool = previous.TurnsWithoutTool + task.Turns - old.Turns
	}
	offline, tool := false, task.ToolsStarted > task.ToolsCompleted
	for _, op := range task.Operations {
		offline = offline || op.Connectivity == "offline"
		tool = tool || op.State == "reserved" || op.State == "running"
	}
	switch {
	case task.NeedsReview:
		f.Class = "needs_review"
	case offline:
		f.Class = "device_offline"
	case tool:
		f.Class = "tool_running"
	case task.Status == "queued":
		f.Class = "queued"
	case task.Status == "running":
		f.Class = "normal_slow"
		if f.TurnsWithoutTool >= p.IdleTurns && now-f.QuietSince >= p.IdleAfter.Milliseconds() {
			f.Class = "idle_suspected"
		}
	case task.Status == "finalizing" || task.Status == "abort_requested":
		f.Class = "normal_slow"
	}
	return f
}

func (a *SuperAgent) validateWatchdog(tr transition) error {
	w := tr.Watchdog
	if w == nil || tr.Index != 0 || tr.Trigger != nil || tr.Task != "" || tr.ProviderSessionID != "" || tr.Text != "" || len(tr.Snapshot) > 0 || len(tr.Receipts) > 0 || tr.NeedsReview || tr.Background != nil || tr.Main != nil || tr.Admission != nil || tr.Thread != nil {
		return errors.New("watchdog: mixed transition")
	}
	switch tr.State {
	case "watchdog_opened":
		if a.watchdog.SessionID != "" || !validIdentity(w.SessionID) || !validIdentity(w.ProviderSessionID) || w.SessionID == w.ProviderSessionID || w.SessionID == a.main.id || w.ProviderSessionID == a.main.providerID || !validWatchdogPolicy(w.Policy) || w.Check != nil || len(w.Snapshot) > 0 {
			return errors.New("watchdog: invalid session admission")
		}
	case "watchdog_checked":
		c := w.Check
		p := a.watchdog.Policy
		if a.watchdog.SessionID == "" || w.SessionID != "" || w.ProviderSessionID != "" || w.Policy != (WatchdogPolicy{}) || c == nil || c.ID != a.watchdog.SessionID+"/"+strconv.Itoa(len(a.watchdog.Checks)+1) || c.TaskCount < a.watchdog.Cursor || c.TaskCount > len(a.taskIDs) || c.ObservedAt < 0 || c.ObservedAt > 1<<62 || c.ObservedAt < a.watchdog.NextCheckAt || c.NextCheckAt != c.ObservedAt+p.Interval.Milliseconds() {
			return errors.New("watchdog: invalid check identity or clock")
		}
		start := a.watchdog.Cursor
		end := min(start+p.BatchSize, c.TaskCount)
		cursor := 0
		if end < c.TaskCount {
			cursor = end
		}
		if c.Cursor != cursor || len(c.Findings) != end-start || (c.ModelStatus != "disabled" && c.ModelStatus != "completed" && c.ModelStatus != "failed") {
			return errors.New("watchdog: invalid check cursor")
		}
		if c.ModelStatus == "completed" && len(w.Snapshot) == 0 {
			return errors.New("watchdog: completed model requires session memory")
		}
		if len(w.Snapshot) > 0 {
			if len(w.Snapshot) > 1<<20 {
				return errors.New("watchdog: snapshot too large")
			}
			snapshot, err := validateSnapshot(w.Snapshot, a.watchdog.SessionID, nil)
			if err != nil {
				return err
			}
			if snapshot.SessionID != a.watchdog.SessionID {
				return errors.New("watchdog: snapshot requires exact session identity")
			}
		}
		if c.ModelStatus != "completed" && !slices.Equal(w.Snapshot, a.watchdog.snapshot) {
			return errors.New("watchdog: failed model changed memory")
		}
		for i, f := range c.Findings {
			if f.ID != c.ID+"/"+strconv.Itoa(i+1) || f.Task.TaskID != a.taskIDs[start+i] || f.ObservedAt != c.ObservedAt || f.RecheckAt != c.NextCheckAt || !validWatchdogTask(f.Task) || !a.validWatchdogSources(f.Task) {
				return errors.New("watchdog: invalid finding evidence")
			}
			if f.Diagnosis != nil && (c.ModelStatus != "completed" || !validWatchdogDiagnoses([]WatchdogDiagnosis{*f.Diagnosis}, []WatchdogTask{f.Task})) {
				return errors.New("watchdog: invalid hypothesis")
			}
			expected := watchdogFinding(f.Task, a.watchdog.latest[f.Task.TaskID], p, c.ObservedAt)
			expected.Diagnosis = f.Diagnosis
			applyWatchdogHypothesis(&expected)
			if f.Class != expected.Class || f.PreviousID != expected.PreviousID || f.QuietSince != expected.QuietSince || f.TurnsWithoutTool != expected.TurnsWithoutTool {
				return errors.New("watchdog: inconsistent finding")
			}
		}
	default:
		return errors.New("watchdog: unknown transition")
	}
	return nil
}
func validWatchdogTask(t WatchdogTask) bool {
	if !validIdentity(t.TaskID) || !validIdentity(t.RuntimeID) || t.Revision != watchdogTaskRevision(t) || t.Pending < 0 || len(t.Goal) > 1024 || len(t.LatestInput) > 1024 || t.GoalDeliveryID == "" || t.LatestInputDeliveryID == "" || len(t.LastReport) > 1024 || len(t.Operations) > 256 || t.ToolsCompleted > t.ToolsStarted {
		return false
	}
	switch t.Status {
	case "ready", "queued", "running", "finalizing", "abort_requested", "needs_review", "completed", "failed", "completed_with_tool_errors", "truncated", "blocked", "interrupted", "reconciled":
	default:
		return false
	}
	if t.Target != (RunTarget{}) && (t.Target.TaskID != t.TaskID || !validIdentity(t.Target.Source) || !validIdentity(t.Target.EventID) || t.DeliveryID == "") {
		return false
	}
	seen := map[string]bool{}
	for _, o := range t.Operations {
		if seen[o.ID] || (o.NeedsReview && !t.NeedsReview) || !validIdentity(o.ID) || o.TaskID != t.TaskID || o.DeliveryID == "" || !validIdentity(o.Tool) || (o.State != "reserved" && o.State != "running" && o.State != "needs_review" && o.State != "exited") || (o.Connectivity != "online" && o.Connectivity != "offline" && o.Connectivity != "unknown") {
			return false
		}
		seen[o.ID] = true
	}
	return true
}

// Validate immutable source links even for a stale observation. Current run
// counters/state cannot be reconstructed on replay and are not used as proof.
func (a *SuperAgent) validWatchdogSources(t WatchdogTask) bool {
	event := func(id string) (int, *item) {
		n, err := strconv.Atoi(strings.TrimPrefix(id, "delivery-"))
		if err != nil || n < 1 || n > len(a.events) || deliveryID(n-1) != id || a.events[n-1].task != t.TaskID {
			return 0, nil
		}
		return n - 1, &a.events[n-1]
	}
	goalIndex, goal := event(t.GoalDeliveryID)
	inputIndex, input := event(t.LatestInputDeliveryID)
	if goal == nil || input == nil || inputIndex < goalIndex || goal.trigger.Source == backgroundSource || input.trigger.Source == backgroundSource || brief(goal.trigger.Text) != t.Goal || brief(input.trigger.Text) != t.LatestInput {
		return false
	}
	for i := 0; i < goalIndex; i++ {
		if a.events[i].task == t.TaskID && a.events[i].trigger.Source != backgroundSource {
			return false
		}
	}
	if t.Target != (RunTarget{}) {
		_, active := event(t.DeliveryID)
		if active == nil || active.trigger.Source != t.Target.Source || active.trigger.ID != t.Target.EventID {
			return false
		}
	} else if t.DeliveryID != "" || t.Turns != 0 || t.ToolsStarted != 0 || t.ToolsCompleted != 0 {
		return false
	}
	if t.LastReportID != "" {
		found := false
		for _, r := range a.reports {
			if r.TaskID == t.TaskID && r.DeliveryID == t.LastReportID && brief(r.Text) == t.LastReport && r.Status == t.LastReportStatus {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	} else if t.LastReport != "" || t.LastReportStatus != "" {
		return false
	}
	for _, op := range t.Operations {
		original, ok := a.backgroundOperation(op.ID)
		if !ok || original.TaskID != t.TaskID || original.DeliveryID != op.DeliveryID || original.Tool != op.Tool {
			return false
		}
	}
	return true
}

func (a *SuperAgent) applyWatchdog(tr transition) error {
	if err := a.validateWatchdog(tr); err != nil {
		return fmt.Errorf("invalid watchdog record: %w", err)
	}
	w := tr.Watchdog
	if tr.State == "watchdog_opened" {
		a.watchdog.WatchdogStatus = WatchdogStatus{SessionID: w.SessionID, ProviderSessionID: w.ProviderSessionID, Policy: w.Policy}
		a.watchdog.latest = map[string]WatchdogFinding{}
		return nil
	}
	c := cloneWatchdog(*w.Check)
	a.watchdog.Checks = append(a.watchdog.Checks, c)
	a.watchdog.Cursor = c.Cursor
	a.watchdog.NextCheckAt = c.NextCheckAt
	a.watchdog.snapshot = slices.Clone(w.Snapshot)
	for _, f := range c.Findings {
		a.watchdog.latest[f.Task.TaskID] = f
	}
	return nil
}
