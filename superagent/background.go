package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
)

const backgroundSource = "superagent.background"

// BackgroundStart contains model-selectable input, never a scope or backend.
// CallID correlates the native launch call; it is not a deduplication key.
type BackgroundStart struct {
	Tool      string          `json:"tool"`
	CallID    string          `json:"call_id"`
	Arguments json.RawMessage `json:"arguments"`
}

// BackgroundAuthorization supplies coordinator-owned provenance to the host.
// Its Target and DeliveryID cannot be selected by tool arguments.
type BackgroundAuthorization struct {
	Start                 BackgroundStart
	Target                RunTarget
	DeliveryID            string
	Scope, PolicyRevision string
}

// BackgroundTool is an explicit host grant. Authorize must check current
// authority; the backend must independently enforce its native permission and
// isolation policy. Scope and PolicyRevision are stable non-secret identifiers.
type BackgroundTool struct {
	Name, Scope, PolicyRevision string
	Backend                     background.Backend
	Authorize                   func(context.Context, BackgroundAuthorization) error
}

type BackgroundOptions struct {
	MaxConcurrent int
	Tools         []BackgroundTool
	// PollInterval controls read-only backend observations (default one second).
	PollInterval time.Duration
}

// BackgroundOperation is a private host record. Arguments and Output may be
// sensitive and must not be published to the main router or external channels.
// Started means dispatch was acknowledged, not that effects committed. Exited
// is backend evidence of actual exit. StopRequested does not imply either.
type BackgroundOperation struct {
	ID                string               `json:"id"`
	TaskID            string               `json:"task_id"`
	DeliveryID        string               `json:"delivery_id"`
	Origin            RunTarget            `json:"origin"`
	CallID            string               `json:"call_id"`
	Tool              string               `json:"tool"`
	Scope             string               `json:"scope"`
	PolicyRevision    string               `json:"policy_revision"`
	Arguments         json.RawMessage      `json:"arguments"`
	QuotaLimit        int                  `json:"quota_limit"`
	Status            string               `json:"status"` // reserved, running, needs_review, exited
	Started           bool                 `json:"started"`
	StopRequested     bool                 `json:"stop_requested"`
	Exited            bool                 `json:"exited"`
	Failed            bool                 `json:"failed"`
	NeedsReview       bool                 `json:"needs_review"`
	Reconciled        bool                 `json:"reconciled"`
	Output            json.RawMessage      `json:"output,omitempty"`
	Receipts          []background.Receipt `json:"receipts,omitempty"`
	CompletionEventID string               `json:"completion_event_id,omitempty"`
}

// BackgroundCompletion is delivered privately to the owning Worker as new
// input, never as a second tool response for the original native call.
type BackgroundCompletion struct {
	OperationID   string               `json:"operation_id"`
	DeliveryID    string               `json:"delivery_id"`
	CallID        string               `json:"call_id"`
	Tool          string               `json:"tool"`
	StopRequested bool                 `json:"stop_requested"`
	Failed        bool                 `json:"failed"`
	NeedsReview   bool                 `json:"needs_review"`
	Output        json.RawMessage      `json:"output,omitempty"`
	Receipts      []background.Receipt `json:"receipts,omitempty"`
}

type backgroundTransition struct {
	Operation BackgroundOperation `json:"operation"`
	Note      string              `json:"note,omitempty"`
}
type backgroundState struct {
	options    *BackgroundOptions
	tools      map[string]BackgroundTool
	operations []BackgroundOperation
	indexes    map[string]int
}

// BackgroundControl is restricted to one currently executing Worker/task.
// Retaining it does not grant permission after that Worker invocation returns.
type BackgroundControl struct {
	a      *SuperAgent
	index  int
	active *activeRun
}

func OpenMainBackground(ctx context.Context, path string, parallel int, route MainRouter, work Worker, options BackgroundOptions) (*SuperAgent, error) {
	return openMainConfigured(ctx, path, parallel, route, nil, MainBatchOptions{}, work, &options)
}

func validateBackgroundOptions(options *BackgroundOptions) error {
	if options == nil {
		return nil
	}
	if options.MaxConcurrent < 1 || options.MaxConcurrent > 256 || len(options.Tools) < 1 || len(options.Tools) > 64 {
		return errors.New("background: require 1–256 slots and 1–64 explicit tools")
	}
	if options.PollInterval == 0 {
		options.PollInterval = time.Second
	}
	if options.PollInterval < time.Millisecond || options.PollInterval > time.Minute {
		return errors.New("background: invalid observation interval")
	}
	seen := map[string]bool{}
	for _, t := range options.Tools {
		if !validIdentity(t.Name) || !validIdentity(t.Scope) || !validIdentity(t.PolicyRevision) || t.Backend == nil || t.Authorize == nil || seen[t.Name] {
			return errors.New("background: each tool requires unique name, scope, policy, backend and authorizer")
		}
		seen[t.Name] = true
	}
	return nil
}

func (a *SuperAgent) configureBackground(options *BackgroundOptions) error {
	if options == nil {
		if len(a.background.operations) > 0 {
			return errors.New("background: journal requires explicit background configuration")
		}
		return nil
	}
	c := *options
	c.Tools = slices.Clone(options.Tools)
	a.background.options = &c
	a.background.tools = map[string]BackgroundTool{}
	for _, t := range c.Tools {
		a.background.tools[t.Name] = t
	}
	// Any unobserved restart is uncertain until this exact backend confirms it.
	// Never replay Start, even for a durable reservation without acknowledgment.
	for _, o := range slices.Clone(a.background.operations) {
		if !o.Exited {
			o.Status = "needs_review"
			o.NeedsReview = true
			if err := a.commit(transition{State: "background_unknown", Background: &backgroundTransition{Operation: o}}); err != nil {
				return err
			}
		}
	}
	return nil
}

func cloneBackground(o BackgroundOperation) BackgroundOperation {
	o.Arguments = bytes.Clone(o.Arguments)
	o.Output = bytes.Clone(o.Output)
	o.Receipts = slices.Clone(o.Receipts)
	return o
}
func completionOf(o BackgroundOperation, private bool) *BackgroundCompletion {
	c := &BackgroundCompletion{OperationID: o.ID, DeliveryID: o.DeliveryID, CallID: o.CallID, Tool: o.Tool, StopRequested: o.StopRequested, Failed: o.Failed, NeedsReview: o.NeedsReview, Receipts: slices.Clone(o.Receipts)}
	if private {
		c.Output = bytes.Clone(o.Output)
	}
	return c
}
func (a *SuperAgent) backgroundOperation(id string) (BackgroundOperation, bool) {
	i, ok := a.background.indexes[id]
	if !ok {
		return BackgroundOperation{}, false
	}
	return a.background.operations[i], true
}
func (a *SuperAgent) backgroundInUse() int {
	n := 0
	for _, o := range a.background.operations {
		if !o.Exited {
			n++
		}
	}
	return n
}
func (a *SuperAgent) backgroundBackend(o BackgroundOperation) (background.Backend, error) {
	t, ok := a.background.tools[o.Tool]
	if !ok || t.Scope != o.Scope || t.PolicyRevision != o.PolicyRevision {
		return nil, errors.New("background: original backend grant is unavailable")
	}
	return t.Backend, nil
}
func (a *SuperAgent) BackgroundOperations() []BackgroundOperation {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]BackgroundOperation, len(a.background.operations))
	for i, o := range a.background.operations {
		out[i] = cloneBackground(o)
	}
	return out
}
func validBackgroundStart(r BackgroundStart) bool {
	return validIdentity(r.Tool) && len(r.CallID) <= 4096 && utf8.ValidString(r.CallID) && len(r.Arguments) > 0 && len(r.Arguments) <= 64<<10 && json.Valid(r.Arguments) && bytes.HasPrefix(bytes.TrimSpace(r.Arguments), []byte("{"))
}
func authorizeBackground(ctx context.Context, t BackgroundTool, r BackgroundStart, target RunTarget, delivery string) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = callbackPanic("background authorization", p)
		}
	}()
	r.Arguments = bytes.Clone(r.Arguments)
	return t.Authorize(ctx, BackgroundAuthorization{Start: r, Target: target, DeliveryID: delivery, Scope: t.Scope, PolicyRevision: t.PolicyRevision})
}

func (c *BackgroundControl) Start(ctx context.Context, r BackgroundStart) (BackgroundOperation, error) {
	if c == nil || c.a == nil || ctx == nil || !validBackgroundStart(r) {
		return BackgroundOperation{}, errors.New("background: invalid launch")
	}
	r.Arguments = bytes.Clone(r.Arguments)
	a := c.a
	a.mu.Lock()
	err := a.workerRouteAllowed(ctx, c.index, c.active)
	t, ok := a.background.tools[r.Tool]
	a.mu.Unlock()
	if err != nil {
		return BackgroundOperation{}, err
	}
	if !ok {
		return BackgroundOperation{}, errors.New("background: tool is not registered")
	}
	if err = authorizeBackground(ctx, t, r, c.active.target, deliveryID(c.index)); err != nil {
		return BackgroundOperation{}, errors.New("background: launch denied")
	}
	a.mu.Lock()
	if err = a.workerRouteAllowed(ctx, c.index, c.active); err == nil && a.tasks[c.active.target.TaskID].blocks(c.index) {
		err = errors.New("background: task requires reconciliation")
	}
	if err == nil && a.backgroundInUse() >= a.background.options.MaxConcurrent {
		err = errors.New("background: concurrency quota exhausted")
	}
	if err != nil {
		a.mu.Unlock()
		return BackgroundOperation{}, err
	}
	o := BackgroundOperation{ID: fmt.Sprintf("%s/background-%d", a.main.id, len(a.background.operations)+1), TaskID: c.active.target.TaskID, DeliveryID: deliveryID(c.index), Origin: c.active.target, CallID: r.CallID, Tool: r.Tool, Scope: t.Scope, PolicyRevision: t.PolicyRevision, Arguments: bytes.Clone(r.Arguments), QuotaLimit: a.background.options.MaxConcurrent, Status: "reserved"}
	err = a.commit(transition{State: "background_reserved", Background: &backgroundTransition{Operation: o}})
	a.mu.Unlock()
	if err != nil {
		return BackgroundOperation{}, err
	}
	// The reservation, grant and quota are durable before crossing dispatch.
	// Its lifetime belongs to the explicit backend, not this model invocation.
	err = callBackgroundStart(ctx, t.Backend, background.Request{ID: o.ID, Scope: o.Scope, Call: agent.ToolCall{ID: o.CallID, Name: o.Tool, Arguments: bytes.Clone(o.Arguments)}})
	a.mu.Lock()
	current, _ := a.backgroundOperation(o.ID)
	if err != nil {
		current.Status = "needs_review"
		current.NeedsReview = true
	} else {
		current.Started = true
		if !current.Exited {
			current.Status = "running"
			current.NeedsReview = current.StopRequested || current.Failed
		}
	}
	if !current.Exited {
		state := "background_observed"
		if err != nil {
			state = "background_unknown"
		}
		if e := a.commit(transition{State: state, Background: &backgroundTransition{Operation: current}}); e != nil {
			err = errors.Join(err, e)
		}
	}
	if a.ctx.Err() == nil {
		a.wg.Add(1)
		go a.watchBackground(o.ID)
	}
	a.mu.Unlock()
	if err != nil {
		return cloneBackground(current), errors.New("background: dispatch outcome requires verification")
	}
	return cloneBackground(current), nil
}
func callBackgroundStart(ctx context.Context, b background.Backend, r background.Request) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = callbackPanic("background dispatch", p)
		}
	}()
	return b.Start(ctx, r)
}
func callBackgroundInspect(ctx context.Context, b background.Backend, id string) (o background.Observation, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = callbackPanic("background inspection", p)
		}
	}()
	return b.Inspect(ctx, id)
}
func callBackgroundStop(ctx context.Context, b background.Backend, id string) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = callbackPanic("background stop", p)
		}
	}()
	return b.Stop(ctx, id)
}

// Pi checks this at every native tool permission boundary. A newly observed
// uncertain operation fences subsequent tools even in an already active turn.
// Already-entered bodies still require their own pre-effect permission checks.
func (c *BackgroundControl) checkToolPermission(ctx context.Context) error {
	if c == nil || c.a == nil {
		return errors.New("background: control unavailable")
	}
	a := c.a
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.workerRouteAllowed(ctx, c.index, c.active); err != nil {
		return err
	}
	if a.tasks[c.active.target.TaskID].blocks(c.index) {
		return errors.New("background: task requires reconciliation before more tools")
	}
	return nil
}

func (c *BackgroundControl) Inspect(ctx context.Context, id string) (BackgroundOperation, error) {
	if c == nil || c.a == nil {
		return BackgroundOperation{}, errors.New("background: control unavailable")
	}
	a := c.a
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.workerRouteAllowed(ctx, c.index, c.active); err != nil {
		return BackgroundOperation{}, err
	}
	o, ok := a.backgroundOperation(id)
	if !ok || o.TaskID != c.active.target.TaskID {
		return BackgroundOperation{}, errors.New("background: operation unavailable")
	}
	return cloneBackground(o), nil
}
func (c *BackgroundControl) Stop(ctx context.Context, id string) (BackgroundOperation, error) {
	if _, err := c.Inspect(ctx, id); err != nil {
		return BackgroundOperation{}, err
	}
	if err := c.a.stopBackground(ctx, id, c); err != nil {
		return BackgroundOperation{}, err
	}
	return c.Inspect(ctx, id)
}

// StopBackground requests one operation's stop, independently of Abort on a
// Worker invocation. It records intent first; neither return nor cancellation
// releases quota. Calling it again safely retries the idempotent backend stop.
func (a *SuperAgent) StopBackground(ctx context.Context, id string) error {
	return a.stopBackground(ctx, id, nil)
}
func (a *SuperAgent) stopBackground(ctx context.Context, id string, control *BackgroundControl) error {
	if ctx == nil {
		return errors.New("background: context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	if control != nil {
		if err := a.workerRouteAllowed(ctx, control.index, control.active); err != nil {
			a.mu.Unlock()
			return err
		}
	}
	o, ok := a.backgroundOperation(id)
	if !ok {
		a.mu.Unlock()
		return errors.New("background: unknown operation")
	}
	if a.fatal != nil || a.ctx.Err() != nil {
		err := errors.Join(a.fatal, a.ctx.Err())
		a.mu.Unlock()
		return err
	}
	if o.Exited {
		a.mu.Unlock()
		return nil
	}
	b, err := a.backgroundBackend(o)
	if err == nil && !o.StopRequested {
		o.StopRequested = true
		o.NeedsReview = true
		err = a.commit(transition{State: "background_stopped", Background: &backgroundTransition{Operation: o}})
	}
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if err = callBackgroundStop(ctx, b, id); err != nil {
		return errors.New("background: stop outcome requires verification")
	}
	return nil
}

// RefreshBackground performs a read-only exact-ID observation. An unknown or
// failed observation retains quota and the review barrier; it never replays Start.
func (a *SuperAgent) RefreshBackground(ctx context.Context, id string) error {
	if ctx == nil {
		return errors.New("background: context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	if err := errors.Join(a.fatal, a.ctx.Err()); err != nil {
		a.mu.Unlock()
		return err
	}
	o, ok := a.backgroundOperation(id)
	if !ok {
		a.mu.Unlock()
		return errors.New("background: unknown operation")
	}
	if o.Exited {
		a.mu.Unlock()
		return nil
	}
	b, err := a.backgroundBackend(o)
	a.mu.Unlock()
	var observation background.Observation
	if err == nil {
		observation, err = callBackgroundInspect(ctx, b, id)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	o, _ = a.backgroundOperation(id)
	if o.Exited {
		return nil
	}
	next := cloneBackground(o)
	if err != nil || !validBackgroundObservation(observation, id) || !backgroundFactsAdvance(o, observation) {
		next.Status = "needs_review"
		next.NeedsReview = true
	} else {
		switch observation.State {
		case "unknown":
			next.Status = "needs_review"
			next.NeedsReview = true
			if receipts, ok := mergeUnknownBackgroundReceipts(o.Receipts, observation.Receipts); ok {
				next.Receipts = receipts
				next.Failed = next.Failed || observation.Failed
			}
		case "running":
			next.Started = true
			next.Status = "running"
			next.Failed = observation.Failed
			next.Receipts = slices.Clone(observation.Receipts)
			next.NeedsReview = next.StopRequested || next.Failed
		case "exited":
			next.Started = true
			next.Exited = true
			next.Status = "exited"
			next.Failed = observation.Failed
			next.Output = bytes.Clone(observation.Output)
			next.Receipts = slices.Clone(observation.Receipts)
			next.CompletionEventID = next.ID
			next.NeedsReview = next.StopRequested || next.Failed
		}
	}
	if reflect.DeepEqual(next, o) {
		return nil
	}
	return a.commit(transition{State: "background_observed", Background: &backgroundTransition{Operation: next}})
}
func validBackgroundObservation(o background.Observation, id string) bool {
	if o.ID != id || (o.State != "running" && o.State != "exited" && o.State != "unknown") || len(o.Output) > 64<<10 || (len(o.Output) > 0 && !json.Valid(o.Output)) || len(o.Receipts) > 256 {
		return false
	}
	if o.State != "exited" && len(o.Output) > 0 {
		return false
	}
	seen := map[uint64]bool{}
	for _, r := range o.Receipts {
		if r.Token == 0 || seen[r.Token] || (o.State == "exited" && !r.Completed) || len(r.CallID) > 4096 || len(r.ToolName) > 4096 || !utf8.ValidString(r.CallID) || !utf8.ValidString(r.ToolName) || (r.Domain != "agent" && r.Domain != "codemode") {
			return false
		}
		if r.ParentToken != 0 && !seen[r.ParentToken] {
			return false
		}
		seen[r.Token] = true
	}
	return true
}

// Unknown is not absence: retain every valid observed fact, including partial
// evidence not previously seen before a backend lost its final status.
func mergeUnknownBackgroundReceipts(old, incoming []background.Receipt) ([]background.Receipt, bool) {
	merged := slices.Clone(old)
	indexes := map[uint64]int{}
	for i, r := range merged {
		indexes[r.Token] = i
	}
	for _, r := range incoming {
		if i, ok := indexes[r.Token]; ok {
			p := merged[i]
			if p.ParentToken != r.ParentToken || p.Domain != r.Domain || p.CallID != r.CallID || p.ToolName != r.ToolName {
				return nil, false
			}
			merged[i].Committed = p.Committed || r.Committed
			merged[i].Completed = p.Completed || r.Completed
		} else {
			indexes[r.Token] = len(merged)
			merged = append(merged, r)
		}
	}
	if len(merged) > 256 {
		return nil, false
	}
	return merged, true
}

func backgroundFactsAdvance(prev BackgroundOperation, next background.Observation) bool {
	if next.State == "unknown" {
		return true
	}
	if prev.Failed && !next.Failed {
		return false
	}
	seen := map[uint64]background.Receipt{}
	for _, r := range next.Receipts {
		seen[r.Token] = r
	}
	for _, r := range prev.Receipts {
		n, ok := seen[r.Token]
		if !ok || r.ParentToken != n.ParentToken || r.Domain != n.Domain || r.CallID != n.CallID || r.ToolName != n.ToolName || r.Committed && !n.Committed || r.Completed && !n.Completed {
			return false
		}
	}
	return true
}
func (a *SuperAgent) watchBackground(id string) {
	defer a.wg.Done()
	stopSent := false
	for {
		if a.ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		before, ok := a.backgroundOperation(id)
		a.mu.Unlock()
		if ok && before.StopRequested && !before.Exited && !stopSent {
			stopSent = a.StopBackground(a.ctx, id) == nil
		}
		_ = a.RefreshBackground(a.ctx, id)
		a.mu.Lock()
		o, ok := a.backgroundOperation(id)
		interval := a.background.options.PollInterval
		a.mu.Unlock()
		if !ok || o.Exited {
			return
		}
		timer := time.NewTimer(interval)
		select {
		case <-a.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (a *SuperAgent) startBackgroundWatchers() {
	var ids []string
	for _, o := range a.background.operations {
		if !o.Exited {
			ids = append(ids, o.ID)
		}
	}
	for _, id := range ids {
		a.wg.Add(1)
		go a.watchBackground(id)
	}
}

// ReconcileBackground records host review only after a backend has proved real
// exit. Unknown/lost operations cannot be released by a note or blindly retried.
// Previously admitted inputs remain withheld; submit fresh instructions after it.
func (a *SuperAgent) ReconcileBackground(id, note string) error {
	if !validNote(note) {
		return errors.New("background: a bounded review note is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := errors.Join(a.fatal, a.ctx.Err()); err != nil {
		return err
	}
	o, ok := a.backgroundOperation(id)
	if !ok || !o.Exited || !o.NeedsReview || a.tasks[o.TaskID].running {
		return errors.New("background: require exited operation, pending review and idle task")
	}
	o.NeedsReview = false
	o.Reconciled = true
	return a.commit(transition{State: "background_reconciled", Background: &backgroundTransition{Operation: o, Note: note}})
}

func (a *SuperAgent) validateBackground(tr transition) error {
	m := tr.Background
	if !a.main.chatRouting || m == nil || tr.Main != nil || tr.Thread != nil || tr.Admission != nil || tr.Index != 0 || tr.Trigger != nil || tr.Task != "" || tr.ProviderSessionID != "" || tr.Text != "" || len(tr.Snapshot) > 0 || len(tr.Receipts) > 0 || tr.NeedsReview {
		return errors.New("background: invalid transition envelope")
	}
	o := m.Operation
	i, ok := a.deliveryIndex(o.Origin)
	if !ok || o.TaskID != o.Origin.TaskID || o.DeliveryID != deliveryID(i) || !validBackgroundStart(BackgroundStart{o.Tool, o.CallID, o.Arguments}) || !validIdentity(o.Scope) || !validIdentity(o.PolicyRevision) || o.QuotaLimit < 1 || o.QuotaLimit > 256 {
		return errors.New("background: invalid durable origin or grant")
	}
	prev, exists := a.backgroundOperation(o.ID)
	if tr.State == "background_reserved" {
		if exists || o.ID != fmt.Sprintf("%s/background-%d", a.main.id, len(a.background.operations)+1) || a.main.id == "" || a.events[i].state != "running" || a.tasks[o.TaskID].blocks(i) || o.Status != "reserved" || o.Started || o.StopRequested || o.Exited || o.Failed || o.NeedsReview || o.Reconciled || len(o.Output) > 0 || len(o.Receipts) > 0 || o.CompletionEventID != "" || m.Note != "" || a.backgroundInUse() >= o.QuotaLimit {
			return errors.New("background: invalid reservation")
		}
		return nil
	}
	if !exists {
		return errors.New("background: unknown transition identity")
	}
	immutable := func(x BackgroundOperation) BackgroundOperation {
		x.Status = ""
		x.Started = false
		x.StopRequested = false
		x.Exited = false
		x.Failed = false
		x.NeedsReview = false
		x.Reconciled = false
		x.Output = nil
		x.Receipts = nil
		x.CompletionEventID = ""
		return x
	}
	if !reflect.DeepEqual(immutable(prev), immutable(o)) {
		return errors.New("background: immutable identity/grant changed")
	}
	if tr.State == "background_reconciled" {
		p := cloneBackground(prev)
		p.NeedsReview = false
		p.Reconciled = true
		if !prev.Exited || !prev.NeedsReview || a.tasks[o.TaskID].running || !validNote(m.Note) || !reflect.DeepEqual(p, o) {
			return errors.New("background: invalid reconciliation")
		}
		return nil
	}
	if m.Note != "" || prev.Exited || o.Reconciled || prev.Started && !o.Started || prev.StopRequested && !o.StopRequested {
		return errors.New("background: non-monotonic transition")
	}
	switch tr.State {
	case "background_stopped":
		p := cloneBackground(prev)
		p.StopRequested = true
		p.NeedsReview = true
		if prev.StopRequested || !reflect.DeepEqual(p, o) {
			return errors.New("background: invalid stop")
		}
	case "background_unknown":
		p := cloneBackground(prev)
		p.Status = "needs_review"
		p.NeedsReview = true
		if !reflect.DeepEqual(p, o) {
			return errors.New("background: invalid uncertainty")
		}
	case "background_observed":
		if !backgroundFactsAdvance(prev, background.Observation{State: o.Status, Failed: o.Failed, Receipts: o.Receipts}) {
			return errors.New("background: execution facts regressed")
		}
		if o.Exited {
			if !o.Started || o.Status != "exited" || o.CompletionEventID != o.ID || o.NeedsReview != (o.StopRequested || o.Failed) || !validBackgroundObservation(background.Observation{ID: o.ID, State: "exited", Output: o.Output, Failed: o.Failed, Receipts: o.Receipts}, o.ID) {
				return errors.New("background: invalid exit")
			}
			if err := validTrigger(a.backgroundTrigger(o)); err != nil {
				return err
			}
			if _, exists := a.accepted[[2]string{backgroundSource, o.ID}]; exists {
				return errors.New("background: duplicate completion")
			}
		}
		if !o.Exited {
			if o.Status != "running" && o.Status != "needs_review" {
				return errors.New("background: invalid observation")
			}
			if len(o.Output) > 0 || o.CompletionEventID != "" || o.Status == "needs_review" && !o.NeedsReview || o.Status == "running" && (!o.Started || o.NeedsReview != (o.StopRequested || o.Failed)) || !validBackgroundObservation(background.Observation{ID: o.ID, State: "running", Failed: o.Failed, Receipts: o.Receipts}, o.ID) {
				return errors.New("background: invalid running facts")
			}
		}
	default:
		return errors.New("background: unsupported transition")
	}
	return nil
}
func (a *SuperAgent) applyBackground(tr transition) error {
	if err := a.validateBackground(tr); err != nil {
		return err
	}
	o := cloneBackground(tr.Background.Operation)
	prev, exists := a.backgroundOperation(o.ID)
	if a.background.indexes == nil {
		a.background.indexes = map[string]int{}
	}
	if !exists {
		a.background.indexes[o.ID] = len(a.background.operations)
		a.background.operations = append(a.background.operations, o)
	} else {
		a.background.operations[a.background.indexes[o.ID]] = o
	}
	t := a.tasks[o.TaskID]
	if o.NeedsReview && !prev.NeedsReview {
		t.backgroundReview++
	}
	if !o.NeedsReview && prev.NeedsReview {
		t.backgroundReview--
	}
	if tr.State == "background_reconciled" {
		t.backgroundAdmitFrom = len(a.events)
	}
	if o.Exited && !prev.Exited {
		// Atomic with terminal facts and slot release. No raw tool output is placed
		// in a public Trigger, main input, report or history reference.
		event := a.backgroundTrigger(o)
		return a.apply(transition{State: "received", Index: len(a.events), Trigger: &event, Task: o.TaskID}, false)
	}
	return nil
}

func (a *SuperAgent) backgroundTrigger(o BackgroundOperation) Trigger {
	committed := false
	for _, r := range o.Receipts {
		committed = committed || r.Committed
	}
	raw, _ := json.Marshal(struct {
		OperationID   string `json:"operation_id"`
		CallID        string `json:"call_id"`
		Tool          string `json:"tool"`
		Exited        bool   `json:"exited"`
		Failed        bool   `json:"failed"`
		StopRequested bool   `json:"stop_requested"`
		Committed     bool   `json:"committed"`
	}{o.ID, o.CallID, o.Tool, true, o.Failed, o.StopRequested, committed})
	origin, _ := a.deliveryIndex(o.Origin)
	event := Trigger{ID: o.ID, Source: backgroundSource, Thread: a.events[origin].trigger.Thread, Kind: "signal", Text: string(raw)}
	return event
}
