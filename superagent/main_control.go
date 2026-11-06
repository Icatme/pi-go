package superagent

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
)

// MainControl is a durable action selected by the main session for one exact
// invocation. queued is not delivered. delivered means dequeued at a Worker
// input boundary, not executed by its model or tools. cancel_requested is not
// completion: inspect the Worker's Report and receipts for actual effects.
// Missing accepted-input evidence becomes needs_review and is never replayed.
// Older untagged deliveries retain the conservative interrupted-run behavior.
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
	// InputAccepted is positive evidence in the target invocation's saved
	// user-message history, not evidence that the model obeyed the correction.
	InputAccepted bool `json:"input_accepted,omitempty"`
	evidence      bool // activated by main_chat_opened; never inferred for old records
}

// MainControls returns detached durable control history, including pending,
// unissued and uncertain instructions. Use ID, not progress events, to dedupe.
func (a *SuperAgent) MainControls() []MainControl {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.main.controls)
}

func copyControl(c *MainControl) *MainControl {
	if c == nil {
		return nil
	}
	copy := *c
	return &copy
}

func (a *SuperAgent) mainControl(id string) *MainControl {
	if i, ok := a.main.controlIndex[id]; ok {
		return &a.main.controls[i]
	}
	return nil
}

func (a *SuperAgent) controlEvent(target RunTarget) *item {
	i, ok := a.deliveryIndex(target)
	if !ok {
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
			if c.Status != "delivered" || c.InputAccepted || e == nil || e.state != "interrupted" {
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
			if e := a.controlEvent(c.Target); !c.InputAccepted && e != nil && e.state == "interrupted" {
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

const controlMetadata = "superagent.control"

// All fields are strings so the marker survives snapshot JSON round trips.
// Only host-created user messages carry this envelope; matching text alone
// (including text that looks like an envelope) is never acceptance evidence.
func controlMessage(c MainControl) agent.Message {
	m := agent.NewUserTextMessage(c.Text)
	m.Metadata = map[string]any{
		controlMetadata:                 c.ID,
		"superagent.control_source":     c.Source,
		"superagent.control_event":      c.EventID,
		"superagent.control_task":       c.Target.TaskID,
		"superagent.control_run_source": c.Target.Source,
		"superagent.control_run_event":  c.Target.EventID,
	}
	return m
}

// Called only when the target Worker's terminal snapshot is already decoded.
// Scan that snapshot once, not on every admission, poll or later task run.
// The terminal record atomically preserves this derived evidence on replay.
func (a *SuperAgent) recordControlInputs(index int, snapshot agent.AgentSnapshot) {
	waiting := make(map[string]*MainControl)
	for _, id := range a.events[index].controls {
		c := a.mainControl(id)
		if c.evidence && c.Action == "steer" && c.Status == "delivered" {
			waiting[c.ID] = c
		}
	}
	if len(waiting) == 0 {
		return
	}
	for _, m := range snapshot.Messages {
		id, _ := m.Metadata[controlMetadata].(string)
		c := waiting[id]
		if c == nil || m.Role != agent.RoleUser || messageText(m) != c.Text || snapshot.SessionID != c.Target.TaskID {
			continue
		}
		if m.Metadata["superagent.control_source"] == c.Source && m.Metadata["superagent.control_event"] == c.EventID &&
			m.Metadata["superagent.control_task"] == c.Target.TaskID && m.Metadata["superagent.control_run_source"] == c.Target.Source &&
			m.Metadata["superagent.control_run_event"] == c.Target.EventID {
			c.InputAccepted = true
		}
	}
	for _, id := range a.events[index].controls {
		if c := waiting[id]; c != nil && !c.InputAccepted {
			c.Status = "needs_review"
			a.enqueueControlResult(c)
		}
	}
}

func (a *SuperAgent) enqueueControlResult(c *MainControl) {
	if !c.evidence || (c.Status != "not_delivered" && c.Status != "needs_review") {
		return
	}
	i := a.accepted[[2]string{c.Source, c.EventID}]
	e := a.events[i]
	input := MainInput{ID: a.nextMainInputID(), DeliveryID: deliveryID(i), Kind: "control_result", Trigger: e.trigger, TaskID: c.Target.TaskID, Control: copyControl(c)}
	a.main.inbox = append(a.main.inbox, mainItem{input: input, event: i})
}
