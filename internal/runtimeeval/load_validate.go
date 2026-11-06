package runtimeeval

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/Icatme/pi-go/superagent"
)

// ValidateLoad applies the oracle after execution, checking raw identities
// before normalized results. A concurrent duplicate may win at either index.
func ValidateLoad(events []Event, r LoadResult) error {
	if !reflect.DeepEqual(events, LoadFixture()) {
		return errors.New("unknown load fixture")
	}
	if r.Repetition < 1 || r.Repetition > 5 || (r.Mode != loadModes[0] && r.Mode != loadModes[1]) {
		return errors.New("invalid load metadata")
	}
	if len(r.Deliveries) != len(events) {
		return errors.New("load delivery conservation")
	}
	expected := map[string]Event{}
	effects := map[string]Effect{}
	histories := map[string][]string{}
	phases, accepted := map[string]int{}, map[string]int{}
	for i, e := range events {
		k := key(e.Trigger)
		d := r.Deliveries[i]
		if d.Source != e.Trigger.Source || d.EventID != e.Trigger.ID {
			return fmt.Errorf("delivery identity %d", i)
		}
		if d.Accepted {
			accepted[k]++
		}
		if _, exists := expected[k]; exists {
			continue
		}
		expected[k], phases[k] = e, i/100
		if e.ExpectedWork != "" {
			var p Payload
			if err := json.Unmarshal([]byte(e.Trigger.Text), &p); err != nil {
				return err
			}
			effects[k] = Effect{e.Trigger.Source, e.Trigger.ID, e.ExpectedWork, p.Value}
			histories[e.ExpectedWork] = append(histories[e.ExpectedWork], k)
		}
	}
	for k := range expected {
		if accepted[k] != 1 {
			return fmt.Errorf("event %s accepted %d times", k, accepted[k])
		}
	}
	if len(r.TaskWork) != 20 || !reflect.DeepEqual(histories, r.Histories) {
		return errors.New("task/history conservation or isolation")
	}
	workSeen := map[string]bool{}
	for task, work := range r.TaskWork {
		if task == "" || workSeen[work] || histories[work] == nil {
			return errors.New("invalid task mapping")
		}
		workSeen[work] = true
	}
	if len(r.Reports) != len(expected) {
		return errors.New("terminal report conservation")
	}
	reports := map[string]superagent.Report{}
	deliveries := map[string]bool{}
	for _, report := range r.Reports {
		k := report.Source + "/" + report.EventID
		e, ok := expected[k]
		if !ok || reports[k].DeliveryID != "" || report.DeliveryID == "" || deliveries[report.DeliveryID] || report.Thread != e.Trigger.Thread || report.NeedsReview {
			return fmt.Errorf("report identity %s", k)
		}
		reports[k], deliveries[report.DeliveryID] = report, true
		if e.ExpectedWork == "" {
			if report.Status != "ignored" || report.TaskID != "" || len(report.Receipts) != 0 {
				return errors.New("noise produced work")
			}
			continue
		}
		if r.TaskWork[report.TaskID] != e.ExpectedWork || report.Status != "completed" || len(report.Receipts) != 1 {
			return fmt.Errorf("work report %s", k)
		}
		var effect Effect
		if json.Unmarshal([]byte(report.Text), &effect) != nil || effect != effects[k] {
			return fmt.Errorf("report effect %s", k)
		}
		receipt := report.Receipts[0]
		if receipt.Token == 0 || receipt.ParentToken != 0 || receipt.Domain != "agent" || receipt.Committed || receipt.ToolName != "record" || receipt.CallID != "call-"+report.Source+"-"+report.EventID || !receipt.Completed {
			return fmt.Errorf("native receipt identity %s", k)
		}
	}
	if len(r.Effects) != len(effects) {
		return errors.New("tool effect conservation")
	}
	seenEffects := map[string]bool{}
	for _, e := range r.Effects {
		k := e.Source + "/" + e.EventID
		if seenEffects[k] || effects[k] != e {
			return fmt.Errorf("incorrect actual effect %s", k)
		}
		seenEffects[k] = true
	}
	inputs := map[string]superagent.MainInput{}
	seenKinds, kinds := map[string]bool{}, map[string]int{}
	multi := false
	for _, batch := range r.Batches {
		raw, err := json.Marshal(batch.Inputs)
		if err != nil || len(raw) != batch.InputBytes || batch.InputBytes > LoadBatchBytes || len(batch.Inputs) < 1 || len(batch.Inputs) > 8 || batch.Phase < 0 || batch.Phase > 2 {
			return errors.New("batch budget or phase")
		}
		if r.Mode == loadModes[0] && len(batch.Inputs) != 1 {
			return errors.New("unbatched arm grouped inputs")
		}
		multi = multi || len(batch.Inputs) > 1
		for _, input := range batch.Inputs {
			k := key(input.Trigger)
			e, ok := expected[k]
			report := reports[k]
			identity := input.Kind + "/" + k
			if !ok || e.ExpectedWork == "" || input.Trigger != e.Trigger || batch.Phase != phases[k] || input.ID == "" || inputs[input.ID].ID != "" || seenKinds[identity] || input.DeliveryID != report.DeliveryID {
				return fmt.Errorf("main input identity %s", identity)
			}
			switch input.Kind {
			case "user":
				if e.Trigger.Source != "im" || input.TaskID != "" || input.Report != nil {
					return errors.New("incorrect user input")
				}
			case "task_input":
				if e.Trigger.Source != "github" || input.TaskID != report.TaskID || input.Report != nil {
					return errors.New("incorrect subscription input")
				}
			case "worker_result":
				if input.Report == nil || !reflect.DeepEqual(*input.Report, report) {
					return errors.New("incorrect result input")
				}
			default:
				return errors.New("unknown main input kind")
			}
			inputs[input.ID], seenKinds[identity] = input, true
			kinds[input.Kind]++
		}
	}
	if !reflect.DeepEqual(kinds, map[string]int{"user": 40, "task_input": 20, "worker_result": 60}) || (r.Mode == loadModes[1] && !multi) {
		return errors.New("main input conservation or batching not exercised")
	}
	if len(r.Replies) != len(effects) {
		return errors.New("reply conservation")
	}
	seenReplies := map[string]bool{}
	for _, reply := range r.Replies {
		k := reply.Source + "/" + reply.EventID
		report, ok := reports[k]
		input := inputs[reply.InputID]
		if !ok || effects[k].WorkID == "" || seenReplies[k] || reply.ID != "reply-"+reply.InputID || reply.DecisionID != "main-decision-"+reply.InputID || input.Kind != "worker_result" || key(input.Trigger) != k || reply.DeliveryID != report.DeliveryID || reply.Thread != report.Thread || reply.TaskID != report.TaskID || reply.Text != report.Text || reply.Status != "pending" || len(reply.Attempts) != 0 || reply.Report == nil || !reflect.DeepEqual(*reply.Report, report) {
			return fmt.Errorf("reply identity %s", k)
		}
		seenReplies[k] = true
	}
	if r.PeakTools != loadSlots || r.VirtualToolEndMS != 150 || len(r.ToolReleases) != len(effects) {
		return errors.New("tool concurrency or virtual clock totals")
	}
	released, ticks := map[string]bool{}, map[int]int{}
	for _, release := range r.ToolReleases {
		k := release.Effect.Source + "/" + release.Effect.EventID
		if released[k] || effects[k] != release.Effect || release.StartMS < 0 || release.ReleaseMS != release.StartMS+loadServiceMS || release.ReleaseMS%loadServiceMS != 0 || release.StartMS/50 != phases[k] {
			return fmt.Errorf("virtual tool release %s", k)
		}
		released[k] = true
		ticks[release.ReleaseMS]++
	}
	for tick := loadServiceMS; tick <= r.VirtualToolEndMS; tick += loadServiceMS {
		if ticks[tick] != loadSlots {
			return errors.New("virtual wave conservation")
		}
	}
	m := r.Metrics
	if m.UniqueEvents != 280 || m.Duplicates != 20 || m.TaskCount != 20 || m.WorkerInvocations != 60 || m.WorkerToolCalls != 60 || m.SubtaskToolCalls != 0 || m.RecordEffects != 60 || m.ToolCalls != 60 || m.Replies != 60 || m.ScriptCalls != 120+len(r.Batches) || m.ScriptCalls > LoadScriptCallLimit {
		return errors.New("load metrics disagree with execution")
	}
	if m.LiveModelCalls != 0 || m.InputTokens != nil || m.OutputTokens != nil || m.CostUSD != nil || m.LatencyMS != nil {
		return errors.New("offline load cannot supply live performance or cost")
	}
	if !r.ReopenChecked || !r.ConflictChecked {
		return errors.New("missing replay/conflict check")
	}
	return nil
}
