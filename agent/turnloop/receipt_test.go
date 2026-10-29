package turnloop

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
)

func trackedBeforeRun(t *testing.T, l *TurnLoop, inputs ...Input) []*Receipt {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queue)+len(inputs) > l.queueCapacity {
		t.Fatal("test queue capacity")
	}
	receipts := make([]*Receipt, len(inputs))
	for i, input := range inputs {
		receipts[i] = &Receipt{ready: make(chan struct{})}
		l.queue = append(l.queue, queuedInput{Input: cloneValue(input), receipt: receipts[i]})
	}
	l.notify()
	return receipts
}
func waitReceipt(t *testing.T, r *Receipt) ReceiptResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	result, err := r.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func checkReceipt(t *testing.T, r *Receipt, d Disposition, cause error) ReceiptResult {
	t.Helper()
	result := waitReceipt(t, r)
	if result.Disposition != d || (cause == nil && result.Err != nil) || (cause != nil && !errors.Is(result.Err, cause)) {
		t.Fatalf("receipt=%+v want disposition=%s cause=%v", result, d, cause)
	}
	return result
}

func TestReceiptPublicAdmissionAndWaiterCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	model := &recordingModel{respond: func(ctx context.Context, _ agent.ModelRequest, _ int) (agent.AssistantStream, error) {
		close(entered)
		<-release
		return newImmediateStream(assistantText("ok"), nil), nil
	}}
	l := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: model}, QueueCapacity: 1})
	if r, err := l.PushTracked(Input{}); r != nil || !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("r=%v err=%v", r, err)
	}
	r, err := l.PushTracked(textInput(DeliveryNextRun, "first"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait=%v", err)
	}
	queued, err := l.PushTracked(textInput(DeliveryNextRun, "second"))
	if err != nil {
		t.Fatal(err)
	}
	if rejected, err := l.PushTracked(textInput(DeliveryNextRun, "full")); rejected != nil || !errors.Is(err, ErrQueueFull) {
		t.Fatalf("r=%v err=%v", rejected, err)
	}
	_ = l.Stop(StopImmediate)
	close(release)
	_, _ = waitLoop(t, l)
	// The fake model returned normally despite cancellation, so do not invent a
	// model failure. Both outcomes depend on which cancellation boundary ran.
	first := waitReceipt(t, r)
	if first.Disposition != DispositionHandedOff {
		t.Fatal(first)
	}
	checkReceipt(t, queued, DispositionUnhandled, context.Canceled)
	if rejected, err := l.PushTracked(textInput(DeliveryNextRun, "late")); rejected != nil || !errors.Is(err, ErrNotAccepting) {
		t.Fatalf("r=%v err=%v", rejected, err)
	}
	for _, invalid := range []*Receipt{nil, {}} {
		if _, err := invalid.Wait(t.Context()); err == nil {
			t.Fatal("invalid receipt accepted")
		}
	}
	if _, err := r.Wait(nil); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestReceiptSuccessSharedInvocationAndNoDedup(t *testing.T) {
	l := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}, SteeringMode: agent.QueueModeAll}, QueueCapacity: 3})
	a, b := textInput(DeliverySteering, "a"), textInput(DeliverySteering, "b")
	a.Message.ID = "same"
	b.Message.ID = "same"
	rs := trackedBeforeRun(t, l, a, b, textInput(DeliveryNextRun, "next"))
	_ = l.Stop(StopGraceful)
	result, err := waitLoop(t, l)
	if err != nil || len(result.Unhandled) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	first := checkReceipt(t, rs[0], DispositionHandedOff, nil)
	second := checkReceipt(t, rs[1], DispositionHandedOff, nil)
	next := checkReceipt(t, rs[2], DispositionHandedOff, nil)
	if first.RunID == "" || first.RunID != second.RunID || next.RunID == first.RunID {
		t.Fatalf("ids=%q %q %q", first.RunID, second.RunID, next.RunID)
	}
	if rs[0].future != rs[1].future || rs[0].future == rs[2].future {
		t.Fatal("invocation future ownership incorrect")
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				checkReceipt(t, rs[0], DispositionHandedOff, nil)
			}
		}()
	}
	wg.Wait()
}

func TestReceiptPreparationAndStartRollback(t *testing.T) {
	for _, kind := range []string{"prepare", "start", "nil-start"} {
		t.Run(kind, func(t *testing.T) {
			failure := errors.New("start rejected")
			var l *TurnLoop
			var extra *Receipt
			l = mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}, SteeringMode: agent.QueueModeAll}, QueueCapacity: 5, PrepareInvocation: func(context.Context) (Invocation, error) {
				if kind == "prepare" {
					return nil, failure
				}
				return invocationFunc(func(_ context.Context, _ *agent.Runner, _ []agent.Message, _ agent.LoopHooks) (*agent.RunStream, error) {
					var err error
					extra, err = l.PushTracked(textInput(DeliveryNextRun, "new"))
					if err != nil {
						return nil, err
					}
					if kind == "nil-start" {
						return nil, nil
					}
					return nil, failure
				}), nil
			}})
			rs := trackedBeforeRun(t, l, textInput(DeliverySteering, "a"), textInput(DeliveryNextRun, "b"), textInput(DeliverySteering, "c"), textInput(DeliveryNextRun, "d"))
			result, err := waitLoop(t, l)
			want := []string{"a", "b", "c", "d"}
			if extra != nil {
				want = append(want, "new")
				rs = append(rs, extra)
			}
			if err == nil || !reflect.DeepEqual(inputTexts(result.Unhandled), want) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			for _, r := range rs {
				got := waitReceipt(t, r)
				if got.Disposition != DispositionUnhandled || got.RunID != "" || got.Err == nil {
					t.Fatal(got)
				}
				if kind != "nil-start" && !errors.Is(got.Err, failure) {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestReceiptHookViolationIsUncertain(t *testing.T) {
	for _, nilError := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "nil"}[nilError], func(t *testing.T) {
			failure := errors.New("bad callback")
			l := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 3, PrepareInvocation: func(context.Context) (Invocation, error) {
				return invocationFunc(func(ctx context.Context, _ *agent.Runner, _ []agent.Message, h agent.LoopHooks) (*agent.RunStream, error) {
					_, _ = h.GetSteeringMessages(ctx)
					if nilError {
						return nil, nil
					}
					return nil, failure
				}), nil
			}})
			rs := trackedBeforeRun(t, l, textInput(DeliveryNextRun, "initial"), textInput(DeliverySteering, "hook"), textInput(DeliveryNextRun, "queued"))
			result, err := waitLoop(t, l)
			if err == nil || !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"queued"}) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			for _, r := range rs[:2] {
				got := waitReceipt(t, r)
				if got.Disposition != DispositionUncertain || got.Err == nil || got.RunID != "" {
					t.Fatal(got)
				}
			}
			if rs[0].future != rs[1].future {
				t.Fatal("not shared")
			}
			got := waitReceipt(t, rs[2])
			if got.Disposition != DispositionUnhandled || got.Err == nil {
				t.Fatal(got)
			}
		})
	}
}

func TestReceiptStreamAndErrorIsUncertain(t *testing.T) {
	failure := errors.New("invalid stream start")
	l := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 2, PrepareInvocation: func(context.Context) (Invocation, error) {
		return invocationFunc(func(ctx context.Context, r *agent.Runner, p []agent.Message, h agent.LoopHooks) (*agent.RunStream, error) {
			return r.RunWithHooks(ctx, agent.AgentSnapshot{}, p, h), failure
		}), nil
	}})
	rs := trackedBeforeRun(t, l, textInput(DeliveryNextRun, "initial"), textInput(DeliveryNextRun, "queued"))
	_, err := waitLoop(t, l)
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	first := checkReceipt(t, rs[0], DispositionUncertain, failure)
	if first.RunID != "" {
		t.Fatal("fabricated observed run ID")
	}
	checkReceipt(t, rs[1], DispositionUnhandled, failure)
}

func TestReceiptImmediateStopPreservesRawCancellation(t *testing.T) {
	entered := make(chan struct{})
	l := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{respond: func(ctx context.Context, _ agent.ModelRequest, _ int) (agent.AssistantStream, error) {
		close(entered)
		return newCancelStream(ctx), nil
	}}}, QueueCapacity: 2})
	rs := trackedBeforeRun(t, l, textInput(DeliveryNextRun, "initial"), textInput(DeliveryNextRun, "queued"))
	<-entered
	_ = l.Stop(StopImmediate)
	_, err := waitLoop(t, l)
	if err != nil {
		t.Fatalf("loop control error=%v", err)
	}
	checkReceipt(t, rs[0], DispositionHandedOff, context.Canceled)
	checkReceipt(t, rs[1], DispositionUnhandled, context.Canceled)
}

func TestReceiptSessionCASFailureIsNotUnhandled(t *testing.T) {
	storage, _ := session.NewMemoryStorage(session.Header{ID: "receipt-cas"})
	s, err := session.New(storage, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 2, PrepareInvocation: func(context.Context) (Invocation, error) {
		b, err := s.PrepareRun(session.MainLane, session.RuntimeOptions{Scope: func() session.RuntimeScope { return session.RuntimeScope{Identity: "host"} }})
		if err != nil {
			return nil, err
		}
		if err := s.MoveLane(session.MainLane, ""); err != nil {
			return nil, err
		}
		return b, nil
	}})
	rs := trackedBeforeRun(t, l, textInput(DeliveryNextRun, "initial"), textInput(DeliveryNextRun, "queued"))
	result, err := waitLoop(t, l)
	if err == nil || !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"queued"}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for i, r := range rs {
		got := waitReceipt(t, r)
		want := DispositionHandedOff
		if i == 1 {
			want = DispositionUnhandled
		}
		if got.Disposition != want || got.Err == nil {
			t.Fatal(got)
		}
	}
}

func TestReceiptActiveHooksShareFuture(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	model := &recordingModel{respond: func(_ context.Context, _ agent.ModelRequest, n int) (agent.AssistantStream, error) {
		if n == 1 {
			close(entered)
			<-release
		}
		return newImmediateStream(assistantText("ok"), nil), nil
	}}
	l := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: model}, QueueCapacity: 2})
	first, err := l.PushTracked(textInput(DeliveryNextRun, "initial"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	steer, err := l.PushTracked(textInput(DeliverySteering, "steer"))
	if err != nil {
		t.Fatal(err)
	}
	follow, err := l.PushTracked(textInput(DeliveryFollowUp, "follow"))
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Stop(StopGraceful)
	close(release)
	_, err = waitLoop(t, l)
	if err != nil {
		t.Fatal(err)
	}
	a := checkReceipt(t, first, DispositionHandedOff, nil)
	for _, r := range []*Receipt{steer, follow} {
		b := checkReceipt(t, r, DispositionHandedOff, nil)
		if b.RunID != a.RunID || r.future != first.future {
			t.Fatal("hook did not share invocation")
		}
	}
	if len(model.views()) != 3 {
		t.Fatalf("model calls=%d", len(model.views()))
	}
}

func TestReceiptSettlesBeforeLoopAndCanceledWaitDoesNotWithdraw(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	l := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{respond: func(_ context.Context, _ agent.ModelRequest, n int) (agent.AssistantStream, error) {
		if n == 1 {
			close(entered)
			<-release
		}
		return newImmediateStream(assistantText("ok"), nil), nil
	}}}, QueueCapacity: 1})
	r, err := l.PushTracked(textInput(DeliveryNextRun, "kept"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var waiters sync.WaitGroup
	for range 8 {
		waiters.Add(1)
		go func() { defer waiters.Done(); checkReceipt(t, r, DispositionHandedOff, nil) }()
	}
	close(release)
	waiters.Wait()
	checkReceipt(t, r, DispositionHandedOff, nil)
	select {
	case <-l.done:
		t.Fatal("receipt terminated idle loop")
	default:
	}
	result, err := r.Wait(ctx)
	if err != nil || result.Err != nil || result.Disposition != DispositionHandedOff {
		t.Fatalf("completed wait=%+v err=%v", result, err)
	}
	next, err := l.PushTracked(textInput(DeliveryNextRun, "next"))
	if err != nil {
		t.Fatal(err)
	}
	nextResult := checkReceipt(t, next, DispositionHandedOff, nil)
	if nextResult.RunID == result.RunID {
		t.Fatal("new invocation reused run ID")
	}
	_ = l.Stop(StopGraceful)
	_, err = waitLoop(t, l)
	if err != nil {
		t.Fatal(err)
	}
}

func TestReceiptStopDuringPreparationIsUnhandled(t *testing.T) {
	entered := make(chan struct{})
	l := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 1, PrepareInvocation: func(ctx context.Context) (Invocation, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() }})
	r, err := l.PushTracked(textInput(DeliveryNextRun, "waiting"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	_ = l.Stop(StopImmediate)
	_, err = waitLoop(t, l)
	if err != nil {
		t.Fatal(err)
	}
	checkReceipt(t, r, DispositionUnhandled, context.Canceled)
}

func TestReceiptScopeRejectionIsUnhandled(t *testing.T) {
	storage, _ := session.NewMemoryStorage(session.Header{ID: "scope-receipt"})
	s, err := session.New(storage, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope := session.RuntimeScope{Identity: "host", AuthEpoch: 1}
	l := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 1, PrepareInvocation: func(context.Context) (Invocation, error) {
		b, err := s.PrepareRun(session.MainLane, session.RuntimeOptions{Scope: func() session.RuntimeScope { return scope }})
		scope.AuthEpoch++
		return b, err
	}})
	r, err := l.PushTracked(textInput(DeliveryNextRun, "not started"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = waitLoop(t, l)
	if !errors.Is(err, session.ErrScopeMismatch) {
		t.Fatal(err)
	}
	checkReceipt(t, r, DispositionUnhandled, session.ErrScopeMismatch)
}

type receiptLostAckStorage struct {
	session.Storage
	cause error
}

func (s *receiptLostAckStorage) CompareAppend(v session.BranchVersion, e []session.NewEntry) (session.BranchVersion, []session.Entry, error) {
	version, entries, err := s.Storage.CompareAppend(v, e)
	if err != nil {
		return version, entries, err
	}
	return session.BranchVersion{}, nil, s.cause
}
func TestReceiptLostAckIsHandedOffButNotCommitProof(t *testing.T) {
	memory, _ := session.NewMemoryStorage(session.Header{ID: "receipt-lost-ack"})
	failure := errors.New("write succeeded but ack lost")
	s, err := session.New(&receiptLostAckStorage{Storage: memory, cause: failure}, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var binding *session.RunBinding
	l := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 1, PrepareInvocation: func(context.Context) (Invocation, error) {
		var err error
		binding, err = s.PrepareRun(session.MainLane, session.RuntimeOptions{Scope: func() session.RuntimeScope { return session.RuntimeScope{Identity: "host"} }})
		return binding, err
	}})
	r, err := l.PushTracked(textInput(DeliveryNextRun, "actually written"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := waitLoop(t, l)
	if !errors.Is(err, failure) || len(result.Unhandled) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	checkReceipt(t, r, DispositionHandedOff, failure)
	status, err := binding.CommitStatus()
	if err != nil || !status.Invalidated || status.Commits != 0 || len(status.Messages) != 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	branch, err := s.ReadBranch(session.MainLane)
	if err != nil {
		t.Fatal(err)
	}
	stored := false
	for _, e := range branch.Entries {
		if e.Message != nil && reflect.DeepEqual(userTexts([]agent.Message{*e.Message}), []string{"actually written"}) {
			stored = true
		}
	}
	if !stored {
		t.Fatal("lost-ack test did not actually persist input")
	}
}

func TestReceiptStopDuringStartRollsBackWithRawCause(t *testing.T) {
	for _, parentStop := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "parent"}[parentStop], func(t *testing.T) {
			parent, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			entered := make(chan struct{})
			l := mustNewLoop(t, parent, Config{Definition: agent.AgentDefinition{Model: &recordingModel{}, SteeringMode: agent.QueueModeAll}, QueueCapacity: 3, PrepareInvocation: func(context.Context) (Invocation, error) {
				return invocationFunc(func(ctx context.Context, _ *agent.Runner, _ []agent.Message, _ agent.LoopHooks) (*agent.RunStream, error) {
					close(entered)
					<-ctx.Done()
					return nil, ctx.Err()
				}), nil
			}})
			rs := trackedBeforeRun(t, l, textInput(DeliverySteering, "first"), textInput(DeliveryNextRun, "middle"), textInput(DeliverySteering, "last"))
			<-entered
			custom := errors.New("owner stopped")
			if parentStop {
				cancel(custom)
			} else {
				_ = l.Stop(StopImmediate)
			}
			result, err := waitLoop(t, l)
			if !parentStop && err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"first", "middle", "last"}) {
				t.Fatal(result)
			}
			for _, r := range rs {
				got := checkReceipt(t, r, DispositionUnhandled, context.Canceled)
				if parentStop && !errors.Is(got.Err, custom) {
					t.Fatal(got)
				}
			}
		})
	}
}
