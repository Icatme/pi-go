package superagent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func snapshotValidationMain() (*SuperAgent, transition) {
	trigger := Trigger{ID: "request", Source: "im", Thread: "chat", Kind: "request"}
	a := &SuperAgent{events: []item{{trigger: trigger, state: "received"}}, main: mainState{id: "main-test", inbox: []mainItem{{input: MainInput{ID: "input", Kind: "user", Trigger: trigger}, event: 0}}}}
	tr := transition{State: "main_decided", Main: &mainTransition{InputID: "input", DecisionID: "main-decision-input", Decision: MainDecision{Action: "ignore"}}}
	return a, tr
}

func snapshotValidationWorker() (*SuperAgent, transition) {
	a := &SuperAgent{events: []item{{state: "running", task: "task-1"}}, tasks: map[string]*task{"task-1": {id: "task-1", running: true}}, running: 1}
	return a, transition{State: "completed", Text: "done"}
}

// Replay always validates the durable bytes, including Agent's stricter tool
// argument parser. Generic JSON validity alone is not enough.
func TestSnapshotReplayRejectsInvalidContent(t *testing.T) {
	for _, kind := range []string{"main", "worker"} {
		for name, raw := range map[string]string{
			"null": `null`, "array": `[]`, "foreign": `{"session_id":"foreign"}`,
			"invalid_type":           `{"messages":"not a message list"}`,
			"invalid_tool_arguments": `{"messages":[{"tool_calls":[{"arguments":{"value":"\ud800"}}]}]}`,
			"oversize":               `{"metadata":{"blob":"` + strings.Repeat("x", maxSnapshotBytes) + `"}}`,
		} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				var a *SuperAgent
				var tr transition
				if kind == "main" {
					a, tr = snapshotValidationMain()
					tr.Main.Snapshot = json.RawMessage(raw)
				} else {
					a, tr = snapshotValidationWorker()
					tr.Snapshot = json.RawMessage(raw)
				}
				if err := a.apply(tr, true); err == nil {
					t.Fatal("invalid snapshot accepted on replay")
				}
			})
		}
	}
}

type snapshotMarshalCounter struct{ calls *atomic.Int32 }

func (c snapshotMarshalCounter) MarshalJSON() ([]byte, error) {
	c.calls.Add(1)
	return []byte(`{"number":9007199254740993}`), nil
}

func TestSnapshotValidationKeepsCallbackAndReaderMemoryDetached(t *testing.T) {
	for _, kind := range []string{"main", "worker"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			metadata := map[string]any{"nested": map[string]any{"value": "original"}, "custom": snapshotMarshalCounter{&calls}}
			var a *SuperAgent
			var err error
			path := filepath.Join(t.TempDir(), "snapshots.jsonl")
			if kind == "main" {
				a, err = OpenMain(t.Context(), path, 1, func(_ context.Context, job MainJob) (MainOutcome, error) {
					return MainOutcome{Decision: MainDecision{Action: "ignore"}, Snapshot: agent.AgentSnapshot{SessionID: job.SessionID, Metadata: metadata}}, nil
				}, mainWorker)
			} else {
				a, err = Open(t.Context(), path, 1, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, func(_ context.Context, job Job) (Outcome, error) {
					return Outcome{Snapshot: agent.AgentSnapshot{SessionID: job.TaskID, Metadata: metadata}}, nil
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "request", Source: "im", Thread: "chat", Kind: "request"})
			waitIdle(t, a)
			if calls.Load() != 1 {
				t.Fatalf("snapshot serialized %d times", calls.Load())
			}
			metadata["nested"].(map[string]any)["value"] = "callback mutation"
			read := func() agent.AgentSnapshot {
				t.Helper()
				var s agent.AgentSnapshot
				var ok bool
				if kind == "main" {
					s, ok, err = a.MainSnapshot()
				} else {
					s, ok, err = a.TaskSnapshot("task-1")
				}
				if err != nil || !ok {
					t.Fatalf("read snapshot: %v, %v", ok, err)
				}
				return s
			}
			first := read()
			if first.Metadata["nested"].(map[string]any)["value"] != "original" {
				t.Fatal("callback retained coordinator snapshot memory")
			}
			if first.Metadata["custom"].(map[string]any)["number"] != json.Number("9007199254740993") {
				t.Fatal("lost JSON number precision")
			}
			first.Metadata["nested"].(map[string]any)["value"] = "reader mutation"
			if read().Metadata["nested"].(map[string]any)["value"] != "original" {
				t.Fatal("snapshot reader retained coordinator memory")
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "main" {
				a, err = OpenMain(t.Context(), path, 1, mainFixture, mainWorker)
			} else {
				a, err = Open(t.Context(), path, 1, func(context.Context, Trigger, []TaskInfo) (Route, error) {
					t.Fatal("replay routed work")
					return Route{}, nil
				}, mainWorker)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if read().Metadata["nested"].(map[string]any)["value"] != "original" {
				t.Fatal("replay changed snapshot contents")
			}
		})
	}
}

// This isolates snapshot encoding/validation from journal fsync and callbacks.
// Main validates at runMain, commit and applyMain; Worker validates at apply.
func BenchmarkSnapshotTransition(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 7 << 20} {
		for _, kind := range []string{"main", "worker"} {
			b.Run(fmt.Sprintf("%s/%dKiB", kind, size>>10), func(b *testing.B) {
				id := "main-test"
				if kind == "worker" {
					id = "task-1"
				}
				s := agent.AgentSnapshot{SessionID: id, Metadata: map[string]any{"blob": strings.Repeat("x", size)}}
				b.ReportAllocs()
				b.SetBytes(int64(size))
				b.ResetTimer()
				for b.Loop() {
					raw, validated, err := encodeSnapshot(s, id)
					if err != nil {
						b.Fatal(err)
					}
					if kind == "main" {
						a, tr := snapshotValidationMain()
						tr.Main.Snapshot, tr.validatedSnapshot = raw, validated
						for i := 0; i < 3; i++ {
							if err := a.validateMain(tr); err != nil {
								b.Fatal(err)
							}
						}
					} else {
						a, tr := snapshotValidationWorker()
						tr.Snapshot, tr.validatedSnapshot = raw, validated
						if err := a.apply(tr, false); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func TestValidatedSnapshotIsBoundToEncodedBytesAndOwner(t *testing.T) {
	for _, owner := range []string{"main-test", "task-1"} {
		t.Run(owner, func(t *testing.T) {
			source := agent.AgentSnapshot{SessionID: owner, Metadata: map[string]any{"value": "original"}}
			raw, proof, err := encodeSnapshot(source, owner)
			if err != nil {
				t.Fatal(err)
			}
			source.Metadata["value"] = "changed by callback"
			decoded, err := validateSnapshot(raw, owner, proof)
			if err != nil || decoded.Metadata["value"] != "original" {
				t.Fatalf("not detached from input: %+v, %v", decoded, err)
			}
			if _, err := validateSnapshot(raw, "different-owner", proof); err == nil {
				t.Fatal("validation bypassed session ownership")
			}
			changed := json.RawMessage(strings.Replace(string(raw), "original", "tampered", 1))
			if _, err := validateSnapshot(changed, owner, proof); err == nil {
				t.Fatal("valid but different JSON borrowed a prior validation")
			}
			// In-place edits cannot alter the immutable byte binding.
			copy(raw, changed)
			if _, err := validateSnapshot(raw, owner, proof); err == nil {
				t.Fatal("raw byte mutation altered the private validation")
			}
		})
	}
}

func TestValidatedSnapshotNeverBypassesMainStateChecks(t *testing.T) {
	a, tr := snapshotValidationMain()
	raw, proof, err := encodeSnapshot(agent.AgentSnapshot{SessionID: "main-test"}, "main-test")
	if err != nil {
		t.Fatal(err)
	}
	tr.Main.Snapshot, tr.validatedSnapshot = raw, proof
	if err := a.validateMain(tr); err != nil {
		t.Fatal(err)
	}
	tr.Main.InputID = "another input"
	if err := a.validateMain(tr); err == nil {
		t.Fatal("proof bypassed exact inbox identity")
	}
	tr.Main.InputID = "input"
	a.events[0].state = "routing"
	if err := a.validateMain(tr); err == nil {
		t.Fatal("proof bypassed current admission state")
	}
	a.events[0].state = "received"
	a.main.inbox[0].input.Kind = "worker_result"
	tr.Main.Decision = MainDecision{Action: "new"}
	if err := a.validateMain(tr); err == nil {
		t.Fatal("proof let a result authorize new work")
	}
}

func TestValidatedSnapshotIsProcessLocalAndReplayUsesDiskBytes(t *testing.T) {
	for _, kind := range []string{"main", "worker"} {
		t.Run(kind, func(t *testing.T) {
			var a *SuperAgent
			var tr transition
			owner := "main-test"
			if kind == "main" {
				a, tr = snapshotValidationMain()
			} else {
				a, tr = snapshotValidationWorker()
				owner = "task-1"
			}
			raw, proof, err := encodeSnapshot(agent.AgentSnapshot{SessionID: owner}, owner)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "main" {
				tr.Main.Snapshot = raw
			} else {
				tr.Snapshot = raw
			}
			without, err := json.Marshal(tr)
			if err != nil {
				t.Fatal(err)
			}
			tr.validatedSnapshot = proof
			with, err := json.Marshal(tr)
			if err != nil {
				t.Fatal(err)
			}
			if string(with) != string(without) {
				t.Fatal("private validation changed the journal format")
			}
			var disk transition
			if err := json.Unmarshal(with, &disk); err != nil {
				t.Fatal(err)
			}
			if disk.validatedSnapshot != nil {
				t.Fatal("proof survived serialization")
			}
			// Even an in-process replay call with a stale proof must read disk bytes.
			// A different valid snapshot should replay, but invalid content should not.
			changed := json.RawMessage(fmt.Sprintf(`{"session_id":%q,"metadata":{"disk":true}}`, owner))
			if kind == "main" {
				tr.Main.Snapshot = changed
			} else {
				tr.Snapshot = changed
			}
			if err := a.apply(tr, true); err != nil {
				t.Fatalf("replay relied on a live proof instead of valid disk bytes: %v", err)
			}
			if kind == "main" {
				a, tr = snapshotValidationMain()
				tr.Main.Snapshot = json.RawMessage(`{"session_id":"foreign"}`)
			} else {
				a, tr = snapshotValidationWorker()
				tr.Snapshot = json.RawMessage(`{"session_id":"foreign"}`)
			}
			tr.validatedSnapshot = proof
			if err := a.apply(tr, true); err == nil {
				t.Fatal("proof bypassed strict disk validation")
			}
		})
	}
}

// Three main checks still run under the coordinator lock. This benchmark
// isolates their content-validation cost, not total lock duration or I/O.
func BenchmarkMainSnapshotChecks(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 7 << 20} {
		for _, mode := range []string{"reparse", "reuse"} {
			b.Run(fmt.Sprintf("%dKiB/%s", size>>10, mode), func(b *testing.B) {
				a, tr := snapshotValidationMain()
				raw, proof, err := encodeSnapshot(agent.AgentSnapshot{SessionID: "main-test", Metadata: map[string]any{"blob": strings.Repeat("x", size)}}, "main-test")
				if err != nil {
					b.Fatal(err)
				}
				tr.Main.Snapshot = raw
				if mode == "reuse" {
					tr.validatedSnapshot = proof
				}
				b.ReportAllocs()
				b.SetBytes(int64(size))
				b.ResetTimer()
				for b.Loop() {
					for i := 0; i < 3; i++ {
						if err := a.validateMain(tr); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func TestInvalidEncodedMainSnapshotCannotPoisonJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.jsonl")
	route := func(_ context.Context, job MainJob) (MainOutcome, error) {
		s := agent.AgentSnapshot{SessionID: job.SessionID, Metadata: map[string]any{"valid": "retained"}}
		if job.Input.Trigger.ID == "bad" {
			s.Messages = []agent.Message{{ToolCalls: []agent.ToolCall{{Arguments: json.RawMessage(`{"value":"\ud800"}`)}}}}
		} else if job.Input.Trigger.ID == "after" && job.Snapshot.Metadata["valid"] != "retained" {
			t.Error("invalid result replaced last valid main snapshot")
		}
		return MainOutcome{Decision: MainDecision{Action: "ignore"}, Snapshot: s}, nil
	}
	a, err := OpenMain(t.Context(), path, 1, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, id := range []string{"before", "bad", "after"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"})
		waitIdle(t, a)
	}
	if failures := a.MainFailures(); len(failures) != 1 || a.MainFailureCause(failures[0].InputID) == nil {
		t.Fatalf("invalid main encoding not recorded as failure: %+v", failures)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 1, route, mainWorker)
	if err != nil {
		t.Fatalf("invalid snapshot poisoned journal: %v", err)
	}
	defer b.Close()
	if len(b.MainFailures()) != 1 {
		t.Fatal("main failure lost after reopen")
	}
	s, ok, err := b.MainSnapshot()
	if err != nil || !ok || s.Metadata["valid"] != "retained" {
		t.Fatal("last valid snapshot lost after reopen")
	}
}
