package superagent

import (
	"context"
	"errors"
)

// Progress is a live observation scoped to one task. Sequence numbers are
// process-local. A gap indicates that a slow subscriber missed older updates.
// DeliveryID correlates a delivery but does not make this stream durable.
// Use ReportsSince for terminal outcomes; one trigger may have many targets.
type Progress struct {
	TaskID     string `json:"task_id"`
	DeliveryID string `json:"delivery_id,omitempty"`
	Source     string `json:"source,omitempty"`
	EventID    string `json:"event_id,omitempty"`
	Kind       string `json:"kind"`
	Sequence   uint64 `json:"sequence"`
}

type progressHub struct {
	byTask map[string]map[chan Progress]<-chan struct{}
	count  int
}

const progressBuffer = 16
const maxProgressSubscribers = 128

// Subscribe returns the current state followed by live updates for one known
// task. Each subscriber keeps the last 16 unread updates. Cancel ctx to
// unsubscribe. Closing the coordinator closes all subscription channels.
// This observes progress only; it never changes Thread routing subscriptions.
func (a *SuperAgent) Subscribe(ctx context.Context, taskID string) (<-chan Progress, error) {
	if ctx == nil {
		return nil, errors.New("superagent: subscription requires a context")
	}
	done := ctx.Done()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.fatal != nil {
		return nil, a.fatal
	}
	if err := a.ctx.Err(); err != nil {
		return nil, err
	}
	t := a.tasks[taskID]
	if t == nil {
		return nil, errors.New("superagent: unknown task")
	}
	if a.progress.count >= maxProgressSubscribers {
		// A caller can cancel and immediately replace a subscription before
		// its cleanup goroutine has run. Count live subscriptions only.
		for id, group := range a.progress.byTask {
			for ch, stopped := range group {
				select {
				case <-stopped:
					a.removeSubscriptionLocked(id, ch)
				default:
				}
			}
		}
		if a.progress.count >= maxProgressSubscribers {
			return nil, errors.New("superagent: subscriber limit reached")
		}
	}
	if a.progress.byTask == nil {
		a.progress.byTask = make(map[string]map[chan Progress]<-chan struct{})
	}
	group := a.progress.byTask[taskID]
	if group == nil {
		group = make(map[chan Progress]<-chan struct{})
		a.progress.byTask[taskID] = group
	}
	ch := make(chan Progress, progressBuffer)
	group[ch] = done
	a.progress.count++
	ch <- a.currentProgressLocked(t)
	go func() {
		select {
		case <-done:
		case <-a.ctx.Done():
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		a.removeSubscriptionLocked(taskID, ch)
	}()
	return ch, nil
}

func (a *SuperAgent) removeSubscriptionLocked(taskID string, ch chan Progress) {
	group := a.progress.byTask[taskID]
	if _, ok := group[ch]; !ok {
		return
	}
	delete(group, ch)
	if len(group) == 0 {
		delete(a.progress.byTask, taskID)
	}
	a.progress.count--
	close(ch)
}

func (a *SuperAgent) currentProgressLocked(t *task) Progress {
	p := Progress{TaskID: t.id, Kind: t.last, Sequence: t.progressSeq}
	if t.review != 0 {
		cause := a.events[t.review-1].trigger
		p.Kind, p.Source, p.EventID = "needs_review", cause.Source, cause.ID
		p.DeliveryID = deliveryID(t.review - 1)
		return p
	}
	if r := t.active; r != nil {
		p.Kind, p.Source, p.EventID = r.phase(), r.target.Source, r.target.EventID
		if index, ok := a.deliveryIndex(r.target); ok {
			p.DeliveryID = deliveryID(index)
		}
		return p
	}
	if t.pending > 0 {
		p.Kind = "queued"
		for _, i := range a.pending {
			e := a.events[i]
			if e.task == t.id && e.state == p.Kind {
				p.Source, p.EventID = e.trigger.Source, e.trigger.ID
				p.DeliveryID = deliveryID(i)
				break
			}
		}
		return p
	}
	for i := len(a.reports) - 1; i >= 0; i-- {
		if r := a.reports[i]; r.TaskID == t.id {
			p.Source, p.EventID = r.Source, r.EventID
			p.DeliveryID = r.DeliveryID
			break
		}
	}
	return p
}

// All publishers and channel closes are serialized by a.mu. Only emit
// fixed phase names, never model text, reasoning, tool arguments or results.
func (a *SuperAgent) publishLocked(target RunTarget, kind string) {
	switch kind {
	case "queued", "running", "turn_start", "turn_end", "tool_start", "tool_end",
		"steer_queued", "steer_delivered", "abort_requested", "finalizing", "completed", "failed",
		"completed_with_tool_errors", "truncated", "blocked", "interrupted", "reconciled":
	default:
		return
	}
	t := a.tasks[target.TaskID]
	if t == nil {
		return
	}
	t.progressSeq++
	p := Progress{TaskID: target.TaskID, Source: target.Source, EventID: target.EventID, Kind: kind, Sequence: t.progressSeq}
	if index, ok := a.deliveryIndex(target); ok {
		p.DeliveryID = deliveryID(index)
	}
	for ch := range a.progress.byTask[t.id] {
		select {
		case ch <- p:
			continue
		default:
		}
		// A reader may have drained the channel after the send failed.
		// We are the sole sender while holding mu, so one free slot suffices.
		select {
		case <-ch:
		default:
		}
		ch <- p
	}
}

func (a *SuperAgent) closeProgressLocked() {
	for _, group := range a.progress.byTask {
		for ch := range group {
			delete(group, ch)
			close(ch)
		}
	}
	a.progress = progressHub{}
}
