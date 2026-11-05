package superagent

import (
	"slices"

	"github.com/Icatme/pi-go/agent"
)

// ReportsSince returns terminal reports after the caller's count, in journal
// order. Pass zero to replay all reports. Record the new count after delivery.
func (a *SuperAgent) ReportsSince(count int) []Report {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneReports(a.reports[min(max(count, 0), len(a.reports)):])
}

func cloneReports(reports []Report) []Report {
	out := slices.Clone(reports)
	for i := range out {
		out[i].Receipts = slices.Clone(out[i].Receipts)
	}
	return out
}

// FailureCause returns the original routing or execution error to a trusted
// host for diagnostics. Causes are process-local and can contain private data.
// Replaying a journal restores public reports and receipts, not these errors.
func (a *SuperAgent) FailureCause(source, eventID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if index, ok := a.accepted[[2]string{source, eventID}]; ok {
		return a.events[index].cause
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
