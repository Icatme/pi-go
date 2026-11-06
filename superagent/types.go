// Package superagent coordinates independent Pi worker sessions behind one local event inbox.
package superagent

import (
	"context"

	"github.com/Icatme/pi-go/agent"
)

// Trigger is one event delivered by a trusted local adapter. ID is unique within Source.
// Source and Thread together identify a conversation. Text is untrusted content.
type Trigger struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Thread string `json:"thread"`
	Kind   string `json:"kind"` // request, signal, or noise
	Text   string `json:"text"`
}

// Router decides what to do with an event without a thread subscription.
type Router func(context.Context, Trigger, []TaskInfo) (Route, error)

type TaskInfo struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

type Route struct {
	Action string `json:"action"`            // new, attach, or ignore
	TaskID string `json:"task_id,omitempty"` // required only for attach
}

// Worker receives only the selected task's snapshot.
type Worker func(context.Context, Job) (Outcome, error)

// RunTarget identifies the specific source event being executed by a task.
// A control from an old run cannot apply to the task's next event.
type RunTarget struct {
	TaskID  string `json:"task_id"`
	Source  string `json:"source"`
	EventID string `json:"event_id"`
}

type Job struct {
	TaskID string
	// DeliveryID identifies this admission's target within the journal. It is
	// stable across replay, distinct from the task and external event, and not
	// an authorization token. Standalone Worker calls may leave it empty.
	DeliveryID string `json:"delivery_id"`
	// ProviderSessionID is the stable wire identity, recorded when a task is
	// created. Standalone workers default to TaskID when this field is empty.
	ProviderSessionID string
	Trigger           Trigger
	Snapshot          agent.AgentSnapshot
	// TakeSteering polls this run's in-memory control queue at a safe boundary.
	// PiWorker connects it to Pi's GetSteeringMessages loop hook.
	TakeSteering func(context.Context, agent.QueueMode) ([]agent.Message, error) `json:"-"`
	// NotifyProgress accepts turn_start, turn_end, tool_start and tool_end.
	NotifyProgress func(kind string) `json:"-"`
	// SetThreadSubscription changes only this task/current Thread while this
	// invocation is active. Present only with OpenMain. A host must explicitly
	// expose it if a model is allowed to request subscription changes.
	SetThreadSubscription func(context.Context, bool) error `json:"-"`
	// HandBack records the original event, reason and handled scope for main
	// clarification. It does not cancel execution or erase effects/receipts.
	HandBack func(ctx context.Context, reason, handled string) error `json:"-"`
}

type Outcome struct {
	Summary  string
	Snapshot agent.AgentSnapshot
	// Status describes the model run: completed (the default), truncated, or
	// completed_with_tool_errors. Receipts describe the tool bodies separately.
	Status      string
	Receipts    []ToolReceipt
	NeedsReview bool
}

// ToolReceipt describes a single executor body within one trigger. A commit
// is a host-reported local effect. Its absence leaves the effect unknown.
// Completed means the body exited, including when it returned an error.
type ToolReceipt struct {
	Token       uint64 `json:"token"`
	ParentToken uint64 `json:"parent_token,omitempty"`
	Domain      string `json:"domain"`
	CallID      string `json:"call_id"`
	ToolName    string `json:"tool_name"`
	Committed   bool   `json:"committed"`
	Completed   bool   `json:"completed"`
}

// View projects task state and public reports, excluding workers' transcripts.
type View struct {
	Tasks   []TaskStatus `json:"tasks"`
	Reports []Report     `json:"reports"`
}

type TaskStatus struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Pending int    `json:"pending"`
	// ActiveSource and ActiveEventID identify the controllable invocation.
	// A finalizing worker has returned and is publishing its outcome.
	ActiveSource     string `json:"active_source,omitempty"`
	ActiveEventID    string `json:"active_event_id,omitempty"`
	ActiveDeliveryID string `json:"active_delivery_id,omitempty"`
	ReviewSource     string `json:"review_source,omitempty"`
	ReviewEventID    string `json:"review_event_id,omitempty"`
	ReviewDeliveryID string `json:"review_delivery_id,omitempty"`
}

type Report struct {
	// DeliveryID is the opaque, stable identity of one terminal outcome in
	// this journal, including unassigned/ignored events. Deduplicate reports
	// by (host journal identity, DeliveryID), never Source/EventID alone.
	DeliveryID  string        `json:"delivery_id"`
	Source      string        `json:"source"`
	Thread      string        `json:"thread"`
	EventID     string        `json:"event_id"`
	TaskID      string        `json:"task_id,omitempty"`
	Status      string        `json:"status"`
	Text        string        `json:"text"`
	Receipts    []ToolReceipt `json:"receipts,omitempty"`
	NeedsReview bool          `json:"needs_review,omitempty"`
}
