package superagent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
)

// MainRouter is a read-only inference callback. Unlike Worker, it must not
// execute tools or external effects: an uncommitted decision may be retried
// after restart. All model-proposed actions are validated by the coordinator.
type MainRouter func(context.Context, MainJob) (MainOutcome, error)

// MainInput is derived from durable admission, control or Worker transitions.
// Report contains the public summary and execution facts, never the private
// Worker transcript or FailureCause. Its source/thread remain host-owned.
type MainInput struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"` // user, task_input, control_result or worker_result
	Trigger      Trigger      `json:"trigger"`
	TaskID       string       `json:"task_id,omitempty"`
	Control      *MainControl `json:"control,omitempty"`
	DelegationID string       `json:"delegation_id,omitempty"`
	Report       *Report      `json:"report,omitempty"`
}

type MainJob struct {
	SessionID         string
	ProviderSessionID string
	Input             MainInput
	Snapshot          agent.AgentSnapshot
	Tasks             []TaskInfo
	// Runs lists only the currently controllable invocations offered this turn.
	Runs []RunTarget
}

// MainDecision selects one coordinator action. A result
// event can be replied to or ignored, never used to start new business work.
// Reply destinations come from the input, not from model-generated fields.
type MainDecision struct {
	Action string     `json:"action"` // new, attach, steer, cancel, reply, clarify, ignore
	TaskID string     `json:"task_id,omitempty"`
	Text   string     `json:"text,omitempty"`
	Target *RunTarget `json:"target,omitempty"`
}

type MainOutcome struct {
	Decision MainDecision
	Snapshot agent.AgentSnapshot
}

// Reply is a durable, host-readable outbox item. The authoritative Report
// accompanies generated text so partial failure, committed effects and review
// requirements cannot be replaced by a model's description of success.
// Control similarly preserves correction facts for control_result replies.
type Reply struct {
	ID           string         `json:"id"`
	DecisionID   string         `json:"decision_id"`
	InputID      string         `json:"input_id"`
	DelegationID string         `json:"delegation_id,omitempty"`
	Source       string         `json:"source"`
	Thread       string         `json:"thread"`
	EventID      string         `json:"event_id"`
	TaskID       string         `json:"task_id,omitempty"`
	Text         string         `json:"text"`
	Status       string         `json:"status"` // pending, needs_review, acknowledged
	Report       *Report        `json:"report,omitempty"`
	Control      *MainControl   `json:"control,omitempty"`
	Attempts     []ReplyAttempt `json:"attempts,omitempty"`
}

type mainItem struct {
	input MainInput
	event int
}

type mainState struct {
	id, providerID string
	snapshot       json.RawMessage
	inbox          []mainItem
	cursor         int
	running        bool
	chatRouting    bool // durable boundary: earlier admissions retain old semantics
	replies        []Reply
	controls       []MainControl
	controlIndex   map[string]int
	failures       []MainFailure
	causes         map[string]error
}

// MainFailure records a consumed input that could not produce a valid
// decision. It is not silently retried or converted into a successful reply.
type MainFailure struct {
	InputID string `json:"input_id"`
	Text    string `json:"text"`
}

type mainTransition struct {
	SessionID         string               `json:"session_id,omitempty"`
	ProviderSessionID string               `json:"provider_session_id,omitempty"`
	InputID           string               `json:"input_id,omitempty"`
	DecisionID        string               `json:"decision_id,omitempty"`
	Decision          MainDecision         `json:"decision,omitempty"`
	Snapshot          json.RawMessage      `json:"snapshot,omitempty"`
	Assignment        *transition          `json:"assignment,omitempty"`
	ReplyID           string               `json:"reply_id,omitempty"`
	ControlIDs        []string             `json:"control_ids,omitempty"`
	Delivery          *ReplyDelivery       `json:"delivery,omitempty"`
	DeliveryResult    *ReplyDeliveryResult `json:"delivery_result,omitempty"`
}

// OpenMain opens the persistent main session for this journal's single host
// authorization domain. Use separate journals for distinct trust domains.
// It shares the existing Worker scheduler and controls. Open is the legacy
// stateless API; once a journal is upgraded, it must be reopened with OpenMain.
// Old completed reports are not replayed as fresh main inputs on upgrade.
func OpenMain(ctx context.Context, path string, parallel int, route MainRouter, work Worker) (*SuperAgent, error) {
	if ctx == nil || path == "" || parallel < 1 || parallel > 64 || route == nil || work == nil {
		return nil, errors.New("superagent: require a context, path, 1–64 workers, main router and worker")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a, err := openState(ctx, path, true)
	if err != nil {
		return nil, err
	}
	if err := a.recoverInterrupted(); err != nil {
		return nil, errors.Join(err, a.Close())
	}
	if a.main.id == "" {
		err = a.commit(transition{State: "main_opened", Main: &mainTransition{SessionID: "main-" + rand.Text(), ProviderSessionID: rand.Text()}})
		if err != nil {
			return nil, errors.Join(err, a.Close())
		}
	}
	if err := a.recoverMainControls(); err != nil {
		return nil, errors.Join(err, a.Close())
	}
	if !a.main.chatRouting {
		if err := a.commit(transition{State: "main_chat_opened", Main: &mainTransition{}}); err != nil {
			return nil, errors.Join(err, a.Close())
		}
	}
	a.parallel, a.mainRoute, a.work = parallel, route, work
	a.wg.Add(1)
	go a.schedule()
	return a, nil
}

func (a *SuperAgent) enqueueMain(index int, report *Report) {
	e := a.events[index]
	input := MainInput{ID: a.nextMainInputID(), Kind: "user", Trigger: e.trigger}
	if report != nil {
		input.Kind, input.Report, input.DelegationID = "worker_result", copyReport(report), e.delegation
	} else if e.entryTask != "" {
		input.Kind, input.TaskID = "task_input", e.entryTask
		// The Worker receives the full event. The main observes bounded public
		// input text, not a second actionable request or a private transcript.
		input.Trigger.Text = brief(input.Trigger.Text)
	} else {
		a.events[index].mainInput = true
		a.events[index].mainAdmitted = true
	}
	a.main.inbox = append(a.main.inbox, mainItem{input: input, event: index})
}

func (a *SuperAgent) nextMainInputID() string {
	return a.main.id + "/input-" + strconv.Itoa(len(a.main.inbox)+1)
}

func copyReport(report *Report) *Report {
	if report == nil {
		return nil
	}
	copy := *report
	copy.Receipts = slices.Clone(report.Receipts)
	return &copy
}

func (a *SuperAgent) dispatchMain() bool {
	if a.mainRoute == nil || a.main.running || a.main.cursor == len(a.main.inbox) {
		return false
	}
	item := a.main.inbox[a.main.cursor]
	job := MainJob{SessionID: a.main.id, ProviderSessionID: a.main.providerID, Input: item.input}
	job.Input.Report = copyReport(job.Input.Report)
	job.Input.Control = copyControl(job.Input.Control)
	if len(a.main.snapshot) != 0 {
		if err := decodeSnapshot(a.main.snapshot, &job.Snapshot); err != nil {
			a.commit(transition{State: "main_failed", Text: "Main snapshot could not be read.", Main: &mainTransition{InputID: item.input.ID}})
			return true
		}
	} else {
		job.Snapshot.SessionID = a.main.id
	}
	for _, id := range a.taskIDs[max(0, len(a.taskIDs)-maxCandidates):] {
		job.Tasks = append(job.Tasks, TaskInfo{ID: id, Summary: a.tasks[id].summary})
	}
	// Active controls are bounded by the worker limit, not the history window.
	for _, id := range a.taskIDs {
		if r := a.tasks[id].active; r != nil && !r.returned && !r.aborted {
			job.Runs = append(job.Runs, r.target)
		}
	}
	a.main.running = true
	a.wg.Add(1)
	go a.runMain(job)
	return true
}

func callMain(ctx context.Context, route MainRouter, job MainJob) (out MainOutcome, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = callbackPanic("main router", p)
		}
	}()
	return route(ctx, job)
}

func (a *SuperAgent) runMain(job MainJob) {
	defer a.wg.Done()
	// Retain the IDs offered to this invocation independently of callback memory.
	ids := make(map[string]bool, len(job.Tasks))
	for _, task := range job.Tasks {
		ids[task.ID] = true
	}
	runs := make(map[RunTarget]bool, len(job.Runs))
	for _, target := range job.Runs {
		runs[target] = true
	}
	out, err := callMain(a.ctx, a.mainRoute, job)
	if out.Decision.Target != nil {
		target := *out.Decision.Target
		out.Decision.Target = &target
	}
	var snapshot json.RawMessage
	var validated *validatedSnapshot
	if err == nil {
		snapshot, validated, err = encodeSnapshot(out.Snapshot, job.SessionID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.main.running = false
	if a.ctx.Err() != nil {
		// No durable start is necessary for read-only inference. Preserve the
		// original inbox item for the next OpenMain, without advancing it.
		close(a.changed)
		a.changed = make(chan struct{})
		return
	}
	m := &mainTransition{InputID: job.Input.ID, DecisionID: "main-decision-" + job.Input.ID, Decision: out.Decision, Snapshot: snapshot}
	tr := transition{State: "main_decided", Main: m, validatedSnapshot: validated}
	if err == nil && out.Decision.Action == "attach" && !ids[out.Decision.TaskID] {
		err = errors.New("main router selected a task not offered to this turn")
	}
	if err == nil && (out.Decision.Action == "steer" || out.Decision.Action == "cancel") {
		if out.Decision.Target == nil || !runs[*out.Decision.Target] {
			err = errors.New("main router selected an invocation not offered to this turn")
		} else if r, targetErr := a.activeLocked(*out.Decision.Target); targetErr != nil || r.aborted {
			err = errors.New("main router selected an invocation no longer controllable")
		}
	}
	if err == nil {
		switch out.Decision.Action {
		case "new":
			m.Assignment = &transition{State: "queued", Index: a.main.inbox[a.main.cursor].event, Task: a.nextTaskID(), ProviderSessionID: rand.Text()}
		case "attach":
			m.Assignment = &transition{State: "queued", Index: a.main.inbox[a.main.cursor].event, Task: out.Decision.TaskID}
		case "reply", "clarify":
			m.ReplyID = "reply-" + job.Input.ID
		}
		err = a.validateMain(tr)
	}
	if err != nil {
		i := a.main.inbox[a.main.cursor].event
		if a.main.causes == nil {
			a.main.causes = make(map[string]error)
		}
		a.main.causes[job.Input.ID] = err
		if job.Input.Kind == "user" {
			a.events[i].cause = err
		}
		a.commit(transition{State: "main_failed", Text: "Main router failed or returned an invalid decision.", Main: &mainTransition{InputID: job.Input.ID}})
		return
	}
	a.commit(tr)
}

// validateMain is side-effect free and runs before the append as well as on
// replay. Invalid model output must never be written to a replayable journal.
func (a *SuperAgent) validateMain(tr transition) error {
	m := tr.Main
	if m == nil {
		return errors.New("missing main transition")
	}
	if tr.State == "main_opened" {
		if a.main.id != "" || !validIdentity(m.SessionID) || !validIdentity(m.ProviderSessionID) || m.SessionID == m.ProviderSessionID {
			return errors.New("invalid main session activation")
		}
		return nil
	}
	if a.main.id == "" {
		return errors.New("main session is not active")
	}
	if tr.State == "main_chat_opened" {
		if a.main.chatRouting {
			return errors.New("chat routing is already active")
		}
		return nil
	}
	if strings.HasPrefix(tr.State, "control_") {
		return a.validateMainControl(tr)
	}
	if strings.HasPrefix(tr.State, "reply_") {
		return a.validateReply(tr)
	}
	if a.main.cursor >= len(a.main.inbox) || m.InputID != a.main.inbox[a.main.cursor].input.ID {
		return errors.New("main decision references a different inbox item")
	}
	item := a.main.inbox[a.main.cursor]
	if item.input.Kind == "user" && a.events[item.event].state != "received" {
		return errors.New("main input is no longer unassigned")
	}
	if tr.State == "main_failed" {
		if !validNote(tr.Text) {
			return errors.New("invalid main failure")
		}
		if m.Decision != (MainDecision{}) || m.DecisionID != "" || m.Assignment != nil || m.ReplyID != "" || len(m.Snapshot) != 0 {
			return errors.New("failed main input cannot contain a decision, assignment, reply or snapshot")
		}
		return nil
	}
	if tr.State != "main_decided" || m.DecisionID != "main-decision-"+m.InputID {
		return errors.New("invalid main decision identity")
	}
	if _, err := validateSnapshot(m.Snapshot, a.main.id, tr.validatedSnapshot); err != nil {
		return errors.New("invalid main snapshot")
	}
	d := m.Decision
	if d.Action != "steer" && d.Action != "cancel" && d.Target != nil {
		return errors.New("unexpected main control target")
	}
	if item.input.Kind == "worker_result" && d.Action != "reply" && d.Action != "ignore" {
		return errors.New("worker result cannot authorize new work")
	}
	if (item.input.Kind == "task_input" || item.input.Kind == "control_result") && d.Action != "reply" && d.Action != "clarify" && d.Action != "ignore" {
		return errors.New("informational input cannot authorize new work or control")
	}
	switch d.Action {
	case "new", "attach":
		if d.Text != "" || m.ReplyID != "" || m.Assignment == nil || m.Assignment.Index != item.event || m.Assignment.State != "queued" || m.Assignment.Main != nil {
			return errors.New("invalid main assignment")
		}
		assignment := m.Assignment
		if previous := a.subscriptions[[2]string{item.input.Trigger.Source, item.input.Trigger.Thread}]; !a.events[item.event].chatEntry && previous != "" && previous != assignment.Task {
			return errors.New("main assignment conflicts with thread subscription")
		}
		if d.Action == "new" {
			if item.input.Trigger.Kind != "request" || d.TaskID != "" || assignment.Task != a.nextTaskID() || !validIdentity(assignment.ProviderSessionID) {
				return errors.New("invalid main delegation")
			}
		} else if a.tasks[d.TaskID] == nil || assignment.Task != d.TaskID || assignment.ProviderSessionID != "" {
			return errors.New("invalid main attachment")
		}
	case "steer", "cancel":
		if err := a.validateControlDecision(item.input, m); err != nil {
			return err
		}
	case "reply", "clarify":
		if d.TaskID != "" || strings.TrimSpace(d.Text) == "" || !utf8.ValidString(d.Text) || len(d.Text) > maxReportBytes || m.Assignment != nil || m.ReplyID != "reply-"+m.InputID {
			return errors.New("invalid main reply")
		}
	case "ignore":
		if d.TaskID != "" || d.Text != "" || m.Assignment != nil || m.ReplyID != "" {
			return errors.New("invalid main ignore")
		}
	default:
		return errors.New("unknown main action")
	}
	return nil
}

func validIdentity(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= 256 && utf8.ValidString(id)
}

func (a *SuperAgent) applyMain(tr transition, replay bool) error {
	if err := a.validateMain(tr); err != nil {
		return err
	}
	m := tr.Main
	if strings.HasPrefix(tr.State, "reply_") {
		a.applyReply(tr)
		return nil
	}
	if strings.HasPrefix(tr.State, "control_") {
		for _, id := range m.ControlIDs {
			c := a.mainControl(id)
			c.Status = strings.TrimPrefix(tr.State, "control_")
			a.enqueueControlResult(c)
		}
		return nil
	}
	switch tr.State {
	case "main_chat_opened":
		a.main.chatRouting = true
		return nil
	case "main_opened":
		a.main.id, a.main.providerID = m.SessionID, m.ProviderSessionID
		// The upgrade boundary is durable. Only previously unassigned user
		// inputs are adopted; old terminal reports do not create new work.
		for _, i := range a.pending {
			e := a.events[i]
			if e.state == "received" && e.trigger.Kind != "noise" && a.subscriptions[[2]string{e.trigger.Source, e.trigger.Thread}] == "" {
				a.enqueueMain(i, nil)
			}
		}
		return nil
	}
	item := a.main.inbox[a.main.cursor]
	if item.input.Kind == "user" {
		// Both reductions belong to this single durable main_decided record.
		if err := a.apply(transition{State: "routing", Index: item.event}, replay); err != nil {
			return err
		}
		terminal := transition{State: "ignored", Index: item.event, Text: "Main session handled this event."}
		if tr.State == "main_failed" {
			terminal.State, terminal.Text = "failed", tr.Text
		}
		if m.Assignment != nil {
			terminal = *m.Assignment
			a.events[item.event].delegation = m.DecisionID
		}
		if err := a.apply(terminal, replay); err != nil {
			return err
		}
	}
	if tr.State == "main_failed" {
		a.main.failures = append(a.main.failures, MainFailure{InputID: m.InputID, Text: tr.Text})
	} else {
		a.main.snapshot = slices.Clone(m.Snapshot)
		if m.Decision.Action == "steer" || m.Decision.Action == "cancel" {
			a.main.controls = append(a.main.controls, MainControl{ID: "control-" + m.InputID, DecisionID: m.DecisionID, InputID: m.InputID, Source: item.input.Trigger.Source, Thread: item.input.Trigger.Thread, EventID: item.input.Trigger.ID, Action: m.Decision.Action, Target: *m.Decision.Target, Text: m.Decision.Text, Status: "pending"})
			c := &a.main.controls[len(a.main.controls)-1]
			c.evidence = a.main.chatRouting
			if a.main.controlIndex == nil {
				a.main.controlIndex = make(map[string]int)
			}
			a.main.controlIndex[c.ID] = len(a.main.controls) - 1
			e := a.controlEvent(c.Target)
			e.controls = append(e.controls, c.ID)
		}
		if m.Decision.Action == "reply" || m.Decision.Action == "clarify" {
			reply := Reply{ID: m.ReplyID, DecisionID: m.DecisionID, InputID: m.InputID, DelegationID: item.input.DelegationID, Source: item.input.Trigger.Source, Thread: item.input.Trigger.Thread, EventID: item.input.Trigger.ID, Text: m.Decision.Text, Status: "pending", Report: copyReport(item.input.Report), Control: copyControl(item.input.Control)}
			reply.TaskID = item.input.TaskID
			if reply.Report != nil {
				reply.TaskID = reply.Report.TaskID
			}
			a.main.replies = append(a.main.replies, reply)
		}
	}
	a.main.cursor++
	return nil
}

// MainSnapshot returns a detached copy of the private main transcript.
func (a *SuperAgent) MainSnapshot() (agent.AgentSnapshot, bool, error) {
	a.mu.Lock()
	raw := a.main.snapshot
	a.mu.Unlock()
	if len(raw) == 0 {
		return agent.AgentSnapshot{}, false, nil
	}
	var snapshot agent.AgentSnapshot
	err := decodeSnapshot(raw, &snapshot)
	return snapshot, err == nil, err
}

func (a *SuperAgent) MainFailures() []MainFailure {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.main.failures)
}

// MainFailureCause is a private process-local diagnostic. A main failure
// while consuming a result never replaces the Worker's own FailureCause.
func (a *SuperAgent) MainFailureCause(inputID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.main.causes[inputID]
}

// Replies returns durable outbox history, including acknowledged and uncertain
// deliveries. A host sends only pending entries, using ID as its idempotency key.
func (a *SuperAgent) Replies() []Reply {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := slices.Clone(a.main.replies)
	for i := range out {
		out[i].Report = copyReport(out[i].Report)
		out[i].Control = copyControl(out[i].Control)
		out[i].Attempts = slices.Clone(out[i].Attempts)
	}
	return out
}
