package superagent

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/Icatme/pi-go/agent"
)

// ReportsSince returns terminal reports after the caller's count, in journal
// order. Pass zero to replay all reports. Record count+len(reports) only after
// the consumer durably handles that batch; the cursor is a report count, not
// an ingress count or delivery ID. A fanout has one report per target. Replays
// retain DeliveryID, so an idempotent sink can deduplicate within this journal.
func (a *SuperAgent) ReportsSince(count int) []Report {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneReports(a.reports[min(max(count, 0), len(a.reports)):])
}

// Admission slots are immutable, including the frozen order of fanout targets.
// Derive the public identity from that durable position, not completion order,
// task assignment or a new random value on replay. This also upgrades old logs
// without rewriting them or changing any task/provider identity. Future log
// compaction/import must preserve these slots (or persist their identities).
func deliveryID(index int) string { return "delivery-" + strconv.Itoa(index+1) }

func cloneReports(reports []Report) []Report {
	out := slices.Clone(reports)
	for i := range out {
		out[i] = *copyReport(&out[i])
	}
	return out
}

// FailureCause returns the original routing or execution error to a trusted
// host for diagnostics. Causes are process-local and can contain private data.
// With multiple deliveries it joins their task-labeled errors. Use
// DeliveryFailureCause for one target. Replaying restores reports, not errors.
func (a *SuperAgent) FailureCause(source, eventID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if index, ok := a.accepted[[2]string{source, eventID}]; ok {
		var causes []error
		for i := index; i < len(a.events) && a.events[i].admission == index; i++ {
			if err := a.events[i].cause; err != nil {
				if a.events[i].task == "" {
					causes = append(causes, err)
				} else {
					causes = append(causes, fmt.Errorf("%s: %w", a.events[i].task, err))
				}
			}
		}
		return errors.Join(causes...)
	}
	return nil
}

// TaskSnapshot retrieves the latest validated worker snapshot for a trusted
// host. It may contain private tool results. The caller owns the decoded copy.
// The boolean is false when no snapshot has been recorded for the task.
func (a *SuperAgent) TaskSnapshot(id string) (agent.AgentSnapshot, bool, error) {
	a.mu.Lock()
	t := a.tasks[id]
	if t == nil || len(t.snapshot) == 0 {
		a.mu.Unlock()
		return agent.AgentSnapshot{}, false, nil
	}
	raw := t.snapshot // Immutable after that turn is committed.
	a.mu.Unlock()
	var snapshot agent.AgentSnapshot
	if err := decodeSnapshot(raw, &snapshot); err != nil {
		return agent.AgentSnapshot{}, false, err
	}
	return snapshot, true, nil
}

// DeliveryFailureCause returns the private in-process error for one exact
// delivery. It never substitutes a different task sharing the external event.
func (a *SuperAgent) DeliveryFailureCause(target RunTarget) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if index, ok := a.deliveryIndex(target); ok {
		return a.events[index].cause
	}
	return nil
}
