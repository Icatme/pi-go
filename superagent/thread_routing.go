package superagent

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

const maxThreadTargets = 128
const maxTaskThreads = 128
const maxWorkerSubscriptionChanges = 16

// ThreadSubscription is a host-authorized relation, separate from Subscribe's
// lossy progress stream. One journal represents one authorization domain.
type ThreadSubscription struct {
	TaskID     string `json:"task_id"`
	Source     string `json:"source"`
	Thread     string `json:"thread"`
	Subscribed bool   `json:"subscribed"`
}

// Handback is a Worker's durable request for main-session clarification.
// Trigger comes from the original admission; a Worker cannot replace it.
// Handled is a public description of what was already processed, not a claim
// that side effects were rolled back. Inspect the separate terminal report.
type Handback struct {
	ID      string    `json:"id"`
	Target  RunTarget `json:"target"`
	Trigger Trigger   `json:"trigger"`
	Reason  string    `json:"reason"`
	Handled string    `json:"handled"`
}

type threadTransition struct {
	Targets      []string            `json:"targets,omitempty"`
	Subscription *ThreadSubscription `json:"subscription,omitempty"`
	Target       *RunTarget          `json:"target,omitempty"`
	Handback     *Handback           `json:"handback,omitempty"`
}

// SubmitThread admits one external event and atomically freezes its current
// subscription targets. Later relation changes do not alter queued deliveries.
// With no subscribers, main handles it once as an unassociated input. Only a
// trusted adapter may select this entry point; ordinary chat uses Submit.
func (a *SuperAgent) SubmitThread(t Trigger) (bool, error) {
	return a.submit(t, "", true)
}

// SetThreadSubscription durably changes one relation for an existing task.
// This trusted host API grants access to future events from the named Thread;
// never pass unvalidated model-selected task or Thread identities to it.
// Repeated requests for the current state are no-ops. Only OpenMain supports it.
func (a *SuperAgent) SetThreadSubscription(taskID, source, thread string, subscribed bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setThreadSubscription(ThreadSubscription{taskID, source, thread, subscribed}, nil)
}

func (a *SuperAgent) setThreadSubscription(s ThreadSubscription, target *RunTarget) error {
	if a.fatal != nil {
		return a.fatal
	}
	if err := a.ctx.Err(); err != nil {
		return err
	}
	if !a.main.chatRouting || a.tasks[s.TaskID] == nil || !validIdentity(s.Source) || !validIdentity(s.Thread) {
		return errors.New("subscription requires OpenMain, an existing task and valid source/thread")
	}
	if slices.Contains(a.threadSubscriptions[[2]string{s.Source, s.Thread}], s.TaskID) == s.Subscribed {
		return nil
	}
	return a.commit(transition{State: "thread_subscription", Thread: &threadTransition{Subscription: &s, Target: target}})
}

// ThreadSubscriptions returns a detached ordered target list for a Thread.
func (a *SuperAgent) ThreadSubscriptions(source, thread string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.threadSubscriptions[[2]string{source, thread}])
}

// Handbacks returns durable clarification requests in admission/target order.
func (a *SuperAgent) Handbacks() []Handback {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Handback
	for _, e := range a.events {
		if e.handback != nil {
			out = append(out, *e.handback)
		}
	}
	return out
}

func copyHandback(h *Handback) *Handback {
	if h == nil {
		return nil
	}
	c := *h
	return &c
}

// Delivery lookup must include the task: multiple deliveries share an ingress
// identity. All items from a fanout admission are contiguous and immutable.
func (a *SuperAgent) deliveryIndex(target RunTarget) (int, bool) {
	first, ok := a.accepted[[2]string{target.Source, target.EventID}]
	if !ok {
		return 0, false
	}
	for i := first; i < len(a.events) && a.events[i].admission == first; i++ {
		if a.events[i].task == target.TaskID {
			return i, true
		}
	}
	return 0, false
}

func (a *SuperAgent) workerRouteAllowed(ctx context.Context, index int, active *activeRun) error {
	if ctx == nil {
		return errors.New("worker routing requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r, err := a.activeLocked(active.target)
	if err != nil {
		return err
	}
	if r != active || r.aborted || r.ctx.Err() != nil || a.events[index].state != "running" {
		return errors.New("worker routing callback is no longer active")
	}
	return nil
}

func (a *SuperAgent) workerSubscription(ctx context.Context, index int, active *activeRun, subscribed bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.workerRouteAllowed(ctx, index, active); err != nil {
		return err
	}
	e := a.events[index]
	return a.setThreadSubscription(ThreadSubscription{e.task, e.trigger.Source, e.trigger.Thread, subscribed}, &active.target)
}

func (a *SuperAgent) workerHandback(ctx context.Context, index int, active *activeRun, reason, handled string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.workerRouteAllowed(ctx, index, active); err != nil {
		return err
	}
	e := a.events[index]
	h := Handback{ID: fmt.Sprintf("handback-%d", index+1), Target: active.target, Trigger: e.trigger, Reason: reason, Handled: handled}
	if e.handback != nil {
		if *e.handback != h {
			return errors.New("delivery already handed back with different contents")
		}
		return nil
	}
	return a.commit(transition{State: "thread_handback", Index: index, Thread: &threadTransition{Handback: &h}})
}

// Validate the entire new record before append or mutation, including replay.
func (a *SuperAgent) validateThread(tr transition) error {
	m := tr.Thread
	if m == nil || tr.Main != nil || !a.main.chatRouting || tr.Task != "" || tr.ProviderSessionID != "" || tr.Text != "" || len(tr.Snapshot) != 0 || len(tr.Receipts) != 0 || tr.NeedsReview {
		return errors.New("invalid thread transition envelope")
	}
	switch tr.State {
	case "thread_received":
		if tr.Index != len(a.events) || tr.Trigger == nil || m.Subscription != nil || m.Target != nil || m.Handback != nil {
			return errors.New("invalid thread admission")
		}
		if err := validTrigger(*tr.Trigger); err != nil {
			return err
		}
		if tr.Trigger.Kind == "noise" || len(m.Targets) > maxThreadTargets {
			return errors.New("invalid thread admission kind or target count")
		}
		if _, exists := a.accepted[[2]string{tr.Trigger.Source, tr.Trigger.ID}]; exists {
			return errors.New("duplicate thread admission")
		}
		seen := make(map[string]bool, len(m.Targets))
		for _, id := range m.Targets {
			if a.tasks[id] == nil || seen[id] {
				return errors.New("unknown or duplicate thread target")
			}
			seen[id] = true
		}
		if !slices.Equal(m.Targets, a.threadSubscriptions[[2]string{tr.Trigger.Source, tr.Trigger.Thread}]) {
			return errors.New("thread targets do not match the admission snapshot")
		}
	case "thread_subscription":
		if tr.Index != 0 || tr.Trigger != nil || len(m.Targets) != 0 || m.Subscription == nil || m.Handback != nil {
			return errors.New("invalid subscription transition")
		}
		s := *m.Subscription
		if a.tasks[s.TaskID] == nil || !validIdentity(s.Source) || !validIdentity(s.Thread) {
			return errors.New("invalid subscription identity")
		}
		ids := a.threadSubscriptions[[2]string{s.Source, s.Thread}]
		if slices.Contains(ids, s.TaskID) == s.Subscribed {
			return errors.New("subscription transition does not change the relation")
		}
		if s.Subscribed {
			count := 0
			for _, targets := range a.threadSubscriptions {
				if slices.Contains(targets, s.TaskID) {
					count++
				}
			}
			if len(ids) >= maxThreadTargets || count >= maxTaskThreads {
				return errors.New("subscription limit reached")
			}
		}
		if m.Target != nil {
			e := a.controlEvent(*m.Target)
			if e == nil || e.state != "running" || e.task != s.TaskID || e.trigger.Source != s.Source || e.trigger.Thread != s.Thread || e.routeActions >= maxWorkerSubscriptionChanges {
				return errors.New("worker subscription is outside its active delivery or mutation budget")
			}
		}
	case "thread_handback":
		if tr.Trigger != nil || len(m.Targets) != 0 || m.Subscription != nil || m.Target != nil || m.Handback == nil || tr.Index < 0 || tr.Index >= len(a.events) {
			return errors.New("invalid handback transition")
		}
		e, h := a.events[tr.Index], m.Handback
		if e.state != "running" || e.handback != nil || h.ID != fmt.Sprintf("handback-%d", tr.Index+1) || h.Target != (RunTarget{e.task, e.trigger.Source, e.trigger.ID}) || h.Trigger != e.trigger || !validNote(h.Reason) || !validNote(h.Handled) {
			return errors.New("invalid handback identity, payload or delivery state")
		}
	default:
		return errors.New("unknown thread transition")
	}
	return nil
}

func (a *SuperAgent) applyThread(tr transition) error {
	if err := a.validateThread(tr); err != nil {
		return err
	}
	m := tr.Thread
	switch tr.State {
	case "thread_received":
		a.accepted[[2]string{tr.Trigger.Source, tr.Trigger.ID}] = tr.Index
		targets := m.Targets
		if len(targets) == 0 {
			targets = []string{""} // durable main input, even with no deliveries
		}
		for _, id := range targets {
			i := len(a.events)
			a.events = append(a.events, item{trigger: *tr.Trigger, state: "received", entryTask: id, threadEntry: true, admission: tr.Index, chatEntry: true, mainAdmitted: true})
			a.pending = append(a.pending, i)
			a.enqueueMain(i, nil)
		}
	case "thread_subscription":
		s := *m.Subscription
		key := [2]string{s.Source, s.Thread}
		if s.Subscribed {
			a.threadSubscriptions[key] = append(a.threadSubscriptions[key], s.TaskID)
		} else {
			a.threadSubscriptions[key] = slices.DeleteFunc(a.threadSubscriptions[key], func(id string) bool { return id == s.TaskID })
			if len(a.threadSubscriptions[key]) == 0 {
				delete(a.threadSubscriptions, key)
			}
		}
		if m.Target != nil {
			a.controlEvent(*m.Target).routeActions++
		}
	case "thread_handback":
		e := &a.events[tr.Index]
		e.handback = copyHandback(m.Handback)
		input := MainInput{ID: a.nextMainInputID(), DeliveryID: deliveryID(tr.Index), Kind: "handback", Trigger: e.trigger, TaskID: e.task, Handback: copyHandback(e.handback)}
		a.main.inbox = append(a.main.inbox, mainItem{input: input, event: tr.Index})
	}
	return nil
}
