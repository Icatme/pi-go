package superagent

import (
	"context"
	"errors"
	"reflect"
	"time"
)

// WatchdogRefreshRequest binds a host decision to one current finding and one
// original background operation/run. Diagnosis text never supplies authority.
// Revision includes the process runtime identity; unconsumed findings from a
// prior coordinator lifetime cannot authorize new observations after restart.
type WatchdogRefreshRequest struct {
	FindingID   string    `json:"finding_id"`
	TaskID      string    `json:"task_id"`
	Revision    string    `json:"revision"`
	OperationID string    `json:"operation_id"`
	Origin      RunTarget `json:"origin"`
	DeliveryID  string    `json:"delivery_id"`
}

// WatchdogIntervention is bounded public evidence. Pending means Inspect may or
// may not have run, never success or permission to retry. Completed does not
// mean the task succeeded. No arguments, output, receipts or private errors are
// exposed here. A fresh check and host decision are required for another attempt.
type WatchdogIntervention struct {
	ID               string                 `json:"id"`
	Request          WatchdogRefreshRequest `json:"request"`
	State            string                 `json:"state"`            // pending, completed
	Result           string                 `json:"result,omitempty"` // unknown, failed, running, exited, stale
	Operation        WatchdogOperation      `json:"operation"`
	ObservedAt       int64                  `json:"observed_at"`
	RecheckAt        int64                  `json:"recheck_at"`
	RecheckCondition string                 `json:"recheck_condition"`
}

// RefreshWatchdog explicitly authorizes one read-only Inspect of the original
// backend ID. It never calls Start, Stop, ReconcileBackground or model code.
// authorize is required, host-owned, read-only and cancellation-aware; do not
// close the coordinator from it. An exact duplicate returns its durable record
// without repeating authorization/Inspect, even after restart. Concurrent
// calls may both authorize before either reserves; only one can Inspect. Incomplete
// reservations are never automatically resumed.
//
// Observation and its result commit atomically. Existing background recovery
// rules apply: unknown retains review/quota; proven exit may enqueue one private
// completion input for the existing Worker. That Worker retains its ordinary
// authority and is not a new task. This API grants no new native permissions.
func (a *SuperAgent) RefreshWatchdog(ctx context.Context, request WatchdogRefreshRequest, authorize func(context.Context, WatchdogRefreshRequest) error) (WatchdogIntervention, error) {
	var empty WatchdogIntervention
	if ctx == nil || authorize == nil {
		return empty, errors.New("watchdog: context and explicit host authorization required")
	}
	a.mu.Lock()
	if err := a.watchdogAllowed(ctx); err != nil {
		a.mu.Unlock()
		return empty, err
	}
	if prior, ok := a.watchdogIntervention(request.FindingID, request.OperationID); ok {
		a.mu.Unlock()
		if prior.Request != request {
			return empty, errors.New("watchdog: intervention binding differs")
		}
		return prior, nil
	}
	if err := a.currentWatchdogRefresh(request); err != nil {
		a.mu.Unlock()
		return empty, err
	}
	a.wg.Add(1)
	owned, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(a.ctx, cancel)
	a.mu.Unlock()
	defer func() { stop(); cancel(); a.wg.Done() }()
	if err := callWatchdogAuthorization(owned, authorize, request); err != nil {
		return empty, err
	}
	a.mu.Lock()
	if err := a.watchdogAllowed(owned); err != nil {
		a.mu.Unlock()
		return empty, err
	}
	// Another caller can reserve while authorization is in flight.
	if prior, ok := a.watchdogIntervention(request.FindingID, request.OperationID); ok {
		a.mu.Unlock()
		if prior.Request != request {
			return empty, errors.New("watchdog: intervention binding differs")
		}
		return prior, nil
	}
	if err := a.currentWatchdogRefresh(request); err != nil {
		a.mu.Unlock()
		return empty, err
	}
	op, _ := a.backgroundOperation(request.OperationID)
	backend, err := a.backgroundBackend(op)
	if err != nil {
		a.mu.Unlock()
		return empty, errors.New("watchdog: original backend grant unavailable")
	}
	now := time.Now().UnixMilli()
	record := WatchdogIntervention{ID: request.FindingID + "/refresh/" + request.OperationID, Request: request, State: "pending", Operation: publicWatchdogOperation(op), ObservedAt: now, RecheckAt: now + a.watchdog.Policy.Interval.Milliseconds(), RecheckCondition: "inspect_outcome_unrecorded"}
	err = a.commit(transition{State: "watchdog_refresh_reserved", Watchdog: &watchdogTransition{Intervention: &record}})
	a.mu.Unlock()
	if err != nil {
		return empty, err
	}
	if err = owned.Err(); err != nil {
		return record, err
	}
	observation, inspectErr := callBackgroundInspect(owned, backend, request.OperationID)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err = a.watchdogAllowed(owned); err != nil {
		return record, err
	}
	record.State = "completed"
	record.ObservedAt = max(record.ObservedAt, time.Now().UnixMilli())
	record.RecheckAt = record.ObservedAt + a.watchdog.Policy.Interval.Milliseconds()
	record.RecheckCondition = "new_finding_and_host_authorization"
	op, _ = a.backgroundOperation(request.OperationID)
	var accepted *BackgroundOperation
	if a.currentWatchdogRefresh(request) != nil {
		record.Result = "stale"
	} else {
		next := observedBackground(op, observation, inspectErr)
		record.Result = observation.State
		if inspectErr != nil || !validBackgroundObservation(observation, op.ID) || !backgroundFactsAdvance(op, observation) {
			record.Result = "failed"
		}
		if !reflect.DeepEqual(next, op) {
			accepted = &next
		}
		op = next
	}
	record.Operation = publicWatchdogOperation(op)
	if err = a.commit(transition{State: "watchdog_refresh_finished", Watchdog: &watchdogTransition{Intervention: &record, Operation: accepted}}); err != nil {
		return empty, err
	}
	return record, nil
}

func callWatchdogAuthorization(ctx context.Context, f func(context.Context, WatchdogRefreshRequest) error, r WatchdogRefreshRequest) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("watchdog: host authorization failed")
		}
	}()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f(ctx, r) != nil {
		return errors.New("watchdog: host authorization denied")
	}
	return nil
}
func publicWatchdogOperation(o BackgroundOperation) WatchdogOperation {
	return WatchdogOperation{ID: o.ID, TaskID: o.TaskID, DeliveryID: o.DeliveryID, Tool: o.Tool, State: o.Status, NeedsReview: o.NeedsReview, Connectivity: "unknown"}
}
func (a *SuperAgent) watchdogIntervention(finding, operation string) (WatchdogIntervention, bool) {
	for _, i := range a.watchdog.Interventions {
		if i.Request.FindingID == finding && i.Request.OperationID == operation {
			return i, true
		}
	}
	return WatchdogIntervention{}, false
}

// Durable validation deliberately excludes live runtime counters. They are
// checked separately before dispatch; replay cannot reconstruct those counters.
func (a *SuperAgent) watchdogRefreshBinding(r WatchdogRefreshRequest) bool {
	f, ok := a.watchdog.latest[r.TaskID]
	if !ok || f.Stale || f.ID != r.FindingID || f.Task.Revision != r.Revision || !a.watchdogRefreshFactsCurrent(f.Task) {
		return false
	}
	op, ok := a.backgroundOperation(r.OperationID)
	if !ok || op.TaskID != r.TaskID || op.Origin != r.Origin || op.DeliveryID != r.DeliveryID {
		return false
	}
	for _, seen := range f.Task.Operations {
		if seen.ID == op.ID && seen.TaskID == op.TaskID && seen.DeliveryID == op.DeliveryID && seen.Tool == op.Tool {
			return true
		}
	}
	return false
}

// Compare replayable public facts while excluding only process-local evidence.
// Replay has no activeRun object, so reconstruct its identity from the original
// durable running delivery. Finalizing/abort-requested are volatile phases.
func (a *SuperAgent) watchdogRefreshFactsCurrent(observed WatchdogTask) bool {
	current := a.watchdogTaskLocked(observed.TaskID)
	for n, event := range a.events {
		if event.entryTask == observed.TaskID && event.task == "" && (event.state == "received" || event.state == "routing") {
			return false
		}
		if event.task == observed.TaskID && event.state == "running" {
			current.Target = RunTarget{TaskID: observed.TaskID, Source: event.trigger.Source, EventID: event.trigger.ID}
			current.DeliveryID = deliveryID(n)
		}
	}
	normalize := func(t WatchdogTask) WatchdogTask {
		t.RuntimeID = ""
		t.Revision = ""
		t.Turns = 0
		t.ToolsStarted = 0
		t.ToolsCompleted = 0
		if t.Status == "finalizing" || t.Status == "abort_requested" {
			t.Status = "running"
		}
		for n := range t.Operations {
			t.Operations[n].Connectivity = "unknown"
		}
		return t
	}
	return reflect.DeepEqual(normalize(current), normalize(cloneWatchdog(observed)))
}
func (a *SuperAgent) currentWatchdogRefresh(r WatchdogRefreshRequest) error {
	if !a.watchdogRefreshBinding(r) || a.watchdogTaskLocked(r.TaskID).Revision != r.Revision {
		return errors.New("watchdog: stale or mismatched finding")
	}
	op, _ := a.backgroundOperation(r.OperationID)
	if op.Exited {
		return errors.New("watchdog: operation already exited")
	}
	return nil
}
func (a *SuperAgent) validateWatchdogRefresh(tr transition) error {
	w := tr.Watchdog
	i := w.Intervention
	if a.watchdog.SessionID == "" || i == nil || w.SessionID != "" || w.ProviderSessionID != "" || w.Policy != (WatchdogPolicy{}) || w.Check != nil || len(w.Snapshot) > 0 || i.ObservedAt < 0 || i.ObservedAt > 1<<62 || i.RecheckAt != i.ObservedAt+a.watchdog.Policy.Interval.Milliseconds() || i.ID != i.Request.FindingID+"/refresh/"+i.Request.OperationID {
		return errors.New("watchdog: invalid intervention envelope")
	}
	prev, exists := a.watchdogIntervention(i.Request.FindingID, i.Request.OperationID)
	op, ok := a.backgroundOperation(i.Request.OperationID)
	if !ok || op.TaskID != i.Request.TaskID || op.Origin != i.Request.Origin || op.DeliveryID != i.Request.DeliveryID {
		return errors.New("watchdog: invalid intervention origin")
	}
	if tr.State == "watchdog_refresh_reserved" {
		if exists || !a.watchdogRefreshBinding(i.Request) || op.Exited || i.State != "pending" || i.Result != "" || w.Operation != nil || i.Operation != publicWatchdogOperation(op) || i.RecheckCondition != "inspect_outcome_unrecorded" {
			return errors.New("watchdog: invalid intervention reservation")
		}
		return nil
	}
	if !exists || prev.State != "pending" || prev.ID != i.ID || prev.Request != i.Request || i.State != "completed" || i.ObservedAt < prev.ObservedAt || i.RecheckCondition != "new_finding_and_host_authorization" {
		return errors.New("watchdog: invalid intervention result")
	}
	if i.Result == "stale" {
		if w.Operation != nil || i.Operation != publicWatchdogOperation(op) {
			return errors.New("watchdog: stale result changed operation")
		}
		return nil
	}
	if !a.watchdogRefreshBinding(i.Request) || op.Exited {
		return errors.New("watchdog: changed result binding")
	}
	if w.Operation != nil {
		if w.Operation.ID != op.ID {
			return errors.New("watchdog: changed result operation")
		}
		if err := a.validateBackground(transition{State: "background_observed", Background: &backgroundTransition{Operation: *w.Operation}}); err != nil {
			return err
		}
		op = *w.Operation
	}
	if i.Operation != publicWatchdogOperation(op) {
		return errors.New("watchdog: result evidence differs")
	}
	switch i.Result {
	case "unknown", "failed":
		if op.Status != "needs_review" || !op.NeedsReview || op.Exited {
			return errors.New("watchdog: uncertain result released barrier")
		}
	case "running", "exited":
		if op.Status != i.Result {
			return errors.New("watchdog: result state differs")
		}
	default:
		return errors.New("watchdog: invalid refresh outcome")
	}
	return nil
}
func (a *SuperAgent) applyWatchdogRefresh(tr transition) error {
	i := *tr.Watchdog.Intervention
	if tr.State == "watchdog_refresh_reserved" {
		a.watchdog.Interventions = append(a.watchdog.Interventions, i)
		return nil
	}
	if op := tr.Watchdog.Operation; op != nil {
		if err := a.applyBackground(transition{State: "background_observed", Background: &backgroundTransition{Operation: *op}}); err != nil {
			return err
		}
	}
	for n, old := range a.watchdog.Interventions {
		if old.ID == i.ID {
			a.watchdog.Interventions[n] = i
			return nil
		}
	}
	return errors.New("watchdog: intervention disappeared")
}
