package superagent

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
)

// AdmissionRule matches one exact authenticated source/principal/group tuple.
// An empty Group identifies a direct conversation. There are no wildcards.
type AdmissionRule struct {
	Source    string `json:"source"`
	Principal string `json:"principal"`
	Group     string `json:"group"`
}

// AdmissionPolicy belongs to the trusted host adapter, never to incoming text.
// Version should change whenever the host changes its rules. Empty rules deny
// every input. Only the decision and version, not credentials, are persisted.
type AdmissionPolicy struct {
	Version string
	Rules   []AdmissionRule
}

// AdmissionEnvelope contains host-authenticated facts. ExplicitRequest means
// the host has established a user request; never infer it from model output.
// TaskID and ToThread are mutually exclusive trusted routing associations.
type AdmissionEnvelope struct {
	Trigger Trigger `json:"trigger"`
	// MessageID is the authenticated platform object identity, distinct from a
	// webhook delivery ID. Empty legacy records use Trigger.ID. Platform object
	// kinds must be namespaced when their numeric ID spaces may overlap.
	MessageID       string `json:"message_id,omitempty"`
	Principal       string `json:"principal"`
	Group           string `json:"group,omitempty"`
	ExplicitRequest bool   `json:"explicit_request,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	ToThread        bool   `json:"to_thread,omitempty"`
}

type AdmissionRecord struct {
	Envelope      AdmissionEnvelope `json:"envelope"`
	PolicyVersion string            `json:"policy_version"`
	Decision      string            `json:"decision"` // accepted or ignored
	Reason        string            `json:"reason"`
	Targets       []string          `json:"targets,omitempty"` // frozen Thread delivery recipients
}

type AdmissionReceipt struct {
	Duplicate   bool
	DeliveryIDs []string
	Record      AdmissionRecord
}

func validAdmissionEnvelope(e AdmissionEnvelope) error {
	if e.Trigger.Source == backgroundSource {
		return errors.New("reserved background source")
	}
	if err := validTrigger(e.Trigger); err != nil {
		return err
	}
	if (e.MessageID != "" && !validIdentity(e.MessageID)) || !validIdentity(e.Principal) || (e.Group != "" && !validIdentity(e.Group)) || (e.TaskID != "" && !validIdentity(e.TaskID)) || (e.TaskID != "" && e.ToThread) {
		return errors.New("invalid authenticated admission identity or routing association")
	}
	if e.ExplicitRequest && e.Trigger.Kind != "request" {
		return errors.New("explicit request must have request kind")
	}
	return nil
}

// Admit atomically filters, audits, deduplicates and freezes delivery targets.
// Duplicate inputs return the original result, even after a policy change.
// Privileged Submit APIs do not use this policy: adapters must consistently
// choose Admit for untrusted ingress and authenticate envelope fields first.
func (a *SuperAgent) Admit(policy AdmissionPolicy, e AdmissionEnvelope) (AdmissionReceipt, error) {
	if !validIdentity(policy.Version) || len(policy.Rules) > 1024 {
		return AdmissionReceipt{}, errors.New("admission policy requires a version and at most 1024 rules")
	}
	if err := validAdmissionEnvelope(e); err != nil {
		return AdmissionReceipt{}, err
	}
	allowed := false
	for _, r := range policy.Rules {
		if !validIdentity(r.Source) || !validIdentity(r.Principal) || (r.Group != "" && !validIdentity(r.Group)) {
			return AdmissionReceipt{}, errors.New("invalid admission policy tuple")
		}
		if r.Source == e.Trigger.Source && r.Principal == e.Principal && r.Group == e.Group {
			allowed = true
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fatal != nil {
		return AdmissionReceipt{}, a.fatal
	}
	if err := a.ctx.Err(); err != nil {
		return AdmissionReceipt{}, err
	}
	if !a.main.chatRouting {
		return AdmissionReceipt{}, errors.New("admission gate requires OpenMain or OpenMainBatch")
	}
	if i, ok := a.accepted[[2]string{e.Trigger.Source, e.Trigger.ID}]; ok {
		record := a.events[i].admissionRecord
		if record == nil || !sameAdmissionEnvelope(record.Envelope, e) {
			return AdmissionReceipt{}, errors.New("event ID already exists with different admission contents")
		}
		return a.admissionReceipt(i, true), nil
	}
	// Echo identity remains authoritative and never creates a new admission.
	echo := e.Trigger
	echo.ID = admissionMessageID(e)
	if a.isReplyEcho(echo) {
		return AdmissionReceipt{Duplicate: true}, nil
	}
	record := AdmissionRecord{Envelope: e, PolicyVersion: policy.Version, Decision: "ignored", Reason: "policy_denied"}
	if allowed {
		switch {
		case e.Trigger.Kind == "noise":
			record.Reason = "noise"
		case e.TaskID != "":
			if a.tasks[e.TaskID] == nil {
				return AdmissionReceipt{}, errors.New("admission references an unknown task")
			}
			record.Decision, record.Reason = "accepted", "task_association"
		case e.ToThread && len(a.threadSubscriptions[[2]string{e.Trigger.Source, e.Trigger.Thread}]) > 0:
			record.Decision, record.Reason = "accepted", "thread_subscription"
			record.Targets = slices.Clone(a.threadSubscriptions[[2]string{e.Trigger.Source, e.Trigger.Thread}])
		case e.ExplicitRequest:
			record.Decision, record.Reason = "accepted", "explicit_request"
		default:
			record.Reason = "unassociated_signal"
		}
	}
	index := len(a.events)
	state := "admission_received"
	if e.MessageID != "" {
		state = "channel_admission_received"
	}
	if err := a.commit(transition{State: state, Index: index, Admission: &record}); err != nil {
		return AdmissionReceipt{}, err
	}
	return a.admissionReceipt(index, false), nil
}

// Omitted legacy identity is equivalent to explicitly naming Trigger.ID.
// Never infer a different webhook/platform association for an old record.
func sameAdmissionEnvelope(a, b AdmissionEnvelope) bool {
	a.MessageID, b.MessageID = admissionMessageID(a), admissionMessageID(b)
	return a == b
}

func admissionMessageID(e AdmissionEnvelope) string {
	if e.MessageID != "" {
		return e.MessageID
	}
	return e.Trigger.ID
}

func (a *SuperAgent) admissionReceipt(index int, duplicate bool) AdmissionReceipt {
	record := *a.events[index].admissionRecord
	record.Targets = slices.Clone(record.Targets)
	receipt := AdmissionReceipt{Duplicate: duplicate, Record: record}
	for i := index; i < len(a.events) && a.events[i].admission == index; i++ {
		receipt.DeliveryIDs = append(receipt.DeliveryIDs, deliveryID(i))
	}
	return receipt
}

// Admissions returns one detached audit record per filtered external ingress
// to the trusted host, not one per fanout delivery. Original untrusted text
// stays in the journal; rejected records must not be forwarded to models.
func (a *SuperAgent) Admissions() []AdmissionRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []AdmissionRecord
	for i, e := range a.events {
		if e.admission == i && e.admissionRecord != nil {
			r := *e.admissionRecord
			r.Targets = slices.Clone(r.Targets)
			out = append(out, r)
		}
	}
	return out
}

func (a *SuperAgent) validateAdmission(tr transition) error {
	r := tr.Admission
	if (tr.State != "admission_received" && tr.State != "channel_admission_received") || r == nil || !a.main.chatRouting || tr.Index != len(a.events) || tr.Main != nil || tr.Thread != nil {
		return errors.New("invalid admission transition")
	}
	if err := validAdmissionEnvelope(r.Envelope); err != nil {
		return err
	}
	if (tr.State == "channel_admission_received") != (r.Envelope.MessageID != "") {
		return errors.New("admission platform identity requires channel journal transition")
	}
	if !validIdentity(r.PolicyVersion) {
		return errors.New("invalid admission policy version")
	}
	e := r.Envelope
	if _, ok := a.accepted[[2]string{e.Trigger.Source, e.Trigger.ID}]; ok {
		return errors.New("duplicate filtered admission")
	}
	if r.Decision == "ignored" {
		if len(r.Targets) != 0 {
			return errors.New("ignored admission has targets")
		}
		switch r.Reason {
		case "policy_denied":
			return nil
		case "noise":
			if e.Trigger.Kind == "noise" {
				return nil
			}
		case "unassociated_signal":
			if !e.ExplicitRequest && e.TaskID == "" && e.Trigger.Kind != "noise" && (!e.ToThread || len(a.threadSubscriptions[[2]string{e.Trigger.Source, e.Trigger.Thread}]) == 0) {
				return nil
			}
		}
		return errors.New("invalid ignored admission reason")
	}
	if r.Decision != "accepted" || e.Trigger.Kind == "noise" {
		return errors.New("invalid accepted admission")
	}
	switch r.Reason {
	case "task_association":
		if e.TaskID == "" || a.tasks[e.TaskID] == nil || len(r.Targets) != 0 {
			return errors.New("invalid admission task association")
		}
	case "thread_subscription":
		if !e.ToThread || len(r.Targets) == 0 || !reflect.DeepEqual(r.Targets, a.threadSubscriptions[[2]string{e.Trigger.Source, e.Trigger.Thread}]) {
			return errors.New("invalid frozen admission subscribers")
		}
	case "explicit_request":
		if !e.ExplicitRequest || e.TaskID != "" || len(r.Targets) != 0 || (e.ToThread && len(a.threadSubscriptions[[2]string{e.Trigger.Source, e.Trigger.Thread}]) != 0) {
			return errors.New("invalid explicit admission request")
		}
	default:
		return errors.New("invalid accepted admission reason")
	}
	return nil
}

func (a *SuperAgent) applyAdmission(tr transition, replay bool) error {
	if err := a.validateAdmission(tr); err != nil {
		return err
	}
	r := *tr.Admission
	r.Targets = slices.Clone(r.Targets)
	e := r.Envelope
	if r.Decision == "ignored" {
		a.accepted[[2]string{e.Trigger.Source, e.Trigger.ID}] = tr.Index
		a.events = append(a.events, item{trigger: e.Trigger, state: "ignored", admission: tr.Index, entryTask: e.TaskID, threadEntry: e.ToThread, chatEntry: true, admissionRecord: &r})
		a.reports = append(a.reports, Report{DeliveryID: deliveryID(tr.Index), Source: e.Trigger.Source, Thread: e.Trigger.Thread, EventID: e.Trigger.ID, Status: "ignored", Text: fmt.Sprintf("Admission ignored: %s.", r.Reason)})
		return nil
	}
	nested := transition{State: "received", Index: tr.Index, Trigger: &e.Trigger, Task: e.TaskID}
	if e.ToThread {
		nested.State = "thread_received"
		nested.Thread = &threadTransition{Targets: r.Targets}
	}
	if err := a.apply(nested, replay); err != nil {
		return err
	}
	for i := tr.Index; i < len(a.events); i++ {
		a.events[i].admissionRecord = &r
	}
	return nil
}
