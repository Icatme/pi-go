package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
	"github.com/Icatme/pi-go/executionreceipt"
)

func TestOfflinePiRoutesAndResumesAcrossSources(t *testing.T) {
	def := agent.AgentDefinition{Model: OfflineModel()}
	router, err := PiRouter(def)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := PiWorker(def)
	if err != nil {
		t.Fatal(err)
	}

	first := Trigger{ID: "1", Source: "chat", Thread: "a", Kind: "request", Text: "investigate backlog"}
	route, err := router(t.Context(), first, nil)
	if err != nil || route != (Route{Action: "new"}) {
		t.Fatalf("new task: %+v, %v", route, err)
	}
	start, err := worker(t.Context(), Job{TaskID: "T1", Trigger: first})
	if err != nil || start.Summary != "T1 (1): investigate backlog" {
		t.Fatalf("first turn: %+v, %v", start, err)
	}
	tasks := []TaskInfo{{ID: "T1", Summary: start.Summary}}
	follow := Trigger{ID: "2", Source: "monitor", Thread: "b", Kind: "signal", Text: "@T1 threshold exceeded"}
	route, err = router(t.Context(), follow, tasks)
	if err != nil || route != (Route{Action: "attach", TaskID: "T1"}) {
		t.Fatalf("attach: %+v, %v", route, err)
	}
	next, err := worker(t.Context(), Job{TaskID: route.TaskID, Trigger: follow, Snapshot: start.Snapshot})
	if err != nil || next.Summary != "T1 (2): @T1 threshold exceeded" {
		t.Fatalf("follow-up: %+v, %v", next, err)
	}
	if _, err := worker(t.Context(), Job{TaskID: "T2", Trigger: follow, Snapshot: start.Snapshot}); err == nil {
		t.Fatal("accepted a different task's conversation")
	}
	separate, err := worker(t.Context(), Job{TaskID: "T2", Trigger: first})
	if err != nil || separate.Summary != "T2 (1): investigate backlog" {
		t.Fatalf("separate task: %+v, %v", separate, err)
	}
	follow.Text = "@missing unknown task"
	route, err = router(t.Context(), follow, tasks)
	if err != nil || route != (Route{Action: "ignore"}) {
		t.Fatalf("unknown signal: %+v, %v", route, err)
	}
}

func TestPiRouterLimitsInputAndExecutionAuthority(t *testing.T) {
	tripwire := errors.New("router called host execution hook")
	router, err := PiRouter(agent.AgentDefinition{
		Tools: []agent.ToolDefinition{{Name: "host-write"}},
		ToolResolver: func(context.Context, agent.AgentSnapshot) ([]agent.ToolDefinition, error) {
			return nil, tripwire
		},
		PrepareRequest: func(context.Context, agent.PrepareRequestContext) (*agent.AgentLoopTurnUpdate, error) {
			return nil, tripwire
		},
		Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
			if len(req.Tools) != 0 {
				return nil, errors.New("router was given tools")
			}
			var input routeInput
			if err := json.Unmarshal([]byte(messageText(req.Messages[len(req.Messages)-1])), &input); err != nil {
				return nil, err
			}
			if len(input.Tasks) != 1 || input.Tasks[0].Summary != strings.Repeat("雪", 256) {
				return nil, errors.New("unexpected candidate view")
			}
			return offlineAnswer(`{"task_id":"T","action":"attach"}`), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := router(t.Context(), Trigger{Text: "follow"}, []TaskInfo{{ID: "T", Summary: strings.Repeat("雪", 300)}})
	if err != nil || decision != (Route{Action: "attach", TaskID: "T"}) {
		t.Fatalf("route: %+v, %v", decision, err)
	}
}

func TestPiRouterRejectsInvalidDecisions(t *testing.T) {
	for _, raw := range []string{
		`{"action":"attach","task_id":"unlisted"}`, `{"action":"new","task_id":""}`,
		`{"action":"ignore","task_id":"T"}`, `{"action":"attach"}`,
		`{"action":"ignore","action":"new"}`, `{"ACTION":"new"}`,
		`{"action":"new","instructions":""}`, `{"action":"new"} {}`,
		`{"action":"attach","task_id":null}`, `{"action":"unknown"}`, `[]`, `no decision`,
	} {
		t.Run(raw, func(t *testing.T) {
			router, err := PiRouter(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
				return offlineAnswer(raw), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := router(t.Context(), Trigger{Text: "hi"}, []TaskInfo{{ID: "T"}}); err == nil {
				t.Fatal("accepted invalid routing result")
			}
		})
	}
}

func TestPiWorkerRunsHostToolAndReportsOnlyFinalAssistantText(t *testing.T) {
	ran := false
	worker, err := PiWorker(agent.AgentDefinition{
		DefaultModel: agent.ModelRef{Provider: "example", Model: "fixture", ProviderConfig: agent.ProviderConfig{APIKey: "private-key"}},
		Tools: []agent.ToolDefinition{{Name: "inspect", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			ran = true
			return agent.ToolResult{Content: []agent.Part{agent.NewTextPart("private tool output")}}, nil
		}}},
		Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
			last := req.Messages[len(req.Messages)-1]
			var msg agent.Message
			if last.Role == agent.RoleUser {
				msg = agent.NewTextMessage(agent.RoleAssistant, "intermediate message")
				msg.ToolCalls = []agent.ToolCall{{ID: "1", Name: "inspect", Arguments: json.RawMessage(`{}`)}}
				msg.StopReason = agent.StopReasonToolUse
			} else if last.Role == agent.RoleTool {
				msg = agent.NewTextMessage(agent.RoleAssistant, "public report")
				msg.Parts = append(msg.Parts, agent.Part{Type: agent.PartTypeThinking, Text: "private reasoning"})
				msg.StopReason = agent.StopReasonStop
			} else {
				return nil, errors.New("unexpected input")
			}
			events := make(chan agent.AssistantEvent, 1)
			events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: msg}
			close(events)
			return &offlineStream{final: msg, events: events}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := worker(t.Context(), Job{TaskID: "T", Trigger: Trigger{Text: "please inspect"}})
	if err != nil || !ran || out.Summary != "public report" {
		t.Fatalf("ran=%v, report=%q, err=%v", ran, out.Summary, err)
	}
	b, err := json.Marshal(out.Snapshot)
	if err != nil || strings.Contains(string(b), "private-key") || !strings.Contains(string(b), "private tool output") {
		t.Fatalf("unexpected durable snapshot, err=%v", err)
	}
}

func piReply(msg agent.Message) agent.AssistantStream {
	events := make(chan agent.AssistantEvent, 1)
	events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: msg}
	close(events)
	return &offlineStream{final: msg, events: events}
}

func piScript(call agent.ToolCall, final agent.Message) agent.StreamModel {
	return agent.StreamFunc(func(ctx context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		if req.OnProviderStreamEvent != nil {
			if err := req.OnProviderStreamEvent(json.RawMessage(`{}`), req.Model); err != nil {
				return nil, err
			}
		}
		if call.Name != "" && req.Messages[len(req.Messages)-1].Role == agent.RoleUser {
			request := agent.NewTextMessage(agent.RoleAssistant, "intermediate")
			request.StopReason = agent.StopReasonToolUse
			request.ToolCalls = []agent.ToolCall{call}
			return piReply(request), nil
		}
		return piReply(final), nil
	})
}

func TestPiRoutingRequiresCompleteAuthorizedResponse(t *testing.T) {
	for _, tc := range []struct {
		name          string
		reason        agent.StopReason
		providerError string
		veto          bool
	}{
		{"truncated JSON that parses", agent.StopReasonLength, "", false},
		{"provider failure with normal stop", agent.StopReasonStop, "provider failed", false},
		{"host veto", agent.StopReasonStop, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := agent.NewTextMessage(agent.RoleAssistant, `{"action":"new"}`)
			message.StopReason, message.ErrorMessage = tc.reason, tc.providerError
			def := agent.AgentDefinition{Model: piScript(agent.ToolCall{}, message)}
			if tc.veto {
				def.OnProviderStreamEvent = func(json.RawMessage, agent.ModelRef) error { return errors.New("veto") }
			}
			route, err := PiRouter(def)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := route(t.Context(), Trigger{}, nil)
			if err == nil || decision != (Route{}) {
				t.Fatalf("accepted %+v, %v", decision, err)
			}
		})
	}
}

func TestPiWorkerKeepsPartialTextWhenModelReachesTokenLimit(t *testing.T) {
	msg := agent.NewTextMessage(agent.RoleAssistant, "partial answer")
	msg.StopReason = agent.StopReasonLength
	work, err := PiWorker(agent.AgentDefinition{Model: piScript(agent.ToolCall{}, msg)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := work(t.Context(), Job{TaskID: "T"})
	if err != nil || out.Status != "truncated" || out.Summary != "partial answer" || out.NeedsReview || len(out.Snapshot.Messages) == 0 {
		t.Fatalf("partial output: %+v err=%v", out, err)
	}
}

func TestPiWorkerPreservesToolFactsAcrossFinalization(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		terminate, toolFail, providerFail bool
		status                            string
	}{
		{"normal", false, false, false, "completed"},
		{"explicit terminal tool", true, false, false, "completed"},
		{"tool failed after commit", false, true, false, "completed_with_tool_errors"},
		{"provider failed after tool", false, false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := agent.NewTextMessage(agent.RoleAssistant, "model claims success")
			msg.StopReason = agent.StopReasonStop
			if tc.providerFail {
				msg.ErrorMessage = "private provider failure"
			}
			tool := agent.ToolDefinition{Name: "publish", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
				if err := executionreceipt.RequireCapture(ctx); err != nil {
					return agent.ToolResult{}, err
				}
				if !executionreceipt.ReportLocalCommit(ctx) {
					return agent.ToolResult{}, errors.New("commit report lost")
				}
				if tc.toolFail {
					return agent.ToolResult{}, errors.New("private failure after publication")
				}
				return agent.ToolResult{Content: []agent.Part{agent.NewTextPart("private tool payload")}, Terminate: tc.terminate}, nil
			}}
			work, err := PiWorker(agent.AgentDefinition{Model: piScript(agent.ToolCall{ID: "publish-1", Name: tool.Name, Arguments: json.RawMessage(`{}`)}, msg), Tools: []agent.ToolDefinition{tool}})
			if err != nil {
				t.Fatal(err)
			}
			out, err := work(t.Context(), Job{TaskID: "T"})
			if (err != nil) != tc.providerFail || out.Status != tc.status || out.NeedsReview != (tc.toolFail || tc.providerFail) {
				t.Fatalf("status=%q review=%v err=%v", out.Status, out.NeedsReview, err)
			}
			if tc.providerFail && !strings.Contains(err.Error(), msg.ErrorMessage) {
				t.Fatalf("trusted failure lost provider cause: %v", err)
			}
			if len(out.Receipts) != 1 {
				t.Fatalf("receipts %+v", out.Receipts)
			}
			fact := out.Receipts[0]
			if fact.Domain != "agent" || fact.CallID != "publish-1" || fact.ToolName != tool.Name || fact.Token == 0 || fact.ParentToken != 0 || !fact.Committed || !fact.Completed {
				t.Fatal(fact)
			}
			if strings.Contains(out.Summary, "private") || ((tc.toolFail || tc.providerFail || tc.terminate) && strings.Contains(out.Summary, "model claims success")) {
				t.Fatalf("unsafe report %q", out.Summary)
			}
		})
	}
}

func TestPiWorkerHonorsFinishTurnEndAfterToolBatch(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		fail         bool
	}{
		{"success", "completed", false}, {"one failure", "completed_with_tool_errors", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const private = "private tool data"
			tool := agent.ToolDefinition{Name: "inspect", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
				if tc.fail {
					return agent.ToolResult{}, errors.New(private)
				}
				return agent.ToolResult{Content: []agent.Part{agent.NewTextPart(private)}}, nil
			}}
			work, err := PiWorker(agent.AgentDefinition{
				Tools: []agent.ToolDefinition{tool},
				Model: piScript(agent.ToolCall{ID: "x", Name: tool.Name, Arguments: json.RawMessage(`{}`)}, agent.Message{}),
				FinishTurn: func(context.Context, agent.AgentTurnContext) (agent.AgentTurnDecision, error) {
					return agent.AgentTurnDecision{Action: agent.TurnActionEnd}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			out, err := work(t.Context(), Job{TaskID: "T"})
			if err != nil || out.Status != tc.status || out.Summary == "" || strings.Contains(out.Summary, private) || out.NeedsReview != tc.fail || len(out.Receipts) != 1 || !out.Receipts[0].Completed {
				t.Fatalf("status=%q review=%v receipts=%v summary=%q err=%v", out.Status, out.NeedsReview, out.Receipts, out.Summary, err)
			}
		})
	}
}

func TestPiWorkerWaitsForLateChildReceiptOnCancellation(t *testing.T) {
	sandbox, err := codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	leaf := agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		close(entered)
		<-release
		if !executionreceipt.ReportLocalCommit(ctx) {
			return agent.ToolResult{}, errors.New("late commit lost")
		}
		return agent.ToolResult{}, nil
	}}
	tool, err := codemodetool.New(sandbox, []codemodetool.Binding{codemodetool.Native(leaf, "test")}, codemodetool.Options{})
	if err != nil {
		t.Fatal(err)
	}
	msg := agent.NewTextMessage(agent.RoleAssistant, "done")
	msg.StopReason = agent.StopReasonStop
	work, err := PiWorker(agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, Model: piScript(agent.ToolCall{ID: "outer", Name: tool.Name, ParsedArgs: map[string]any{"code": "await tools.write({});"}}, msg)})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		out Outcome
		err error
	}
	done := make(chan result, 1)
	a, err := Open(t.Context(), filepath.Join(t.TempDir(), "inbox.jsonl"), 1, func(context.Context, Trigger, []TaskInfo) (Route, error) {
		return Route{Action: "new"}, nil
	}, func(ctx context.Context, job Job) (Outcome, error) {
		out, err := work(ctx, job)
		done <- result{out, err}
		return out, err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { releaseOnce(); a.Close() }()
	submit(t, a, Trigger{ID: "abort-me", Source: "chat", Thread: "thread", Kind: "request", Text: "call tool"})
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("child did not enter")
	}
	if err := a.Abort(RunTarget{TaskID: "task-1", Source: "chat", EventID: "abort-me"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("worker released the task before child exit")
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce()
	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) || !r.out.NeedsReview || len(r.out.Receipts) != 3 {
			t.Fatalf("out=%+v err=%v", r.out, r.err)
		}
		found := false
		for _, f := range r.out.Receipts {
			if !f.Completed {
				t.Fatalf("unsettled receipt: %+v", f)
			}
			if f.Domain == "agent" && f.ToolName == "write" && f.ParentToken != 0 && f.Committed {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing late commit: %+v", r.out.Receipts)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not return after child exit")
	}
}

func TestPiWorkerSteersAtNativeBoundariesAndQueuesOrdinarySubmit(t *testing.T) {
	for _, mode := range []agent.QueueMode{"", agent.QueueModeAll} {
		t.Run("mode_"+string(mode), func(t *testing.T) {
			type request struct {
				texts   []string
				release chan struct{}
			}
			requests := make(chan request, 8)
			started := make(chan string, 4)
			model := agent.StreamFunc(func(ctx context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
				var users []string
				for _, msg := range req.Messages {
					if msg.Role == agent.RoleUser {
						users = append(users, messageText(msg))
					}
				}
				r := request{users, make(chan struct{})}
				select {
				case requests <- r:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				select {
				case <-r.release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return offlineAnswer("done"), nil
			})
			work, err := PiWorker(agent.AgentDefinition{Model: model, SteeringMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			a, err := Open(t.Context(), filepath.Join(t.TempDir(), "inbox.jsonl"), 1, func(context.Context, Trigger, []TaskInfo) (Route, error) {
				return Route{Action: "new"}, nil
			}, func(ctx context.Context, job Job) (Outcome, error) {
				started <- job.Trigger.ID
				return work(ctx, job)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "first", Source: "chat", Thread: "thread", Kind: "request", Text: "initial"})
			if got := receive(t, started); got != "first" {
				t.Fatal(got)
			}
			first := receive(t, requests)
			target := RunTarget{TaskID: "task-1", Source: "chat", EventID: "first"}
			var receipts []*SteerReceipt
			for _, instruction := range []string{"steer one", "steer two"} {
				r, err := a.Steer(target, instruction)
				if err != nil {
					t.Fatal(err)
				}
				receipts = append(receipts, r)
			}
			submit(t, a, Trigger{ID: "later", Source: "chat", Thread: "thread", Kind: "request", Text: "ordinary follow-up"})
			close(first.release)
			second := receive(t, requests)
			want := []string{"steer one"}
			if mode == agent.QueueModeAll {
				want = append(want, "steer two")
			}
			if len(second.texts) < 1 || !slices.Equal(second.texts[1:], want) {
				t.Fatalf("native second request users=%v", second.texts)
			}
			tail := second
			if mode != agent.QueueModeAll {
				close(second.release)
				tail = receive(t, requests)
				if len(tail.texts) != 3 || !slices.Equal(tail.texts[1:], []string{"steer one", "steer two"}) {
					t.Fatalf("native third request users=%v", tail.texts)
				}
			}
			for _, r := range receipts {
				if ok, err := r.Wait(t.Context()); err != nil || !ok {
					t.Fatalf("steering receipt: %v, %v", ok, err)
				}
			}
			select {
			case id := <-started:
				t.Fatalf("ordinary job %s already entered active run", id)
			default:
			}
			close(tail.release)
			if got := receive(t, started); got != "later" {
				t.Fatal(got)
			}
			later := receive(t, requests)
			if len(later.texts) != 4 || !strings.Contains(later.texts[3], "ordinary follow-up") {
				t.Fatalf("later job users=%v", later.texts)
			}
			close(later.release)
			waitIdle(t, a)
			if got := len(a.Status().Reports); got != 2 {
				t.Fatalf("steering created additional public reports: %d", got)
			}
		})
	}
}

func TestPiProgressPublishesOnlyNativePhaseNames(t *testing.T) {
	const private = "secret_host_tool"
	msg := agent.NewTextMessage(agent.RoleAssistant, "private final text")
	msg.StopReason = agent.StopReasonStop
	work, err := PiWorker(agent.AgentDefinition{
		Model: piScript(agent.ToolCall{ID: "private-call", Name: private, Arguments: json.RawMessage(`{}`)}, msg),
		Tools: []agent.ToolDefinition{{Name: private, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			return agent.ToolResult{Content: []agent.Part{agent.NewTextPart("private result")}}, nil
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var phases []string
	_, err = work(t.Context(), Job{TaskID: "T", NotifyProgress: func(kind string) { phases = append(phases, kind) }})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(phases, []string{"turn_start", "tool_start", "tool_end", "turn_end", "turn_start", "turn_end"}) {
		t.Fatalf("progress %v", phases)
	}
}
