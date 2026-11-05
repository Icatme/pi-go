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
					raw, err := encodeSnapshot(s, id)
					if err != nil {
						b.Fatal(err)
					}
					if kind == "main" {
						a, tr := snapshotValidationMain()
						tr.Main.Snapshot = raw
						for i := 0; i < 3; i++ {
							if err := a.validateMain(tr); err != nil {
								b.Fatal(err)
							}
						}
					} else {
						a, tr := snapshotValidationWorker()
						tr.Snapshot = raw
						if err := a.apply(tr, false); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}
