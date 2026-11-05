package runtimeeval

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// Validate scores observed reports, native tool receipts, tool-body effects,
// replies and isolated Pi histories against the fixture only after execution.
// The script has no access to ExpectedWork or this scorer.
func Validate(events []Event, r Result) error {
	if r.Repetition < 1 || r.Repetition > 5 {
		return fmt.Errorf("invalid repetition")
	}
	if len(r.Deliveries) != len(events) {
		return fmt.Errorf("delivery conservation: %d != %d", len(r.Deliveries), len(events))
	}
	seen := map[string]Event{}
	expectedEffects := map[string]Effect{}
	histories := map[string][]string{}
	duplicates := 0
	for i, e := range events {
		d := r.Deliveries[i]
		k := key(e.Trigger)
		prior, dup := seen[k]
		if dup {
			duplicates++
			if prior != e {
				return fmt.Errorf("conflicting fixture duplicate %s", k)
			}
		}
		if d.Source != e.Trigger.Source || d.EventID != e.Trigger.ID || d.Duplicate != dup || d.WorkID != e.ExpectedWork {
			return fmt.Errorf("incorrect delivery %d: %+v", i, d)
		}
		status := "completed"
		receipts := 1
		if e.ExpectedWork == "" {
			status = "ignored"
			receipts = 0
		}
		if d.Status != status || d.Receipts != receipts {
			return fmt.Errorf("incorrect outcome %s: %+v", k, d)
		}
		if dup {
			continue
		}
		seen[k] = e
		if e.ExpectedWork != "" {
			var p Payload
			if err := json.Unmarshal([]byte(e.Trigger.Text), &p); err != nil {
				return err
			}
			expectedEffects[k] = Effect{e.Trigger.Source, e.Trigger.ID, e.ExpectedWork, p.Value}
			histories[e.ExpectedWork] = append(histories[e.ExpectedWork], k)
		}
	}
	for _, set := range []struct {
		name   string
		values []Effect
	}{{"effects", r.Effects}, {"replies", r.Replies}} {
		if len(set.values) != len(expectedEffects) {
			return fmt.Errorf("%s conservation", set.name)
		}
		observed := map[string]bool{}
		for _, e := range set.values {
			k := e.Source + "/" + e.EventID
			if observed[k] || expectedEffects[k] != e {
				return fmt.Errorf("incorrect %s for %s", set.name, k)
			}
			observed[k] = true
		}
	}
	if !reflect.DeepEqual(histories, r.Histories) {
		return fmt.Errorf("task history isolation: got %v want %v", r.Histories, histories)
	}
	m := r.Metrics
	// Every non-noise event requires two worker script turns. SuperAgent has
	// one main observation/decision plus one result reply; single Pi has one
	// subtask tool call and one final reply. None of these are live model calls.
	expectedMain := map[string]int{}
	switch r.Mode {
	case "superagent_subscribed", "superagent_router_only":
		expectedMain["user"] = len(expectedEffects)
		expectedMain["worker_result"] = len(expectedEffects)
		if r.Mode == "superagent_subscribed" {
			for _, e := range seen {
				if e.Trigger.Source == "github" {
					expectedMain["user"]--
					expectedMain["task_input"]++
				}
			}
		}
		if m.SubtaskToolCalls != 0 || !r.ReopenChecked {
			return fmt.Errorf("incorrect SuperAgent scope evidence")
		}
	case "single_pi_neutral", "single_pi_delegate_first":
		if m.SubtaskToolCalls != len(expectedEffects) || r.ReopenChecked {
			return fmt.Errorf("incorrect single-Pi scope evidence")
		}
	default:
		return fmt.Errorf("unknown evaluation arm")
	}
	if !reflect.DeepEqual(expectedMain, r.MainInputs) {
		return fmt.Errorf("incorrect observed main input kinds: %v", r.MainInputs)
	}
	if m.UniqueEvents != len(seen) || m.Duplicates != duplicates || m.TaskCount != len(histories) || m.RecordEffects != len(expectedEffects) || m.WorkerInvocations != len(expectedEffects) || m.WorkerToolCalls != len(expectedEffects) || m.ToolCalls != m.WorkerToolCalls+m.SubtaskToolCalls || m.Replies != len(expectedEffects) || m.ScriptCalls != 4*len(expectedEffects) || m.ScriptCalls > ScriptCallLimit {
		return fmt.Errorf("metrics disagree with observations: %+v", m)
	}
	if m.LiveModelCalls != 0 || m.InputTokens != nil || m.OutputTokens != nil || m.CostUSD != nil || m.LatencyMS != nil {
		return fmt.Errorf("script cannot supply live usage or performance")
	}
	return nil
}
