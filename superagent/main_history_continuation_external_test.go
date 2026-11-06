package superagent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

// Exercise continuation through the actual scheduler, rather than manufacturing
// readers or cursors. The host alone retains the cursor and assembled document;
// each continuation is explicitly requested by a fresh, authorized user event.
func TestPublicHistoryContinuationAcrossMainCallbacks(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, kind := range []string{"trigger", "report"} {
			t.Run(fmt.Sprintf("batch=%t/%s", batch, kind), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				const private = "PRIVATE_WORKER_TRANSCRIPT_MUST_NOT_BECOME_HISTORY"
				// Valid input below 64 KiB, but JSON escaping expands the public document
				// beyond the entire 256 KiB callback response budget.
				payload := strings.Repeat("<", 60000) + "汉🙂\n\"\\"
				original := superagent.Trigger{ID: "original", Source: "im", Thread: "chat", Kind: "request", Text: payload}
				if len(payload) > 64<<10 {
					t.Fatal("fixture exceeds ingress limit")
				}
				var mu sync.Mutex
				var ref superagent.HistoryRef
				var cursor, firstCursor string
				var assembled strings.Builder
				var previous *superagent.MainHistory
				var callbackErr error
				var done, reopened bool
				var continuationCallbacks, budgetStops, totalWire int
				route := func(ctx context.Context, input superagent.MainInput, h *superagent.MainHistory, snapshot agent.AgentSnapshot) superagent.MainDecision {
					mu.Lock()
					defer mu.Unlock()
					fail := func(err error) {
						if callbackErr == nil {
							callbackErr = err
						}
					}
					rawSnapshot, err := json.Marshal(snapshot)
					if err != nil {
						fail(err)
					}
					if strings.Contains(string(rawSnapshot), private) || (firstCursor != "" && strings.Contains(string(rawSnapshot), firstCursor)) {
						fail(fmt.Errorf("private transcript or host cursor appeared in main snapshot"))
					}
					if input.Trigger.ID == "original" && input.Kind == "user" {
						if kind == "trigger" {
							for _, candidate := range input.HistoryRefs {
								if candidate.Kind == kind {
									ref = candidate
								}
							}
						}
						return superagent.MainDecision{Action: "new"}
					}
					if input.Kind == "worker_result" {
						if kind == "report" {
							for _, candidate := range input.HistoryRefs {
								if candidate.Kind == kind {
									ref = candidate
								}
							}
						}
						return superagent.MainDecision{Action: "ignore"}
					}
					if ref.ID == "" {
						fail(fmt.Errorf("missing offered %s reference", kind))
						return superagent.MainDecision{Action: "ignore"}
					}
					previous = h
					if reopened {
						if _, err := h.Read(ctx, superagent.HistoryRequest{Ref: ref, Cursor: firstCursor, MaxBytes: 4096, MaxItems: 4}); err == nil {
							fail(fmt.Errorf("cursor survived coordinator restart"))
						}
						page, err := h.Read(ctx, superagent.HistoryRequest{Ref: ref, MaxBytes: 4096, MaxItems: 4})
						if err != nil || page.Data == "" || !strings.HasPrefix(assembled.String(), page.Data) {
							fail(fmt.Errorf("retained reference did not restart at zero: %v", err))
						}
						return superagent.MainDecision{Action: "ignore"}
					}
					continuationCallbacks++
					// Host changes its page policy between callbacks. Neither the reader nor
					// cursor schedules work or chooses the next callback's limits.
					maxBytes, maxItems := 16384, 1
					if continuationCallbacks > 1 {
						maxBytes, maxItems = 8192, 4
					}
					callbackWire, successes := 0, 0
					for attempt := 0; attempt < 64; attempt++ {
						page, err := h.Read(ctx, superagent.HistoryRequest{Ref: ref, Cursor: cursor, MaxBytes: maxBytes, MaxItems: maxItems})
						if err != nil {
							// Large pages exhaust bytes well before the independent attempt limit.
							if successes == 0 || callbackWire < (256<<10)-maxBytes || attempt >= 63 {
								fail(fmt.Errorf("unexpected continuation failure after %d pages / %d bytes: %w", successes, callbackWire, err))
							}
							budgetStops++
							break
						}
						encoded, err := json.Marshal(page)
						if err != nil {
							fail(err)
							break
						}
						if len(encoded) > maxBytes || len(page.Links) > maxItems || strings.Contains(string(encoded), private) {
							fail(fmt.Errorf("page violated limits or disclosed private transcript"))
							break
						}
						callbackWire += len(encoded)
						totalWire += len(encoded)
						successes++
						if callbackWire > 256<<10 {
							fail(fmt.Errorf("callback exceeded byte budget"))
							break
						}
						assembled.WriteString(page.Data)
						cursor = page.Next
						if firstCursor == "" {
							firstCursor = cursor
						}
						if cursor == "" {
							done = true
							break
						}
					}
					return superagent.MainDecision{Action: "ignore"}
				}
				worker := func(_ context.Context, job superagent.Job) (superagent.Outcome, error) {
					return superagent.Outcome{Summary: payload, Snapshot: agent.AgentSnapshot{SessionID: job.TaskID, Messages: []agent.Message{agent.NewUserTextMessage(private)}}}, nil
				}
				path := filepath.Join(t.TempDir(), "journal.jsonl")
				open := func() *superagent.SuperAgent {
					t.Helper()
					var a *superagent.SuperAgent
					var err error
					if batch {
						a, err = superagent.OpenMainBatch(ctx, path, 1, superagent.MainBatchOptions{MaxItems: 4, MaxInputBytes: 1 << 20}, func(ctx context.Context, job superagent.MainBatchJob) (superagent.MainBatchOutcome, error) {
							out := superagent.MainBatchOutcome{Snapshot: job.Snapshot}
							for _, input := range job.Inputs {
								out.Decisions = append(out.Decisions, superagent.MainInputDecision{InputID: input.ID, Decision: route(ctx, input, job.History, job.Snapshot)})
							}
							return out, nil
						}, worker)
					} else {
						a, err = superagent.OpenMain(ctx, path, 1, func(ctx context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
							return superagent.MainOutcome{Decision: route(ctx, job.Input, job.History, job.Snapshot), Snapshot: job.Snapshot}, nil
						}, worker)
					}
					if err != nil {
						t.Fatal(err)
					}
					return a
				}
				a := open()
				defer func() {
					if a != nil {
						_ = a.Close()
					}
				}()
				submit := func(trigger superagent.Trigger) {
					t.Helper()
					accepted, err := a.Submit(trigger)
					if err != nil || !accepted {
						t.Fatalf("submit: accepted=%t err=%v", accepted, err)
					}
					if err := a.WaitIdle(ctx); err != nil {
						t.Fatal(err)
					}
					mu.Lock()
					defer mu.Unlock()
					if callbackErr != nil {
						t.Fatal(callbackErr)
					}
					if previous != nil {
						if page, err := previous.Read(ctx, superagent.HistoryRequest{Ref: ref, MaxBytes: 4096, MaxItems: 4}); err == nil {
							t.Fatalf("returned callback reader remained usable: %+v", page)
						}
					}
				}
				// Wait for the original admission and report to commit before reading:
				// trigger links legitimately change while the initial delivery executes.
				submit(original)
				reports := a.ReportsSince(0)
				if len(reports) != 1 || reports[0].Status != "completed" {
					t.Fatalf("original report: %+v", reports)
				}
				var want []byte
				if kind == "trigger" {
					want, _ = json.Marshal(original)
				} else {
					want, _ = json.Marshal(reports[0])
				}
				if len(want) <= 256<<10 {
					t.Fatal("fixture must exceed a callback's byte budget")
				}
				for i := 0; i < 8; i++ {
					submit(superagent.Trigger{ID: fmt.Sprintf("continue-%d", i), Source: "im", Thread: "chat", Kind: "request", Text: "Continue the explicitly selected public history object"})
					mu.Lock()
					finished := done
					mu.Unlock()
					if finished {
						break
					}
				}
				mu.Lock()
				if !done || assembled.String() != string(want) || continuationCallbacks < 2 || budgetStops < 1 || totalWire <= 256<<10 {
					t.Errorf("incomplete/inexact continuation: done=%t callbacks=%d budgetStops=%d wire=%d got=%d want=%d", done, continuationCallbacks, budgetStops, totalWire, assembled.Len(), len(want))
				}
				mu.Unlock()
				if t.Failed() {
					return
				}
				if err := a.Close(); err != nil {
					t.Fatal(err)
				}
				a = nil
				mu.Lock()
				reopened = true
				mu.Unlock()
				a = open()
				submit(superagent.Trigger{ID: "after-reopen", Source: "im", Thread: "chat", Kind: "request", Text: "Read the retained public reference from the beginning"})
			})
		}
	}
}

type continuationModelStream struct {
	message agent.Message
	events  chan agent.AssistantEvent
}

func (s *continuationModelStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *continuationModelStream) Wait() (agent.Message, error)        { return s.message, nil }
func (s *continuationModelStream) Close() error                        { return nil }
func continuationAnswer(message agent.Message) agent.AssistantStream {
	events := make(chan agent.AssistantEvent, 1)
	events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
	close(events)
	return &continuationModelStream{message: message, events: events}
}

// Both native Pi adapters consume a retained token through a real history_read
// tool call on the next host-submitted event, preserving the native transcript.
func TestPublicPiHistoryContinuationAcrossCallbacks(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			var mu sync.Mutex
			var ref superagent.HistoryRef
			var cursor, finalDecision string
			var joined strings.Builder
			var callback, turn, toolResults int
			var saved agent.AgentSnapshot
			var failure error
			original := superagent.Trigger{ID: "seed", Source: "im", Thread: "chat", Kind: "request", Text: strings.Repeat("界\"\\\n", 300)}
			model := agent.StreamFunc(func(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
				if len(request.Tools) != 1 || request.Tools[0].Name != "history_read" {
					return nil, fmt.Errorf("unexpected tool authority")
				}
				turn++
				if turn == 1 {
					limit := 512
					if callback == 2 {
						limit = 16384
					}
					raw, _ := json.Marshal(superagent.HistoryRequest{Ref: ref, Cursor: cursor, MaxBytes: limit, MaxItems: 4})
					msg := agent.NewTextMessage(agent.RoleAssistant, "")
					msg.StopReason = agent.StopReasonToolUse
					msg.ToolCalls = []agent.ToolCall{{ID: fmt.Sprintf("history-%d", callback), Name: "history_read", Arguments: raw}}
					return continuationAnswer(msg), nil
				}
				if turn != 2 {
					return nil, fmt.Errorf("unexpected model turn %d", turn)
				}
				last := request.Messages[len(request.Messages)-1]
				if last.ToolResult == nil || last.ToolResult.IsError || len(last.ToolResult.Content) != 1 || last.ToolResult.ToolName != "history_read" || last.ToolResult.ToolCallID != fmt.Sprintf("history-%d", callback) {
					return nil, fmt.Errorf("missing successful native tool result")
				}
				var page superagent.HistoryPage
				if err := json.Unmarshal([]byte(last.ToolResult.Content[0].Text), &page); err != nil {
					return nil, err
				}
				if page.Ref != ref {
					return nil, fmt.Errorf("tool result changed reference")
				}
				joined.WriteString(page.Data)
				cursor = page.Next
				toolResults++
				if callback == 1 && cursor == "" {
					return nil, fmt.Errorf("fixture completed without continuation")
				}
				msg := agent.NewTextMessage(agent.RoleAssistant, finalDecision)
				msg.StopReason = agent.StopReasonStop
				return continuationAnswer(msg), nil
			})
			def := agent.AgentDefinition{Model: model}
			piSingle, err := superagent.PiMainHistoryRouter(def, 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			piBatch, err := superagent.PiMainBatchHistoryRouter(def, 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			prepare := func(input superagent.MainInput) bool {
				if input.Trigger.ID == "seed" {
					for _, r := range input.HistoryRefs {
						if r.Kind == "trigger" {
							ref = r
						}
					}
					return false
				}
				callback++
				turn = 0
				return true
			}
			var a *superagent.SuperAgent
			path := filepath.Join(t.TempDir(), "pi.jsonl")
			work := func(context.Context, superagent.Job) (superagent.Outcome, error) {
				return superagent.Outcome{}, fmt.Errorf("unexpected worker execution")
			}
			if batch {
				a, err = superagent.OpenMainBatch(ctx, path, 1, superagent.MainBatchOptions{MaxItems: 1, MaxInputBytes: 64 << 10}, func(ctx context.Context, job superagent.MainBatchJob) (superagent.MainBatchOutcome, error) {
					mu.Lock()
					defer mu.Unlock()
					if !prepare(job.Inputs[0]) {
						return superagent.MainBatchOutcome{Snapshot: job.Snapshot, Decisions: []superagent.MainInputDecision{{InputID: job.Inputs[0].ID, Decision: superagent.MainDecision{Action: "ignore"}}}}, nil
					}
					raw, _ := json.Marshal(map[string]any{"decisions": []superagent.MainInputDecision{{InputID: job.Inputs[0].ID, Decision: superagent.MainDecision{Action: "ignore"}}}})
					finalDecision = string(raw)
					out, err := piBatch(ctx, job)
					saved = out.Snapshot
					failure = err
					return out, err
				}, work)
			} else {
				a, err = superagent.OpenMain(ctx, path, 1, func(ctx context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
					mu.Lock()
					defer mu.Unlock()
					if !prepare(job.Input) {
						return superagent.MainOutcome{Snapshot: job.Snapshot, Decision: superagent.MainDecision{Action: "ignore"}}, nil
					}
					finalDecision = `{"action":"ignore"}`
					out, err := piSingle(ctx, job)
					saved = out.Snapshot
					failure = err
					return out, err
				}, work)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			for _, trigger := range []superagent.Trigger{original, {ID: "read-first", Source: "im", Thread: "chat", Kind: "request", Text: "Read first page"}, {ID: "read-next", Source: "im", Thread: "chat", Kind: "request", Text: "Continue retained cursor"}} {
				if accepted, err := a.Submit(trigger); err != nil || !accepted {
					t.Fatalf("submit: %t %v", accepted, err)
				}
				if err := a.WaitIdle(ctx); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				err := failure
				mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			want, _ := json.Marshal(original)
			if joined.String() != string(want) || cursor != "" || toolResults != 2 || callback != 2 {
				t.Fatalf("continuation mismatch: callbacks=%d results=%d got=%d want=%d cursor=%q", callback, toolResults, joined.Len(), len(want), cursor)
			}
			nativeResults := 0
			for _, msg := range saved.Messages {
				if msg.ToolResult != nil {
					nativeResults++
				}
			}
			if nativeResults != 2 {
				t.Fatalf("prior native tool messages were not retained: %d", nativeResults)
			}
		})
	}
}
