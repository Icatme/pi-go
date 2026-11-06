package runtimeeval

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/Icatme/pi-go/superagent"
)

func sortedCompareKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ValidateCompare is the only consumer of ExpectedWork. All runtime evidence
// is collected first, including actual Pi receipt and private-history contents.
func ValidateCompare(events []Event, r CompareResult) error {
	if !reflect.DeepEqual(events, LoadFixture()) {
		return errors.New("unknown comparison fixture/oracle")
	}
	if !slices.Contains(compareModes, r.Mode) || r.Repetition < 1 || r.Repetition > 5 {
		return errors.New("invalid comparison metadata")
	}
	single := r.Mode == compareModes[2] || r.Mode == compareModes[3]
	if len(r.Deliveries) != len(events) {
		return errors.New("delivery conservation")
	}
	expected := map[string]Event{}
	effects := map[string]Effect{}
	histories := map[string][]string{}
	accepted := map[string]int{}
	for i, e := range events {
		k := key(e.Trigger)
		d := r.Deliveries[i]
		if d.Source != e.Trigger.Source || d.EventID != e.Trigger.ID {
			return fmt.Errorf("delivery identity %d", i)
		}
		if d.Accepted {
			accepted[k]++
		}
		if _, ok := expected[k]; ok {
			continue
		}
		expected[k] = e
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
			return fmt.Errorf("acceptance conservation %s", k)
		}
	}
	if len(r.TaskWork) != 20 || !reflect.DeepEqual(histories, r.Histories) {
		return errors.New("task/history isolation")
	}
	works := map[string]bool{}
	for task, work := range r.TaskWork {
		if task == "" || works[work] || histories[work] == nil {
			return errors.New("task mapping")
		}
		works[work] = true
	}
	if len(r.Reports) != len(expected) {
		return errors.New("report conservation")
	}
	reports := map[string]superagent.Report{}
	deliveries := map[string]bool{}
	for _, report := range r.Reports {
		k := report.Source + "/" + report.EventID
		e, ok := expected[k]
		if !ok || reports[k].DeliveryID != "" || report.DeliveryID == "" || deliveries[report.DeliveryID] || report.Thread != e.Trigger.Thread || report.NeedsReview {
			return fmt.Errorf("report identity %s", k)
		}
		reports[k] = report
		deliveries[report.DeliveryID] = true
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
			return fmt.Errorf("native receipt %s", k)
		}
	}
	if len(r.Effects) != len(effects) {
		return errors.New("effect conservation")
	}
	seen := map[string]bool{}
	for _, effect := range r.Effects {
		k := effect.Source + "/" + effect.EventID
		want, ok := effects[k]
		if !ok || seen[k] || effect != want {
			return errors.New("effect identity")
		}
		seen[k] = true
	}
	inputs := map[string]superagent.MainInput{}
	kinds := map[string]int{}
	seenKinds := map[string]bool{}
	for _, in := range r.Inputs {
		k := key(in.Trigger)
		e, ok := expected[k]
		report := reports[k]
		identity := in.Kind + "/" + k
		if !ok || e.ExpectedWork == "" || in.Trigger != e.Trigger || in.ID == "" || inputs[in.ID].ID != "" || seenKinds[identity] || in.DeliveryID != report.DeliveryID {
			return fmt.Errorf("input identity %s", identity)
		}
		switch in.Kind {
		case "user":
			if in.TaskID != "" || in.Report != nil || (r.Mode == compareModes[0] && in.Trigger.Source == "github") {
				return errors.New("invalid user input")
			}
		case "task_input":
			if r.Mode != compareModes[0] || in.Trigger.Source != "github" || in.TaskID != report.TaskID || in.Report != nil {
				return errors.New("invalid subscribed input")
			}
		case "worker_result":
			expectedTask := ""
			if single {
				expectedTask = report.TaskID
			}
			if in.TaskID != expectedTask || in.Report == nil || !reflect.DeepEqual(*in.Report, report) {
				return errors.New("invalid result input")
			}
		default:
			return errors.New("unknown main input")
		}
		inputs[in.ID] = in
		kinds[in.Kind]++
		seenKinds[identity] = true
	}
	wantKinds := map[string]int{"user": 60, "worker_result": 60}
	if r.Mode == compareModes[0] {
		wantKinds = map[string]int{"user": 40, "task_input": 20, "worker_result": 60}
	}
	if !reflect.DeepEqual(kinds, wantKinds) {
		return errors.New("main input conservation")
	}
	if len(r.Replies) != len(effects) {
		return errors.New("effective reply conservation")
	}
	seen = map[string]bool{}
	for _, reply := range r.Replies {
		k := reply.Source + "/" + reply.EventID
		report, ok := reports[k]
		in := inputs[reply.InputID]
		prefix, decision, status := "reply-", "main-decision-", "pending"
		if single {
			prefix, decision, status = "single-final-", "single-decision-", "returned_undelivered"
		}
		if !ok || effects[k].WorkID == "" || seen[k] || reply.ID != prefix+reply.InputID || reply.DecisionID != decision+reply.InputID || in.Kind != "worker_result" || key(in.Trigger) != k || reply.DeliveryID != report.DeliveryID || reply.Thread != report.Thread || reply.TaskID != report.TaskID || reply.Text != report.Text || reply.Status != status || len(reply.Attempts) != 0 || reply.Report == nil || !reflect.DeepEqual(*reply.Report, report) {
			return fmt.Errorf("effective reply identity %s", k)
		}
		seen[k] = true
	}
	if single {
		if len(r.Acknowledgments) != 60 || r.ReopenChecked {
			return errors.New("async acknowledgment or persistence scope")
		}
		ackSeen := map[string]bool{}
		for _, ack := range r.Acknowledgments {
			in := inputs[ack.InputID]
			report := reports[key(in.Trigger)]
			if in.Kind != "user" || ackSeen[ack.InputID] || ack.DeliveryID != in.DeliveryID || ack.Receipt.CallID != "subtask-"+in.ID || !ack.Receipt.Completed || ack.Receipt.Token == 0 || ack.Receipt.ParentToken != 0 || ack.Receipt.Domain != "agent" || ack.Receipt.ToolName != "subtask" || ack.Receipt.Committed || ack.Text != "admitted:"+report.DeliveryID+":"+report.TaskID {
				return errors.New("native subtask acknowledgment identity")
			}
			ackSeen[ack.InputID] = true
		}
	} else if len(r.Acknowledgments) != 0 || !r.ReopenChecked {
		return errors.New("SuperAgent scope")
	}
	if r.PeakWorkers < 1 || r.PeakWorkers > CompareWorkerSlots || r.PeakTools < 1 || r.PeakTools > CompareWorkerSlots || r.PeakTools > r.PeakWorkers {
		return errors.New("worker/tool hard cap")
	}
	m := r.Metrics
	subtasks, calls := 0, 240
	if single {
		subtasks, calls = 60, 300
	}
	if r.WorkerScriptCalls != 120 || r.MainScriptCalls != calls-120 || r.MainScriptCalls+r.WorkerScriptCalls != m.ScriptCalls {
		return errors.New("role script counters disagree")
	}
	if m.UniqueEvents != 280 || m.Duplicates != 20 || m.TaskCount != 20 || m.WorkerInvocations != 60 || m.WorkerToolCalls != 60 || m.SubtaskToolCalls != subtasks || m.RecordEffects != 60 || m.ToolCalls != 60+subtasks || m.Replies != 60 || m.ScriptCalls != calls || m.ScriptCalls > CompareScriptCallLimit {
		return errors.New("metrics disagree with evidence")
	}
	if m.LiveModelCalls != 0 || m.InputTokens != nil || m.OutputTokens != nil || m.CostUSD != nil || m.LatencyMS != nil {
		return errors.New("unmeasured usage or timing")
	}
	if !r.ConflictChecked {
		return errors.New("missing conflict evidence")
	}
	return nil
}
