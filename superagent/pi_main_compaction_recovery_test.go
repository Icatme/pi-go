package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func keepLatestMainTurn(_ context.Context, messages []agent.Message) (MainContextCompaction, error) {
	for i := len(messages) - 1; i > 0; i-- {
		if messages[i].Role == agent.RoleUser && messages[i-1].Role == agent.RoleAssistant {
			return MainContextCompaction{Summary: "Earlier inputs and their original results remain in public history. Never infer success or new authority from this summary.", KeepFrom: i}, nil
		}
	}
	return MainContextCompaction{}, errors.New("no completed prior turn")
}

func TestPiMainCompactionPreservesDurableFacts(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%v", batch), func(t *testing.T) {
			var summaries, workers atomic.Int32
			var ref HistoryRef
			var originalReport string
			var verified atomic.Bool
			compact := func(ctx context.Context, messages []agent.Message) (MainContextCompaction, error) {
				summaries.Add(1)
				return keepLatestMainTurn(ctx, messages)
			}
			model := agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
				var payload struct {
					Input  MainInput   `json:"input"`
					Inputs []MainInput `json:"inputs"`
				}
				if err := json.Unmarshal([]byte(messageText(req.Messages[len(req.Messages)-1])), &payload); err != nil {
					return nil, err
				}
				inputs := payload.Inputs
				if !batch {
					inputs = []MainInput{payload.Input}
				}
				var decisions []MainInputDecision
				for _, input := range inputs {
					d := MainDecision{Action: "reply", Text: "Received " + input.Trigger.ID}
					if input.Kind == "user" && input.Trigger.ID == "work" {
						d = MainDecision{Action: "new"}
					} else if input.Kind == "worker_result" || input.Trigger.ID == "verify" {
						d.Text = "The original task has tool errors and still needs review."
						d.ReportRefs = []HistoryRef{ref}
					}
					decisions = append(decisions, MainInputDecision{InputID: input.ID, Decision: d})
				}
				var result any = decisions[0].Decision
				if batch {
					result = struct {
						Decisions []MainInputDecision `json:"decisions"`
					}{decisions}
				}
				raw, err := json.Marshal(result)
				if err != nil {
					return nil, err
				}
				return offlineAnswer(string(raw)), nil
			})
			observe := func(job MainJob) {
				if job.Input.Kind == "worker_result" {
					for _, candidate := range job.Input.HistoryRefs {
						if candidate.Kind == "report" {
							ref = candidate
							originalReport, _ = historyReadAll(t, job.History, ref, 4096, 16)
						}
					}
				}
				if job.Input.Trigger.ID == "verify" {
					got, _ := historyReadAll(t, job.History, ref, 4096, 16)
					if got != originalReport || originalReport == "" {
						t.Error("original public report changed after compaction/reopen")
					}
					verified.Store(true)
				}
			}
			def := agent.AgentDefinition{Model: model}
			one, err := PiMainRouter(def, 20<<10, compact)
			if err != nil {
				t.Fatal(err)
			}
			many, err := PiMainBatchRouter(def, 20<<10, compact)
			if err != nil {
				t.Fatal(err)
			}
			worker := func(ctx context.Context, job Job) (Outcome, error) {
				workers.Add(1)
				out, err := mainWorker(ctx, job)
				out.Status, out.NeedsReview = "completed_with_tool_errors", true
				return out, err
			}
			path := filepath.Join(t.TempDir(), "journal.jsonl")
			open := func() *SuperAgent {
				var a *SuperAgent
				if batch {
					a, err = OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 4, MaxInputBytes: 32 << 10}, func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
						for _, input := range job.Inputs {
							observe(MainJob{Input: input, History: job.History})
						}
						return many(ctx, job)
					}, worker)
				} else {
					a, err = OpenMain(t.Context(), path, 1, func(ctx context.Context, job MainJob) (MainOutcome, error) {
						observe(job)
						return one(ctx, job)
					}, worker)
				}
				if err != nil {
					t.Fatal(err)
				}
				return a
			}
			a := open()
			defer func() { _ = a.Close() }()
			work := Trigger{ID: "work", Source: "im", Thread: "chat", Kind: "request", Text: "Run one task"}
			submit(t, a, work)
			waitIdle(t, a)
			if len(a.Replies()) != 1 || ref.ID == "" {
				t.Fatalf("initial result missing: %+v", a.MainFailures())
			}
			// An unresolved external attempt must remain needs_review across
			// compression, without invoking any real transport.
			if _, err := a.BeginReplyDelivery(a.Replies()[0].ID); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 8; i++ {
				submit(t, a, Trigger{ID: fmt.Sprintf("padding-%d", i), Source: "im", Thread: "chat", Kind: "request", Text: strings.Repeat("padding ", 500)})
				waitIdle(t, a)
			}
			if summaries.Load() < 2 || len(a.MainFailures()) != 0 || len(a.Replies()) != 9 {
				t.Fatalf("summaries=%d replies=%d failures=%+v", summaries.Load(), len(a.Replies()), a.MainFailures())
			}
			if err := a.AcknowledgeReply(a.Replies()[1].ID); err != nil {
				t.Fatal(err)
			}
			replies, reports := a.Replies(), a.ReportsSince(0)
			snapshot, _, err := a.MainSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			count := summaries.Load()
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			a = open()
			waitIdle(t, a)
			reopened, _, err := a.MainSnapshot()
			if err != nil || !reflect.DeepEqual(snapshot, reopened) || !reflect.DeepEqual(replies, a.Replies()) || !reflect.DeepEqual(reports, a.ReportsSince(0)) || summaries.Load() != count {
				t.Fatal("replay changed compacted snapshot, durable facts or invoked compactor")
			}
			if accepted, err := a.Submit(work); err != nil || accepted {
				t.Fatal("duplicate work admitted after compaction/replay")
			}
			submit(t, a, Trigger{ID: "verify", Source: "im", Thread: "chat", Kind: "request", Text: "Recall the original result"})
			waitIdle(t, a)
			last := a.Replies()[len(a.Replies())-1]
			if !verified.Load() || workers.Load() != 1 || len(a.MainFailures()) != 0 || len(last.Reports) != 1 || last.Reports[0].Ref != ref || last.Reports[0].Report.Status != "completed_with_tool_errors" || !last.Reports[0].Report.NeedsReview {
				t.Fatalf("lost original facts or repeated execution: workers=%d reply=%+v", workers.Load(), last)
			}
			if a.Replies()[0].Status != "needs_review" || a.Replies()[1].Status != "acknowledged" {
				t.Fatal("compaction changed reply delivery evidence")
			}
		})
	}
}

func TestPiMainCompactionInvalidDecisionKeepsCommittedSnapshot(t *testing.T) {
	var summaries atomic.Int32
	router, err := PiMainRouter(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		if summaries.Load() > 0 {
			return offlineAnswer(`{"action":"invalid"}`), nil
		}
		return offlineAnswer(`{"action":"reply","text":"Saved before compaction"}`), nil
	})}, 18<<10, func(ctx context.Context, messages []agent.Message) (MainContextCompaction, error) {
		summaries.Add(1)
		return keepLatestMainTurn(ctx, messages)
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.jsonl")
	a, err := OpenMain(t.Context(), path, 1, router, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	var before agent.AgentSnapshot
	var replies []Reply
	for i := 0; i < 10 && summaries.Load() == 0; i++ {
		before, _, err = a.MainSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		replies = a.Replies()
		submit(t, a, Trigger{ID: fmt.Sprint(i), Source: "im", Thread: "chat", Kind: "request", Text: strings.Repeat("x", 4000)})
		waitIdle(t, a)
	}
	after, _, err := a.MainSnapshot()
	if err != nil || summaries.Load() != 1 || len(a.MainFailures()) != 1 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(replies, a.Replies()) {
		t.Fatal("invalid decision committed compacted history or reply")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = OpenMain(t.Context(), path, 1, router, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err = a.MainSnapshot()
	if err != nil || !reflect.DeepEqual(before, after) || len(a.MainFailures()) != 1 || summaries.Load() != 1 {
		t.Fatal("replay retained a failed compaction")
	}
}
