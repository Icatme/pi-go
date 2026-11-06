package superagent

import (
	"errors"
	"slices"
	"strconv"
	"strings"
)

const maxReplyReportRefs = 16

// ReportCitation binds an exact domain-scoped public report reference to the
// coordinator's original terminal facts, never facts supplied by the model.
// Review/receipt fields describe that historical outcome, not a later task state.
type ReportCitation struct {
	Ref    HistoryRef `json:"ref"`
	Report Report     `json:"report"`
}

func validateReportRefList(refs []HistoryRef) error {
	if refs == nil {
		return nil
	}
	if len(refs) == 0 || len(refs) > maxReplyReportRefs {
		return errors.New("invalid report reference count")
	}
	seen := make(map[HistoryRef]bool, len(refs))
	for _, ref := range refs {
		if !validIdentity(ref.Domain) || ref.Kind != "report" || !validIdentity(ref.ID) || seen[ref] {
			return errors.New("invalid or duplicate report reference")
		}
		seen[ref] = true
	}
	return nil
}

// Validate against the frozen callback view before close clears it. Public
// reports that finish during inference are not newly offered citation targets.
func (h *MainHistory) validateReportReferences(refs []HistoryRef) error {
	if err := validateReportRefList(refs); err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	if h == nil {
		return errHistoryUnavailable
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.ctx == nil || h.ctx.Err() != nil || (h.lifetime != nil && h.lifetime.Err() != nil) {
		return errHistoryUnavailable
	}
	for _, ref := range refs {
		if ref.Domain != h.domain || h.nodes[historyKey{ref.Kind, ref.ID}] == nil {
			return errHistoryUnavailable
		}
	}
	return nil
}

// Resolve again from durable state under the coordinator lock, including on
// replay. Never trust callback memory, history cache contents or task summaries.
func (a *SuperAgent) resolveReportReferences(refs []HistoryRef) ([]ReportCitation, error) {
	if err := validateReportRefList(refs); err != nil {
		return nil, err
	}
	var result []ReportCitation
	for _, ref := range refs {
		index, err := strconv.Atoi(strings.TrimPrefix(ref.ID, "delivery-"))
		if ref.Domain != a.main.id || err != nil || index < 1 || index > len(a.events) || ref.ID != deliveryID(index-1) || !a.historyVisible(index-1) {
			return nil, errHistoryUnavailable
		}
		found := false
		for _, report := range a.reports {
			if report.DeliveryID == ref.ID {
				result = append(result, ReportCitation{Ref: ref, Report: *copyReport(&report)})
				found = true
				break
			}
		}
		if !found {
			return nil, errHistoryUnavailable
		}
	}
	return result, nil
}

func copyReportCitations(citations []ReportCitation) []ReportCitation {
	out := slices.Clone(citations)
	for i := range out {
		out[i].Report = *copyReport(&out[i].Report)
	}
	return out
}

func emptyMainDecision(d MainDecision) bool {
	return d.Action == "" && d.TaskID == "" && d.Text == "" && d.Target == nil && d.ReportRefs == nil
}
