package exectools_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/agent/exectools"
	"github.com/Icatme/pi-go/codemode"
	"github.com/Icatme/pi-go/executionreceipt"
)

func TestExplicitCommitSurvivesExecutorAndFinalizationFailures(t *testing.T) {
	for _, kind := range []string{"command", "python"} {
		for _, mode := range []string{"success", "nonzero", "backend error", "backend panic", "after error", "after panic", "result validation", "cancel"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				cause := errors.New("post-commit fixture failure")
				var backendCtx context.Context
				var lateWriter io.Writer
				execute := func(ctx context.Context, stdout io.Writer) (int, error) {
					backendCtx, lateWriter = ctx, stdout
					if err := executionreceipt.RequireCapture(ctx); err != nil {
						return -1, err
					}
					if !executionreceipt.ReportLocalCommit(ctx) {
						return -1, errors.New("commit report rejected")
					}
					io.WriteString(stdout, "committed")
					switch mode {
					case "nonzero":
						return 7, nil
					case "backend error":
						return -1, cause
					case "backend panic":
						panic(cause)
					case "cancel":
						cancel()
						return -1, ctx.Err()
					default:
						return 0, nil
					}
				}
				args := map[string]any{"args": []string{}}
				tool := commandTool(t, func(ctx context.Context, _ []string, stdout, _ io.Writer) (int, error) { return execute(ctx, stdout) }, exectools.Options{})
				if kind == "python" {
					tool = pythonTool(t, func(ctx context.Context, _ string, stdout, _ io.Writer) (int, error) { return execute(ctx, stdout) }, exectools.Options{})
					args = map[string]any{"code": "pass"}
				}
				if mode == "result validation" {
					tool.ValidateResult = func(agent.ToolResult) error { return cause }
				}
				options := agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}
				if mode == "after error" || mode == "after panic" {
					options.AfterToolCall = func(context.Context, agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
						if mode == "after panic" {
							panic(cause)
						}
						return agent.AfterToolCallResult{}, cause
					}
				}
				out, h := receiptCallOptions(t, ctx, agent.ToolCall{ID: "committing-call", Name: tool.Name, ParsedArgs: args}, options, 1)
				if mode == "success" || mode == "nonzero" {
					value := decodedOutput(t, out)
					if value.Stdout != "committed" || out.IsError != (mode == "nonzero") {
						t.Fatalf("outcome=%+v value=%+v", out, value)
					}
				} else if out.Err == nil || !out.IsError {
					t.Fatalf("expected failed outcome: %+v", out)
				}
				if err := h.Wait(t.Context()); err != nil {
					t.Fatal(err)
				}
				facts := h.Snapshot()
				if len(facts) != 1 || facts[0].Token() == 0 || facts[0].ParentToken() != 0 || facts[0].CallID() != "committing-call" || facts[0].ToolName() != tool.Name || facts[0].Domain() != "agent" || !facts[0].Committed() || !facts[0].Completed() {
					t.Fatalf("commit lost: %+v", facts)
				}
				if backendCtx == nil || executionreceipt.RequireCapture(backendCtx) == nil || executionreceipt.ReportLocalCommit(backendCtx) {
					t.Fatal("late receipt capture still active")
				}
				if n, err := lateWriter.Write([]byte("late")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("late writer after %s: n=%d err=%v", mode, n, err)
				}
			})
		}
	}
}

func TestNoCommitIsInferredFromSuccessOrFailure(t *testing.T) {
	for _, mode := range []string{"success", "nonzero", "error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			tool := commandTool(t, func(context.Context, []string, io.Writer, io.Writer) (int, error) {
				switch mode {
				case "nonzero":
					return 1, nil
				case "error":
					return -1, errors.New("unknown effect")
				case "panic":
					panic("unknown effect")
				default:
					return 0, nil
				}
			}, exectools.Options{})
			_, h := receiptCall(t, t.Context(), tool, map[string]any{"args": []string{}})
			facts := h.Snapshot()
			if len(facts) != 1 || facts[0].Committed() || !facts[0].Completed() {
				t.Fatalf("inferred effect: %+v", facts)
			}
		})
	}
}

func TestTimeoutDoesNotPretendNoncooperativeBackendExited(t *testing.T) {
	entered, observedCancel, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseBackend := func() { once.Do(func() { close(release) }) }
	defer releaseBackend()
	tool := commandTool(t, func(ctx context.Context, _ []string, stdout, _ io.Writer) (int, error) {
		close(entered)
		<-ctx.Done()
		close(observedCancel)
		<-release
		if !executionreceipt.ReportLocalCommit(ctx) {
			return -1, errors.New("late synchronous commit was lost")
		}
		io.WriteString(stdout, "late synchronous output")
		return 0, nil
	}, exectools.Options{Timeout: 10 * time.Millisecond})
	type result struct {
		out agent.ToolCallOutcome
		h   *executionreceipt.Handle
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, h, err := agent.RunToolCallWithReceipts(t.Context(), agent.ToolCall{Name: tool.Name, ParsedArgs: map[string]any{"args": []string{}}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}, 1)
		done <- result{out, h, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("backend never entered")
	}
	select {
	case <-observedCancel:
	case <-time.After(5 * time.Second):
		t.Fatal("backend context never reached deadline")
	}
	select {
	case got := <-done:
		t.Fatalf("adapter returned before actual backend exit: %+v", got)
	default:
	}
	releaseBackend()
	select {
	case got := <-done:
		if got.err != nil || !errors.Is(got.out.Err, context.DeadlineExceeded) || !got.out.IsError {
			t.Fatalf("wrapper=%v outcome=%+v", got.err, got.out)
		}
		if err := got.h.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		facts := got.h.Snapshot()
		if len(facts) != 1 || !facts[0].Committed() || !facts[0].Completed() {
			t.Fatalf("late synchronous effect lost: %+v", facts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("adapter did not return after backend exit")
	}
}

func TestCodemodeNativeExecutionAndLeafDenial(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	sandbox, err := codemode.NewSandbox(ctx, codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close(context.Background())
	for _, kind := range []string{"command", "python"} {
		for _, mode := range []string{"success", "denied", "nonzero", "capture exhausted"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				var entered atomic.Int32
				execute := func(ctx context.Context, stdout io.Writer) (int, error) {
					entered.Add(1)
					if !executionreceipt.ReportLocalCommit(ctx) {
						return -1, errors.New("leaf capture missing")
					}
					io.WriteString(stdout, "native-output")
					if mode == "nonzero" {
						return 7, nil
					}
					return 0, nil
				}
				leaf := commandTool(t, func(ctx context.Context, args []string, stdout, _ io.Writer) (int, error) {
					if len(args) != 1 || args[0] != "child" {
						return -1, fmt.Errorf("unexpected args: %q", args)
					}
					return execute(ctx, stdout)
				}, exectools.Options{})
				argument := `{args:["child"]}`
				if kind == "python" {
					leaf = pythonTool(t, func(ctx context.Context, code string, stdout, _ io.Writer) (int, error) {
						if code != "pass" {
							return -1, fmt.Errorf("unexpected source: %q", code)
						}
						return execute(ctx, stdout)
					}, exectools.Options{})
					argument = `{code:"pass"}`
				}
				tool, err := codemodetool.New(sandbox, []codemodetool.Binding{codemodetool.Native(leaf, "host")}, codemodetool.Options{})
				if err != nil {
					t.Fatal(err)
				}
				options := agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}
				if mode == "denied" {
					options.CheckToolPermission = func(_ context.Context, in agent.BeforeToolCallContext) error {
						if in.ToolCall.Name == leaf.Name {
							return errors.New("host denies leaf")
						}
						return nil
					}
				}
				capacity := 3
				if mode == "capture exhausted" {
					capacity = 2
				}
				source := `let result; let rejected=false; try { result = await tools.` + leaf.Name + `(` + argument + `); } catch (error) { rejected=true; text("NATIVE_REJECTED"); } if (!rejected) { if (typeof result !== "object" || result.exit_code !== 0 || result.stdout !== "native-output") throw Error("wrong native value"); text("NATIVE_OK"); }`
				if mode == "nonzero" {
					source = `try { await tools.` + leaf.Name + `(` + argument + `); text("NATIVE_RESOLVED"); } catch (error) { if (error.code !== "tool_reported_error") throw Error("wrong rejection code"); text("NATIVE_REJECTED"); }`
				}

				out, h := receiptCallOptions(t, ctx, agent.ToolCall{ID: "script", Name: tool.Name, ParsedArgs: map[string]any{"code": source}}, options, capacity)
				if out.Err != nil || out.IsError {
					t.Fatalf("script failed: %+v", out)
				}
				if err := h.Wait(ctx); err != nil {
					t.Fatal(err)
				}
				text := ""
				for _, part := range out.Result.Content {
					text += part.Text
				}
				marker := "NATIVE_REJECTED"
				if mode == "success" {
					marker = "NATIVE_OK"
				}
				if !strings.Contains(text, marker) {
					t.Fatalf("Native result marker missing: %s", text)
				}
				facts := h.Snapshot()
				wantEntered, wantFacts := int32(1), 3
				if mode == "denied" || mode == "capture exhausted" {
					wantEntered, wantFacts = 0, 2
				}
				if entered.Load() != wantEntered || len(facts) != wantFacts {
					t.Fatalf("backend calls=%d receipts=%+v", entered.Load(), facts)
				}
				if facts[0].Committed() || !facts[0].Completed() || facts[0].ParentToken() != 0 || facts[1].Domain() != "codemode" || facts[1].Committed() || !facts[1].Completed() || facts[1].ParentToken() != facts[0].Token() {
					t.Fatalf("parent/host receipts=%+v", facts)
				}
				if wantEntered == 1 && (!facts[2].Committed() || !facts[2].Completed() || facts[2].ToolName() != leaf.Name || facts[2].Domain() != "agent" || facts[2].ParentToken() != facts[0].Token()) {
					t.Fatalf("leaf receipt=%+v", facts[2])
				}
			})
		}
	}
}

type fixtureModelStream struct {
	message agent.Message
	events  chan agent.AssistantEvent
}

func (s *fixtureModelStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *fixtureModelStream) Wait() (agent.Message, error)        { return s.message, nil }
func (s *fixtureModelStream) Close() error                        { return nil }

func TestModelRunReceiptBoundaryAndCommit(t *testing.T) {
	for _, captured := range []bool{false, true} {
		t.Run(fmt.Sprintf("captured=%v", captured), func(t *testing.T) {
			var requests, entered atomic.Int32
			tool := pythonTool(t, func(ctx context.Context, code string, stdout, _ io.Writer) (int, error) {
				entered.Add(1)
				if code != "pass" {
					return -1, fmt.Errorf("source=%q", code)
				}
				if !executionreceipt.ReportLocalCommit(ctx) {
					return -1, errors.New("capture missing")
				}
				io.WriteString(stdout, "model-output")
				return 0, nil
			}, exectools.Options{})
			model := agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
				message := agent.NewTextMessage(agent.RoleAssistant, "done")
				message.StopReason = agent.StopReasonStop
				if requests.Add(1) == 1 {
					message.Parts = nil
					message.StopReason = agent.StopReasonToolUse
					message.ToolCalls = []agent.ToolCall{{ID: "model-call", Name: tool.Name, ParsedArgs: map[string]any{"code": "pass"}}}
				}
				events := make(chan agent.AssistantEvent, 1)
				events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
				close(events)
				return &fixtureModelStream{message: message, events: events}, nil
			})
			runner, err := agent.NewRunner(agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, Model: model})
			if err != nil {
				t.Fatal(err)
			}
			prompts := []agent.Message{agent.NewUserTextMessage("run fixture")}
			var stream *agent.RunStream
			var h *executionreceipt.Handle
			if captured {
				stream, h, err = runner.RunWithReceipts(t.Context(), agent.AgentSnapshot{}, prompts, agent.LoopHooks{}, 1)
			} else {
				stream = runner.Run(t.Context(), agent.AgentSnapshot{}, prompts)
			}
			if err != nil {
				t.Fatal(err)
			}
			for range stream.Events() {
			}
			snapshot, err := stream.Wait()
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 2 {
				t.Fatalf("model requests=%d", requests.Load())
			}
			var toolResults []*agent.ToolResultPayload
			for _, message := range snapshot.Messages {
				if message.ToolResult != nil {
					toolResults = append(toolResults, message.ToolResult)
				}
			}
			if len(toolResults) != 1 || toolResults[0].IsError == captured {
				t.Fatalf("model tool results=%+v", toolResults)
			}
			if !captured {
				if entered.Load() != 0 || toolResults[0].Failure == nil || toolResults[0].Failure.Reason != "capture_required" {
					t.Fatalf("legacy run entered backend: calls=%d result=%+v", entered.Load(), toolResults[0])
				}
				return
			}
			if entered.Load() != 1 {
				t.Fatalf("captured backend calls=%d", entered.Load())
			}
			if err := h.Wait(t.Context()); err != nil {
				t.Fatal(err)
			}
			facts := h.Snapshot()
			if len(facts) != 1 || !facts[0].Committed() || !facts[0].Completed() || facts[0].CallID() != "model-call" || facts[0].ToolName() != "run_python" {
				t.Fatalf("model receipt=%+v", facts)
			}
		})
	}
}
