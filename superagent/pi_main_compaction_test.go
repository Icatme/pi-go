package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func compactionHistory() []agent.Message {
	answer := agent.NewTextMessage(agent.RoleAssistant, `{"action":"ignore"}`)
	answer.StopReason = agent.StopReasonStop
	call := agent.ToolCall{ID: "old-read", Name: "history_read", Arguments: json.RawMessage(`{"ref":"old"}`)}
	request := agent.NewTextMessage(agent.RoleAssistant, "")
	request.ToolCalls, request.StopReason = []agent.ToolCall{call}, agent.StopReasonToolUse
	return []agent.Message{agent.NewUserTextMessage(strings.Repeat("old", 20000)), answer,
		agent.NewUserTextMessage("retained"), request,
		agent.NewToolResultMessage(call, agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "original public evidence"}}}, false), answer}
}

func compactionRouter(t *testing.T, history, batch bool, def agent.AgentDefinition, budget int, compactor ...MainContextCompactor) func(context.Context, MainJob) (agent.AgentSnapshot, error) {
	t.Helper()
	if batch {
		var router MainBatchRouter
		var err error
		if history {
			router, err = PiMainBatchHistoryRouter(def, budget, compactor...)
		} else {
			router, err = PiMainBatchRouter(def, budget, compactor...)
		}
		if err != nil {
			t.Fatal(err)
		}
		return func(ctx context.Context, job MainJob) (agent.AgentSnapshot, error) {
			out, err := router(ctx, MainBatchJob{SessionID: job.SessionID, ProviderSessionID: job.ProviderSessionID, Snapshot: job.Snapshot, Inputs: []MainInput{job.Input}, Tasks: job.Tasks, Runs: job.Runs, History: job.History})
			return out.Snapshot, err
		}
	}
	var router MainRouter
	var err error
	if history {
		router, err = PiMainHistoryRouter(def, budget, compactor...)
	} else {
		router, err = PiMainRouter(def, budget, compactor...)
	}
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, job MainJob) (agent.AgentSnapshot, error) {
		out, err := router(ctx, job)
		return out.Snapshot, err
	}
}

func TestPiMainCompactionAdapters(t *testing.T) {
	for _, history := range []bool{false, true} {
		for _, batch := range []bool{false, true} {
			name := "single"
			if batch {
				name = "batch"
			}
			if history {
				name += "-history"
			}
			t.Run(name, func(t *testing.T) {
				job := historyFixtureJob(t)
				job.Snapshot.Messages = compactionHistory()
				job.Snapshot.Metadata = map[string]any{"owner": "original"}
				job.Tasks = []TaskInfo{{ID: "offered", Summary: "unchanged"}}
				job.Runs = []RunTarget{{TaskID: "offered", Source: "im", EventID: "run"}}
				before, _ := json.Marshal(job.Snapshot)
				var expected []byte
				if batch {
					expected, _ = json.Marshal(mainBatchModelInput{Inputs: []MainInput{job.Input}, Tasks: job.Tasks, Runs: job.Runs})
				} else {
					expected, _ = json.Marshal(mainModelInput{Input: job.Input, Tasks: job.Tasks, Runs: job.Runs})
				}
				models, summaries := 0, 0
				def := agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
					models++
					if req.SessionID != "wire" || messageText(req.Messages[len(req.Messages)-1]) != string(expected) {
						t.Fatal("current payload or provider identity changed")
					}
					if err := checkMainHistoryContextBudget(req.SystemPrompt, req.Messages, req.Tools, 24<<10); err != nil {
						t.Fatal(err)
					}
					// The host callback cannot rewrite retained tool arguments/results.
					for _, message := range req.Messages {
						if message.ToolResult != nil && messageText(message) != "original public evidence" {
							t.Fatal("retained tool result mutated")
						}
					}
					if !strings.Contains(messageText(req.Messages[1]), "main_context_summary") || req.Messages[1].Role != agent.RoleUser {
						t.Fatal("summary was not untrusted context")
					}
					if batch {
						return offlineAnswer(`{"decisions":[{"input_id":"input","decision":{"action":"ignore"}}]}`), nil
					}
					return offlineAnswer(`{"action":"ignore"}`), nil
				})}
				router := compactionRouter(t, history, batch, def, 24<<10)
				if _, err := router(t.Context(), job); err == nil || models != 0 {
					t.Fatal("default overflow did not fail closed")
				}
				compact := func(_ context.Context, messages []agent.Message) (MainContextCompaction, error) {
					summaries++
					if len(messages) != len(job.Snapshot.Messages) {
						t.Fatal("current input sent to compactor")
					}
					messages[4].ToolResult.Content[0].Text = "mutated"
					return MainContextCompaction{Summary: "The old task failed; preserve that fact.", KeepFrom: 2}, nil
				}
				router = compactionRouter(t, history, batch, def, 24<<10, compact)
				snapshot, err := router(t.Context(), job)
				if err != nil || models != 1 || summaries != 1 || snapshot.SessionID != "main" {
					t.Fatalf("models=%d summaries=%d err=%v", models, summaries, err)
				}
				after, _ := json.Marshal(job.Snapshot)
				if string(before) != string(after) {
					t.Fatal("original snapshot changed")
				}
				// Below-budget subsequent calls do not invoke the compactor.
				job.Snapshot = snapshot
				if _, err := router(t.Context(), job); err != nil || summaries != 1 {
					t.Fatalf("unnecessary compaction: %v", err)
				}
			})
		}
	}
}

func TestPiMainCompactionRejectsInvalidResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*MainJob)
		result MainContextCompaction
		err    error
		panic  bool
		cancel bool
	}{
		{name: "empty", result: MainContextCompaction{KeepFrom: 2}},
		{name: "invalid-utf8", result: MainContextCompaction{Summary: string([]byte{255}), KeepFrom: 2}},
		{name: "oversized", result: MainContextCompaction{Summary: strings.Repeat("\\\"\n", 10000), KeepFrom: 2}},
		{name: "no-prefix", result: MainContextCompaction{Summary: "s", KeepFrom: 0}},
		{name: "no-tail", result: MainContextCompaction{Summary: "s", KeepFrom: 6}},
		{name: "split-turn", result: MainContextCompaction{Summary: "s", KeepFrom: 3}},
		{name: "split-tools", result: MainContextCompaction{Summary: "s", KeepFrom: 4}},
		{name: "unmatched", change: func(j *MainJob) { j.Snapshot.Messages[4].ToolResult.ToolCallID = "unknown" }, result: MainContextCompaction{Summary: "s", KeepFrom: 2}},
		{name: "pending", change: func(j *MainJob) { j.Snapshot.PendingToolCalls = []agent.PendingToolCall{{}} }, result: MainContextCompaction{Summary: "s", KeepFrom: 2}},

		{name: "incomplete-prefix", change: func(j *MainJob) { j.Snapshot.Messages[1].StopReason = agent.StopReasonLength }, result: MainContextCompaction{Summary: "s", KeepFrom: 2}},
		{name: "incomplete-tail", change: func(j *MainJob) { j.Snapshot.Messages[5].StopReason = agent.StopReasonLength }, result: MainContextCompaction{Summary: "s", KeepFrom: 2}},
		{name: "user-tail", change: func(j *MainJob) {
			j.Snapshot.Messages = append(j.Snapshot.Messages, agent.NewUserTextMessage("unfinished"))
		}, result: MainContextCompaction{Summary: "s", KeepFrom: 2}},
		{name: "system-only-prefix", change: func(j *MainJob) {
			j.Snapshot.Messages[0] = agent.NewSystemMessage(agent.SystemMessagePayload{Content: strings.Repeat("x", 60000)})
			j.Snapshot.Messages = append(j.Snapshot.Messages[:1], j.Snapshot.Messages[2:]...)
		}, result: MainContextCompaction{Summary: "s", KeepFrom: 1}},
		{name: "error", err: errors.New("summary unavailable")},
		{name: "panic", panic: true},
		{name: "cancel", cancel: true, result: MainContextCompaction{Summary: "s", KeepFrom: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := historyFixtureJob(t)
			job.Snapshot.Messages = compactionHistory()
			if test.change != nil {
				test.change(&job)
			}
			before, _ := json.Marshal(job.Snapshot)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			router := compactionRouter(t, false, false, agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
				calls++
				return offlineAnswer(`{"action":"ignore"}`), nil
			})}, 24<<10, func(context.Context, []agent.Message) (MainContextCompaction, error) {
				if test.panic {
					panic("boom")
				}
				if test.cancel {
					cancel()
				}
				return test.result, test.err
			})
			out, err := router(ctx, job)
			if err == nil || calls != 0 || !reflect.DeepEqual(out, agent.AgentSnapshot{}) {
				t.Fatalf("failed open: calls=%d err=%v", calls, err)
			}
			after, _ := json.Marshal(job.Snapshot)
			if string(before) != string(after) {
				t.Fatal("failure mutated snapshot")
			}
		})
	}
}

func TestPiMainCompactionModelFailureReturnsNoSnapshot(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		job := historyFixtureJob(t)
		job.Snapshot.Messages = compactionHistory()
		router := compactionRouter(t, false, false, agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
			if malformed {
				return offlineAnswer(`{"action":"unknown"}`), nil
			}
			return nil, errors.New("model unavailable")
		})}, 24<<10, func(context.Context, []agent.Message) (MainContextCompaction, error) {
			return MainContextCompaction{Summary: "summary", KeepFrom: 2}, nil
		})
		out, err := router(t.Context(), job)
		if err == nil || len(out.Messages) != 0 {
			t.Fatal("failed model returned compacted snapshot")
		}
	}
}

func TestPiMainCompactionRepeatedAndReopened(t *testing.T) {
	var summaries atomic.Int32
	compact := func(_ context.Context, messages []agent.Message) (MainContextCompaction, error) {
		summaries.Add(1)
		for i := len(messages) - 1; i > 0; i-- {
			if messages[i].Role == agent.RoleUser && messages[i-1].Role == agent.RoleAssistant {
				return MainContextCompaction{Summary: "Previous inputs were informational and ignored.", KeepFrom: i}, nil
			}
		}
		return MainContextCompaction{}, errors.New("no complete turn")
	}
	router, err := PiMainRouter(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		return offlineAnswer(`{"action":"ignore"}`), nil
	})}, 18<<10, compact)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	a, err := OpenMain(t.Context(), path, 1, router, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		submit(t, a, Trigger{ID: string(rune('a' + i)), Source: "im", Thread: "chat", Kind: "request", Text: strings.Repeat("x", 4000)})
		waitIdle(t, a)
	}
	if len(a.MainFailures()) != 0 || summaries.Load() < 2 {
		t.Fatalf("summaries=%d failures=%+v", summaries.Load(), a.MainFailures())
	}
	before, _, err := a.MainSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	a, err = OpenMain(t.Context(), path, 1, router, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	after, _, err := a.MainSnapshot()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("compacted snapshot did not survive reopen")
	}
	submit(t, a, Trigger{ID: "after-reopen", Source: "im", Thread: "chat", Kind: "request", Text: strings.Repeat("x", 4000)})
	waitIdle(t, a)
	if len(a.MainFailures()) != 0 {
		t.Fatal(a.MainFailures())
	}
}

func TestPiMainCompactionHistoryRecallBoundary(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete-pair", true: "overflow-after-tool"}[oversized], func(t *testing.T) {
			job := historyFixtureJob(t)
			job.Snapshot.Messages = compactionHistory()
			if oversized {
				job.History.nodes[historyKey{"report", "report"}] = &historyNode{data: strings.Repeat("\\\"\n<", 4000)}
				job.History.nodes[historyKey{"report", "report"}].seal()
			}
			summaries, calls := 0, 0
			router, err := PiMainHistoryRouter(agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
				calls++
				raw, _ := json.Marshal(agent.AgentContext{SystemPrompt: req.SystemPrompt, Messages: req.Messages, Tools: req.Tools})
				if len(raw) > 24<<10 {
					t.Fatal("actual model envelope exceeded budget")
				}
				if calls == 1 {
					return historyToolAnswer(HistoryRef{Domain: "main", Kind: "report", ID: "report"}, "", 16384), nil
				}
				if lastHistoryPage(t, req).Data == "" {
					t.Fatal("history read lost")
				}
				return offlineAnswer(`{"action":"ignore"}`), nil
			})}, 24<<10, func(context.Context, []agent.Message) (MainContextCompaction, error) {
				summaries++
				return MainContextCompaction{Summary: "summary", KeepFrom: 2}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			out, err := router(t.Context(), job)
			if summaries != 1 {
				t.Fatalf("compactor called %d times", summaries)
			}
			if oversized {
				if err == nil || calls != 1 || len(out.Snapshot.Messages) != 0 {
					t.Fatalf("overflow did not fail closed: calls=%d err=%v", calls, err)
				}
			} else {
				if err != nil || calls != 2 {
					t.Fatalf("recall failed: calls=%d err=%v", calls, err)
				}
				pairs := 0
				for _, m := range out.Snapshot.Messages {
					if m.ToolResult != nil {
						pairs++
					}
				}
				if pairs != 2 {
					t.Fatalf("lost complete history tool pairs: %d", pairs)
				}
			}
		})
	}
}

func TestPiMainCompactorDetachedJSONNumbers(t *testing.T) {
	messages := compactionHistory()
	messages[4].ToolResult.Details = map[string]any{"count": json.Number("9007199254740993")}
	_, err := prepareMainContext(t.Context(), messages, agent.NewUserTextMessage("current"), func(_ context.Context, detached []agent.Message) (MainContextCompaction, error) {
		if detached[4].ToolResult.Details.(map[string]any)["count"] != json.Number("9007199254740993") {
			t.Fatal("detachment rounded integer")
		}
		return MainContextCompaction{Summary: "summary", KeepFrom: 2}, nil
	}, func(messages []agent.Message) error { return checkMainContextBudget("host", messages, 24<<10) })
	if err != nil {
		t.Fatal(err)
	}
}
