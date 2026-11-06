package runtimeeval

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

type mixedEvidence struct {
	View      superagent.View
	Replies   []superagent.Reply
	Controls  []superagent.MainControl
	Snapshots map[string]agent.AgentSnapshot
	Effects   []mixedCall
	Failures  []superagent.MainFailure
}

func validateMixed(t *testing.T, a *superagent.SuperAgent, cp mixedCheckpoint, effects []mixedCall, crash bool) {
	t.Helper()
	evidence := mixedEvidence{a.Status(), a.Replies(), a.MainControls(), mixedSnapshot(t, a), effects, a.MainFailures()}
	if err := checkMixedEvidence(cp, evidence, crash); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"drop_report", "duplicate_delivery", "wrong_task", "receipt_commit", "effect_missing", "effect_duplicate", "effect_empty", "reply", "ignored_reply", "wrong_decision", "correction", "history", "missing_snapshot", "missing_reply", "invent_crash_receipt"} {
		if fault == "invent_crash_receipt" && !crash {
			continue
		}
		t.Run(fault, func(t *testing.T) {
			raw, err := json.Marshal(evidence)
			if err != nil {
				t.Fatal(err)
			}
			var bad mixedEvidence
			if err := json.Unmarshal(raw, &bad); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "drop_report":
				bad.View.Reports = bad.View.Reports[1:]
			case "duplicate_delivery":
				bad.View.Reports[1].DeliveryID = bad.View.Reports[0].DeliveryID
			case "wrong_task":
				bad.View.Reports[0].TaskID = "foreign"
			case "receipt_commit":
				for i := range bad.View.Reports {
					if len(bad.View.Reports[i].Receipts) > 0 {
						bad.View.Reports[i].Receipts[0].Committed = false
						break
					}
				}
			case "effect_missing":
				bad.Effects = bad.Effects[1:]
			case "effect_duplicate":
				bad.Effects = append(bad.Effects, bad.Effects[0])
			case "effect_empty":
				bad.Effects[0] = mixedCall{}
			case "reply":
				bad.Replies[0].DeliveryID = "foreign"
			case "ignored_reply":
				for _, r := range bad.View.Reports {
					if r.TaskID != "" {
						continue
					}
					body, err := json.Marshal(r)
					if err != nil {
						t.Fatal(err)
					}
					bad.Replies = append(bad.Replies, superagent.Reply{ID: "reply-extra", InputID: "extra", DecisionID: "main-decision-extra", DeliveryID: r.DeliveryID, Status: "pending", Source: r.Source, Thread: r.Thread, EventID: r.EventID, Report: &r, Text: string(body)})
					break
				}
			case "wrong_decision":
				bad.Replies[len(bad.Replies)-1].DecisionID = "main-decision-foreign"
			case "correction":
				bad.Controls[0].Target.TaskID = cp.Tasks["A"]
			case "history":
				snap := bad.Snapshots[cp.Tasks["A"]]
				snap.Messages = append(snap.Messages, agent.NewUserTextMessage(mixedCorrection))
				bad.Snapshots[cp.Tasks["A"]] = snap
			case "missing_snapshot":
				delete(bad.Snapshots, cp.Tasks["A"])
			case "missing_reply":
				bad.Replies = bad.Replies[1:]
			case "invent_crash_receipt":
				for i := range bad.View.Reports {
					r := &bad.View.Reports[i]
					if r.TaskID == cp.Tasks["B"] && r.EventID == "shared" {
						r.Receipts = []superagent.ToolReceipt{{Token: 1, Committed: true}}
					}
				}
			}
			if err := checkMixedEvidence(cp, bad, crash); err == nil {
				t.Fatal("corrupted evidence passed")
			}
		})
	}
	t.Logf("35 submissions, 23 event identities, 8 tasks, %d terminal deliveries, %d pending replies, %d fsynced effects; crash=%v; live model calls=0; tokens/cost/latency unmeasured", len(evidence.View.Reports), len(evidence.Replies), len(evidence.Effects), crash)
}

func checkMixedEvidence(cp mixedCheckpoint, evidence mixedEvidence, crash bool) error {
	if len(evidence.Failures) != 0 {
		return fmt.Errorf("main failures: %+v", evidence.Failures)
	}
	if len(cp.Tasks) != 2+mixedRounds || len(evidence.View.Tasks) != len(cp.Tasks) {
		return errors.New("task count")
	}
	byTask := map[string]string{}
	for work, task := range cp.Tasks {
		if task == "" || byTask[task] != "" {
			return errors.New("task identity")
		}
		byTask[task] = work
	}
	expected := map[string]superagent.Trigger{}
	accepted := map[string]int{}
	for _, admission := range cp.Admissions {
		key := admission.Trigger.Source + "/" + admission.Trigger.ID
		if admission.Accepted {
			accepted[key]++
		}
		for _, task := range admission.Targets {
			expected[task+"/"+key] = admission.Trigger
		}
	}
	for _, n := range accepted {
		if n != 1 {
			return errors.New("admission conservation")
		}
	}
	if len(accepted) != 5+3*mixedRounds || len(cp.Admissions) != 5+5*mixedRounds {
		return fmt.Errorf("fixture admission counts: unique=%d deliveries=%d", len(accepted), len(cp.Admissions))
	}
	// A source/event fanout is two terminal delivery identities, not one.
	reports := map[string]superagent.Report{}
	deliveries := map[string]bool{}
	wantEffects := map[string]mixedCall{}
	for _, r := range evidence.View.Reports {
		key := r.TaskID + "/" + r.Source + "/" + r.EventID
		tr, ok := expected[key]
		if !ok || reports[key].DeliveryID != "" || r.DeliveryID == "" || deliveries[r.DeliveryID] || r.Thread != tr.Thread {
			return fmt.Errorf("report identity: %+v", r)
		}
		reports[key], deliveries[r.DeliveryID] = r, true
		status, review, receipt := "completed", false, true
		work := byTask[r.TaskID]
		if r.TaskID == "" {
			status, receipt = "ignored", false
		}
		if r.EventID == "shared" && work == "A" {
			status, review = "interrupted", true
			if crash {
				status = "failed"
			}
		}
		if r.EventID == "shared" && work == "B" && crash {
			status, review, receipt = "interrupted", true, false
		}
		for i := 0; i < mixedRounds; i++ {
			if r.EventID == fmt.Sprintf("follow-%d", i) && (work == "A" || crash) {
				status, receipt = "blocked", false
			}
		}
		if r.Status != status || r.NeedsReview != review {
			return fmt.Errorf("status for %s: got=%+v want=%s review=%v", key, r, status, review)
		}
		if receipt {
			if len(r.Receipts) != 1 {
				return fmt.Errorf("missing receipt: %+v", r)
			}
			fact := r.Receipts[0]
			if fact.Token == 0 || fact.ParentToken != 0 || fact.Domain != "agent" || fact.ToolName != "write" || fact.CallID != "write-"+r.Source+"-"+r.EventID || !fact.Committed || !fact.Completed {
				return fmt.Errorf("receipt identity/commit: %+v", fact)
			}
		} else if len(r.Receipts) != 0 {
			return fmt.Errorf("fabricated receipt for blocked/unknown effect: %+v", r)
		}
		if receipt || (crash && work == "B" && r.EventID == "shared") {
			wantEffects[key] = mixedCall{r.TaskID, work, r.Source, r.EventID}
		}
	}
	if len(reports) != len(expected) {
		return fmt.Errorf("report conservation: %d != %d", len(reports), len(expected))
	}
	taskSeen := map[string]bool{}
	for _, task := range evidence.View.Tasks {
		work := byTask[task.ID]
		if work == "" || taskSeen[task.ID] || task.Pending != 0 || task.ActiveEventID != "" || task.ActiveSource != "" || task.ActiveDeliveryID != "" {
			return errors.New("task state conservation")
		}
		taskSeen[task.ID] = true
		if work == "A" || (work == "B" && crash) {
			r := reports[task.ID+"/github/shared"]
			if task.Status != "needs_review" || task.ReviewSource != "github" || task.ReviewEventID != "shared" || task.ReviewDeliveryID != r.DeliveryID {
				return errors.New("task review barrier lost exact delivery")
			}
		} else if task.Status != "completed" || task.ReviewDeliveryID != "" {
			return errors.New("unrelated task acquired review barrier")
		}
	}
	seen := map[string]bool{}
	for _, effect := range evidence.Effects {
		want, ok := wantEffects[effect.key()]
		if !ok || seen[effect.key()] || want != effect {
			return fmt.Errorf("duplicate or wrong physical effect: %+v", effect)
		}
		seen[effect.key()] = true
	}
	if len(seen) != len(wantEffects) {
		return errors.New("missing committed physical effect")
	}
	for _, old := range cp.Reports {
		if !reflect.DeepEqual(old, reports[old.TaskID+"/"+old.Source+"/"+old.EventID]) {
			return errors.New("checkpoint report changed")
		}
	}
	// Every execution outcome is returned to its source thread with the exact
	// authoritative report attached. No actual external send occurs.
	replied := map[string]bool{}
	ids := map[string]bool{}
	for _, reply := range evidence.Replies {
		if reply.InputID == "" || reply.ID != "reply-"+reply.InputID || ids[reply.ID] || reply.DecisionID != "main-decision-"+reply.InputID || reply.Status != "pending" || reply.Report == nil || reply.TaskID == "" {
			return fmt.Errorf("reply identity/state: %+v", reply)
		}
		ids[reply.ID] = true
		r := *reply.Report
		key := r.TaskID + "/" + r.Source + "/" + r.EventID
		var quoted superagent.Report
		if json.Unmarshal([]byte(reply.Text), &quoted) != nil || !reflect.DeepEqual(quoted, r) || !reflect.DeepEqual(reports[key], r) || replied[r.DeliveryID] || reply.DeliveryID != r.DeliveryID || reply.Source != r.Source || reply.Thread != r.Thread || reply.EventID != r.EventID || reply.TaskID != r.TaskID {
			return fmt.Errorf("reply lost report facts/correlation: %+v", reply)
		}
		replied[r.DeliveryID] = true
	}
	for _, report := range reports {
		if report.TaskID != "" && !replied[report.DeliveryID] {
			return fmt.Errorf("missing outcome reply: %+v", report)
		}
	}
	for _, old := range cp.Replies {
		if !slices.ContainsFunc(evidence.Replies, func(r superagent.Reply) bool { return reflect.DeepEqual(r, old) }) {
			return errors.New("checkpoint reply changed")
		}
	}
	controls := evidence.Controls
	if len(controls) != 1 {
		return errors.New("correction duplicated/lost")
	}
	c := controls[0]
	status := "delivered"
	if crash {
		status = "not_delivered"
	}
	if c.Target != (superagent.RunTarget{TaskID: cp.Tasks["B"], Source: "github", EventID: "shared"}) || c.EventID != "correction" || c.Status != status || c.InputAccepted == crash || c.Text != mixedCorrection {
		return fmt.Errorf("correction evidence: %+v", c)
	}
	snapshots := evidence.Snapshots
	if len(snapshots) != len(cp.Tasks) {
		return errors.New("snapshot conservation")
	}
	for task := range snapshots {
		if byTask[task] == "" {
			return errors.New("foreign snapshot")
		}
	}
	for task, snapshot := range snapshots {
		work := byTask[task]
		var events []string
		corrections := 0
		for _, message := range snapshot.Messages {
			if message.Role != agent.RoleUser {
				continue
			}
			if textOf(message) == mixedCorrection {
				corrections++
				if task != cp.Tasks["B"] || message.Metadata["superagent.control"] != c.ID {
					return errors.New("correction leaked or lost durable marker")
				}
				continue
			}
			if message.Metadata["superagent.trigger_task"] != task {
				return errors.New("cross-task private input")
			}
			var tr superagent.Trigger
			if err := json.Unmarshal([]byte(textOf(message)), &tr); err != nil {
				return err
			}
			events = append(events, tr.Source+"/"+tr.ID)
		}
		want := []string{"im/" + work}
		if work == "A" || work == "B" {
			want = []string{"im/seed-" + work, "github/shared"}
		}
		if work == "B" {
			if crash {
				want = want[:1]
			} else {
				for i := 0; i < mixedRounds; i++ {
					want = append(want, fmt.Sprintf("github/follow-%d", i))
				}
			}
		}
		if !reflect.DeepEqual(events, want) || (corrections == 1) != (work == "B" && !crash) || corrections > 1 {
			return fmt.Errorf("private history work=%s got=%v want=%v corrections=%d", work, events, want, corrections)
		}
		if crash && !reflect.DeepEqual(snapshot, cp.Snapshots[task]) {
			return errors.New("crash recovery invented unpersisted transcript")
		}
	}
	return nil
}
