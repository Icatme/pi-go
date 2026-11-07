package superagent

import (
	"errors"
	"reflect"
	"strconv"
)

// WatchdogFollowupTask contains committed task facts only. Status is the last
// durable outcome, not a claim that the current invocation has finished.
type WatchdogFollowupTask struct {
	TaskID                string    `json:"task_id"`
	LastStatus            string    `json:"last_status"`
	Pending               int       `json:"pending"`
	NeedsReview           bool      `json:"needs_review"`
	GoalDeliveryID        string    `json:"goal_delivery_id"`
	LatestInputDeliveryID string    `json:"latest_input_delivery_id"`
	RunningOrigin         RunTarget `json:"running_origin"`
	RunningDeliveryID     string    `json:"running_delivery_id,omitempty"`
	LastReportID          string    `json:"last_report_id,omitempty"`
	LastReportStatus      string    `json:"last_report_status,omitempty"`
	LastReport            string    `json:"last_report,omitempty"`
}

// WatchdogFollowup is a one-shot observation, not a resolution, notification or
// renewed authorization. Request always names the original intervention/run.
type WatchdogFollowup struct {
	Sequence       uint64                 `json:"sequence"`
	ID             string                 `json:"id"`
	InterventionID string                 `json:"intervention_id"`
	Request        WatchdogRefreshRequest `json:"request"`
	ObservedAt     int64                  `json:"observed_at"` // wall clock, independent of WatchdogClock
	Source         string                 `json:"source"`      // coordinator_observation
	Class          string                 `json:"class"`       // needs_review, operation_exited, operation_running, operation_reserved
	Operation      WatchdogOperation      `json:"operation"`
	Task           WatchdogFollowupTask   `json:"task"`
}

type WatchdogFollowupPage struct {
	Followups []WatchdogFollowup `json:"followups"`
	Cursor    uint64             `json:"cursor"`
	HasMore   bool               `json:"has_more"`
}

// WatchdogFollowupsSince returns detached evidence with a durable exclusive
// cursor. The host owns delivery and acknowledgement. Limit defaults to 32 and
// is capped at 32. An ahead-of-log cursor is preserved, never rewound.
func (a *SuperAgent) WatchdogFollowupsSince(after uint64, limit int) WatchdogFollowupPage {
	a.mu.Lock()
	defer a.mu.Unlock()
	if limit <= 0 || limit > 32 {
		limit = 32
	}
	p := WatchdogFollowupPage{Cursor: after}
	for _, f := range a.watchdog.followups {
		if f.Sequence <= after {
			continue
		}
		if len(p.Followups) == limit {
			p.HasMore = true
			break
		}
		p.Followups = append(p.Followups, f)
		p.Cursor = f.Sequence
	}
	return p
}

type watchdogFollowupBatch struct {
	ObservedAt int64              `json:"observed_at"`
	Followups  []WatchdogFollowup `json:"followups,omitempty"`
}

func (a *SuperAgent) watchdogFollowupTask(id string) WatchdogFollowupTask {
	t := a.tasks[id]
	out := WatchdogFollowupTask{TaskID: id, LastStatus: t.last, Pending: t.pending, NeedsReview: t.review != 0 || t.backgroundReview != 0}
	for n, e := range a.events {
		if e.task != id {
			continue
		}
		if e.trigger.Source != backgroundSource {
			if out.GoalDeliveryID == "" {
				out.GoalDeliveryID = deliveryID(n)
			}
			out.LatestInputDeliveryID = deliveryID(n)
		}
		if e.state == "running" {
			out.RunningOrigin = RunTarget{TaskID: id, Source: e.trigger.Source, EventID: e.trigger.ID}
			out.RunningDeliveryID = deliveryID(n)
		}
	}
	for n := len(a.reports) - 1; n >= 0; n-- {
		r := a.reports[n]
		if r.TaskID == id {
			out.LastReportID = r.DeliveryID
			out.LastReportStatus = r.Status
			out.LastReport = brief(r.Text)
			break
		}
	}
	return out
}

// Caller holds mu. Selection is independent of the ordinary task cursor and
// sorted by immutable intervention reservation order. Pending reservations are
// excluded forever unless their original Inspect actually commits completion.
// The supplied time makes due selection deterministic without a runtime hook.
func (a *SuperAgent) watchdogFollowupsDue(now int64) []WatchdogFollowup {
	seen := make(map[string]bool, len(a.watchdog.followups))
	for _, f := range a.watchdog.followups {
		seen[f.InterventionID] = true
	}
	var out []WatchdogFollowup
	for _, i := range a.watchdog.Interventions {
		if i.State != "completed" || i.RecheckAt > now || seen[i.ID] {
			continue
		}
		op, _ := a.backgroundOperation(i.Request.OperationID)
		task := a.watchdogFollowupTask(i.Request.TaskID)
		class := "operation_reserved"
		switch {
		case op.NeedsReview || task.NeedsReview:
			class = "needs_review"
		case op.Exited:
			class = "operation_exited"
		case op.Status == "running":
			class = "operation_running"
		}
		seq := uint64(len(a.watchdog.followups) + len(out) + 1)
		out = append(out, WatchdogFollowup{Sequence: seq, ID: a.watchdog.SessionID + "/followup/" + strconv.FormatUint(seq, 10), InterventionID: i.ID, Request: i.Request, ObservedAt: now, Source: "coordinator_observation", Class: class, Operation: publicWatchdogOperation(op), Task: task})
		if len(out) == a.watchdog.Policy.BatchSize {
			break
		}
	}
	return out
}

func (a *SuperAgent) validateWatchdogFollowups(tr transition) error {
	b := tr.Watchdog.Followups
	if tr.State != "watchdog_checked_followups" {
		if b != nil || (tr.State == "watchdog_checked" && a.watchdog.followupVersion) {
			return errors.New("watchdog: unexpected followups or downgraded check")
		}
		return nil
	}
	if b == nil || b.ObservedAt < 0 || b.ObservedAt > 1<<62 {
		return errors.New("watchdog: invalid followup clock")
	}
	expected := a.watchdogFollowupsDue(b.ObservedAt)
	if !reflect.DeepEqual(b.Followups, expected) {
		return errors.New("watchdog: inconsistent followup batch")
	}
	return nil
}
