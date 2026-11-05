package superagent

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// MainControl is a durable action selected by the main session for one exact
// invocation. queued is not delivered. delivered means dequeued at a Worker
// input boundary, not executed by its model or tools. cancel_requested is not
// completion: inspect the Worker's Report and receipts for actual effects.
// An interrupted delivery becomes needs_review on restart and is never replayed.
type MainControl struct {
	ID         string    `json:"id"`
	DecisionID string    `json:"decision_id"`
	InputID    string    `json:"input_id"`
	Source     string    `json:"source"`
	Thread     string    `json:"thread"`
	EventID    string    `json:"event_id"`
	Action     string    `json:"action"` // steer or cancel
	Target     RunTarget `json:"target"`
	Text       string    `json:"text,omitempty"`
	Status     string    `json:"status"` // pending, queued, delivered, cancel_requested, not_delivered, needs_review
}

// MainControls returns detached durable control history, including pending,
// unissued and uncertain instructions. Use ID, not progress events, to dedupe.
func (a *SuperAgent) MainControls() []MainControl {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.main.controls)
}

func (a *SuperAgent) mainControl(id string) *MainControl {
	for i := range a.main.controls {
		if a.main.controls[i].ID == id {
			return &a.main.controls[i]
		}
	}
	return nil
}

func (a *SuperAgent) controlEvent(target RunTarget) *item {
	i, ok := a.accepted[[2]string{target.Source, target.EventID}]
	if !ok || a.events[i].task != target.TaskID {
		return nil
	}
	return &a.events[i]
}

func (a *SuperAgent) validateControlDecision(input MainInput, m *mainTransition) error {
	d := m.Decision
	if input.Kind != "user" || input.Trigger.Kind != "request" || d.Target == nil || d.TaskID != "" || m.Assignment != nil || m.ReplyID != "" {
		return errors.New("main control requires an explicit request and exact invocation")
	}
	e := a.controlEvent(*d.Target)
	if e == nil || e.state != "running" {
		return errors.New("main control target is not running")
	}
	if d.Action == "steer" {
		if len(d.Text) > maxSteerBytes || strings.TrimSpace(d.Text) == "" || !utf8.ValidString(d.Text) {
			return errors.New("invalid main steering text")
		}
	} else if d.Text != "" {
		return errors.New("cancel action must not contain steering text")
	}
	return nil
}

func (a *SuperAgent) validateMainControl(tr transition) error {
	if len(tr.Main.ControlIDs) == 0 {
		return errors.New("missing control identity")
	}
	seen := make(map[string]bool, len(tr.Main.ControlIDs))
	for _, id := range tr.Main.ControlIDs {
		c := a.mainControl(id)
		if c == nil || seen[id] {
			return errors.New("unknown or repeated control identity")
		}
		seen[id] = true
		e := a.controlEvent(c.Target)
		switch tr.State {
		case "control_queued":
			if c.Action != "steer" || c.Status != "pending" || e == nil || e.state != "running" {
				return errors.New("invalid control enqueue")
			}
		case "control_delivered":
			if c.Action != "steer" || c.Status != "queued" || e == nil || e.state != "running" {
				return errors.New("invalid control delivery")
			}
		case "control_cancel_requested":
			if c.Action != "cancel" || c.Status != "pending" || e == nil || e.state != "running" {
				return errors.New("invalid control cancellation")
			}
		case "control_not_delivered":
			if c.Status != "pending" && c.Status != "queued" {
				return errors.New("invalid control drop")
			}
		case "control_needs_review":
			if c.Status != "delivered" || e == nil || e.state != "interrupted" {
				return errors.New("invalid uncertain control")
			}
		default:
			return errors.New("unknown main control state")
		}
	}
	return nil
}

func (a *SuperAgent) commitControls(status string, ids []string) error {
	return a.commit(transition{State: "control_" + status, Main: &mainTransition{ControlIDs: ids}})
}

// Only the scheduler issues control actions. The main_decided record already
// contains the consumed input, snapshot and stable pending action identity.
func (a *SuperAgent) dispatchMainControl() bool {
	for _, c := range a.main.controls {
		if c.Status != "pending" {
			continue
		}
		r, err := a.activeLocked(c.Target)
		if err != nil || r.aborted || (c.Action == "steer" && len(r.queue) >= maxPendingSteers) {
			a.commitControls("not_delivered", []string{c.ID})
			return true
		}
		if c.Action == "steer" {
			if a.commitControls("queued", []string{c.ID}) == nil {
				r.queue = append(r.queue, steering{text: c.Text, controlID: c.ID, receipt: &SteerReceipt{done: make(chan struct{})}})
				a.publishLocked(c.Target, "steer_queued")
			}
		} else if a.commitControls("cancel_requested", []string{c.ID}) == nil {
			r.aborted = true
			a.dropRunSteeringLocked(r)
			a.publishLocked(c.Target, "abort_requested")
			// This is our own nonblocking context cancellation, never a Worker callback.
			r.cancel()
		}
		return true
	}
	return false
}

func (a *SuperAgent) recoverMainControls() error {
	for _, c := range a.main.controls {
		status := ""
		switch c.Status {
		case "pending", "queued":
			status = "not_delivered"
		case "delivered":
			if e := a.controlEvent(c.Target); e != nil && e.state == "interrupted" {
				status = "needs_review"
			}
		}
		if status != "" {
			if err := a.commitControls(status, []string{c.ID}); err != nil {
				return err
			}
		}
	}
	return nil
}
