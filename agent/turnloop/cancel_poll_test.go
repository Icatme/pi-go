package turnloop

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

// Cancellation happens synchronously inside the real Engine FinishTurn hook.
// One P keeps watchParent from masking the missing synchronous poll check.
func TestReceiptCancellationBeforeControlPoll(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	for _, kind := range []string{"parent", "child", "explicit", "explicit-before-parent", "graceful"} {
		for _, delivery := range []Delivery{DeliverySteering, DeliveryFollowUp} {
			t.Run(kind+"/"+string(delivery), func(t *testing.T) {
				parent, cancelParent := context.WithCancelCause(t.Context())
				defer cancelParent(nil)
				parentFailure := errors.New("host canceled parent")
				var cancelChild context.CancelFunc
				var l *TurnLoop
				var queued *Receipt
				turns, starts := 0, 0
				config := Config{Definition: agent.AgentDefinition{Model: &recordingModel{}, FinishTurn: func(context.Context, agent.AgentTurnContext) (agent.AgentTurnDecision, error) {
					turns++
					if turns != 1 {
						return agent.AgentTurnDecision{}, nil
					}
					var err error
					queued, err = l.PushTracked(textInput(delivery, "queued"))
					if err != nil {
						return agent.AgentTurnDecision{}, err
					}
					switch kind {
					case "parent":
						cancelParent(parentFailure)
					case "child":
						cancelChild()
					case "explicit":
						_ = l.Stop(StopImmediate)
					case "explicit-before-parent":
						_ = l.Stop(StopImmediate)
						cancelParent(parentFailure)
					case "graceful":
						_ = l.Stop(StopGraceful)
					}
					return agent.AgentTurnDecision{}, nil
				}}, QueueCapacity: 1, OnEvent: func(e agent.AgentEvent) {
					if e.Type == agent.EventAgentStart {
						starts++
					}
				}}
				if kind == "child" {
					config.PrepareInvocation = func(context.Context) (Invocation, error) {
						return invocationFunc(func(ctx context.Context, r *agent.Runner, p []agent.Message, h agent.LoopHooks) (*agent.RunStream, error) {
							child, cancel := context.WithCancel(ctx)
							cancelChild = cancel
							return r.RunWithHooks(child, agent.AgentSnapshot{}, p, h), nil
						}), nil
					}
				}
				l = mustNewLoop(t, parent, config)
				first, err := l.PushTracked(textInput(DeliveryNextRun, "first"))
				if err != nil {
					t.Fatal(err)
				}
				result, err := waitLoop(t, l)
				if starts != 1 {
					t.Fatalf("started %d invocations (canceled input must not restart)", starts)
				}
				a, b := waitReceipt(t, first), waitReceipt(t, queued)
				if a.Disposition != DispositionHandedOff {
					t.Fatalf("already handed off input returned: %+v", a)
				}
				if kind == "graceful" {
					if err != nil || result.StopMode != StopGraceful || len(result.Unhandled) != 0 || b.Disposition != DispositionHandedOff || a.Err != nil || b.Err != nil || a.RunID != b.RunID || turns != 2 {
						t.Fatalf("graceful result=%+v err=%v receipts=%+v/%+v turns=%d", result, err, a, b, turns)
					}
					return
				}
				if !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"queued"}) || !reflect.DeepEqual(userTexts(result.Snapshot.Messages), []string{"first"}) || b.Disposition != DispositionUnhandled || b.RunID != "" {
					t.Fatalf("result=%+v err=%v receipts=%+v/%+v", result, err, a, b)
				}
				if !errors.Is(a.Err, context.Canceled) || !errors.Is(b.Err, context.Canceled) {
					t.Fatalf("raw cancellation missing: %+v/%+v", a, b)
				}
				switch kind {
				case "parent":
					if !errors.Is(err, parentFailure) || result.StopMode != StopImmediate || !errors.Is(b.Err, parentFailure) {
						t.Fatalf("parent result=%+v err=%v receipt=%+v", result, err, b)
					}
				case "child":
					if !errors.Is(err, context.Canceled) || result.StopMode != "" || l.parentErr != nil || l.stopSource != stopSourceNone {
						t.Fatalf("local cancellation treated as parent stop: result=%+v err=%v source=%v", result, err, l.stopSource)
					}
				default:
					if err != nil || result.StopMode != StopImmediate || errors.Is(b.Err, parentFailure) {
						t.Fatalf("explicit priority changed: result=%+v err=%v receipt=%+v", result, err, b)
					}
				}
			})
		}
	}
}
