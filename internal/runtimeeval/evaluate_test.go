package runtimeeval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icatme/pi-go/agent"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestActualEntrypointsConserveCommonFixture(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	suite, err := Run(ctx, t.TempDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Runs) != 8 || suite.FixtureDigest == "" {
		t.Fatalf("suite: %+v", suite)
	}
	for _, run := range suite.Runs {
		if err := Validate(suite.Events, run); err != nil {
			t.Fatalf("%s: %v", run.Mode, err)
		}
		if run.Metrics.UniqueEvents != 10 || run.Metrics.Duplicates != 2 || run.Metrics.TaskCount != 2 || run.Metrics.RecordEffects != 6 || run.Metrics.WorkerToolCalls != 6 || run.Metrics.WorkerInvocations != 6 || run.Metrics.Replies != 6 {
			t.Fatalf("%s: %+v", run.Mode, run.Metrics)
		}
		if run.Metrics.LiveModelCalls != 0 || run.Metrics.InputTokens != nil || run.Metrics.OutputTokens != nil || run.Metrics.CostUSD != nil || run.Metrics.LatencyMS != nil {
			t.Fatal("invented performance/cost")
		}
	}
	raw, _ := json.Marshal(suite)
	for _, key := range []string{`"input_tokens":null`, `"output_tokens":null`, `"cost_usd":null`, `"latency_ms":null`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("missing %s", key)
		}
	}
}

func TestConservationRejectsObservedFaults(t *testing.T) {
	suite, err := Run(t.Context(), t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"drop", "misroute", "duplicate_effect", "empty_effect", "empty_reply", "missing_receipt", "wrong_value", "false_duplicate", "reply", "history", "metrics", "mode", "repetition", "reopen", "script_calls", "main_inputs"} {
		t.Run(fault, func(t *testing.T) {
			raw, _ := json.Marshal(suite.Runs[0])
			var run Result
			json.Unmarshal(raw, &run)
			switch fault {
			case "drop":
				run.Deliveries = run.Deliveries[1:]
			case "misroute":
				run.Deliveries[0].WorkID = "wrong"
			case "duplicate_effect":
				run.Effects = append(run.Effects, run.Effects[0])
			case "empty_effect":
				run.Effects[0] = Effect{}
			case "empty_reply":
				run.Replies[0] = Effect{}
			case "missing_receipt":
				run.Deliveries[0].Receipts = 0
			case "wrong_value":
				run.Effects[0].Value = "wrong"
			case "false_duplicate":
				run.Deliveries[0].Duplicate = true
			case "reply":
				run.Replies[0].Value = "wrong"
			case "history":
				run.Histories["B"] = append(run.Histories["B"], run.Histories["A"][0])
			case "metrics":
				run.Metrics.UniqueEvents++
			case "mode":
				run.Mode = "invalid"
			case "repetition":
				run.Repetition = -1
			case "reopen":
				run.ReopenChecked = false
			case "script_calls":
				run.Metrics.ScriptCalls = 1
			case "main_inputs":
				run.MainInputs["user"]++
			}
			if Validate(suite.Events, run) == nil {
				t.Fatal("fault passed conservation")
			}
		})
	}
}

func TestBudgetRejectedBeforeScript(t *testing.T) {
	s := newScript(1, 1)
	_, err := s.model("worker").Stream(t.Context(), agent.ModelRequest{})
	if err == nil || s.calls() != 0 {
		t.Fatal("oversized request reached script")
	}
}

func TestRepeatedResultsAreReproducible(t *testing.T) {
	suite, err := Run(t.Context(), t.TempDir(), 5)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(suite.Runs); i += 5 {
		base := suite.Runs[i]
		base.Repetition = 0
		for _, r := range suite.Runs[i+1 : i+5] {
			r.Repetition = 0
			if !reflect.DeepEqual(base, r) {
				t.Fatalf("%s observations changed", base.Mode)
			}
		}
	}
}

func exerciseMode(ctx context.Context, dir, mode string, events []Event, h *host) (Result, error) {
	r := Result{Mode: mode, Histories: map[string][]string{}}
	var err error
	if strings.HasPrefix(mode, "superagent_") {
		err = h.super(ctx, filepath.Join(dir, "journal.jsonl"), events, &r)
	} else {
		err = h.single(ctx, events, &r)
	}
	return r, err
}

func TestControlledToolBoundaryAndCancellation(t *testing.T) {
	for _, mode := range modes {
		for _, cancelAtTool := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%t", mode, cancelAtTool), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
				var once sync.Once
				h := &host{script: newScript(ContextBytes, ScriptCallLimit), beforeEffect: func(ctx context.Context, e Effect) error {
					once.Do(func() { close(entered) })
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}}
				dir := t.TempDir()
				go func() { _, err := exerciseMode(ctx, dir, mode, Fixture(), h); done <- err }()
				select {
				case <-entered:
				case err := <-done:
					t.Fatalf("never reached tool: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				h.mu.Lock()
				effects := len(h.effects)
				h.mu.Unlock()
				if effects != 0 {
					t.Fatal("effect crossed closed gate")
				}
				select {
				case err := <-done:
					t.Fatalf("finished while tool gated: %v", err)
				default:
				}
				if cancelAtTool {
					cancel()
				} else {
					close(release)
				}
				select {
				case err := <-done:
					if cancelAtTool && err == nil {
						t.Fatal("cancellation reported success")
					}
					if !cancelAtTool && err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("tool cancellation/exit not joined")
				}
				if cancelAtTool && len(h.effects) != 0 {
					t.Fatal("canceled tool produced an effect")
				}
			})
		}
	}
}

func TestConflictingDuplicateRejectedByEveryArm(t *testing.T) {
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			events := Fixture()
			conflict := events[0]
			conflict.Trigger.Text += "changed"
			events = append(events, conflict)
			h := &host{script: newScript(ContextBytes, ScriptCallLimit)}
			_, err := exerciseMode(t.Context(), t.TempDir(), mode, events, h)
			if err == nil || len(h.effects) != 6 {
				t.Fatalf("conflict: effects=%d err=%v", len(h.effects), err)
			}
		})
	}
}

func TestScriptCallBudgetStopsWithoutExtraInvocation(t *testing.T) {
	s := newScript(ContextBytes, 1)
	// Malformed model input consumes one admitted script call, not zero calls.
	_, err := s.model("worker").Stream(t.Context(), agent.ModelRequest{})
	if err == nil || s.calls() != 1 {
		t.Fatal("first call was not accounted")
	}
	_, err = s.model("worker").Stream(t.Context(), agent.ModelRequest{})
	if err == nil || s.calls() != 1 {
		t.Fatal("call limit not enforced")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.model("worker").Stream(ctx, agent.ModelRequest{})
	if !errors.Is(err, context.Canceled) || s.calls() != 1 {
		t.Fatal("canceled request counted")
	}
}

// A canceled main run must join admitted tool bodies, even if they ignore the
// cancellation signal. This is not a claim that failed receipts are persisted.
func TestSingleCancellationJoinsUncooperativeBody(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	releaseBody := func() { once.Do(func() { close(release) }) }
	defer releaseBody()
	h := &host{script: newScript(ContextBytes, ScriptCallLimit), beforeEffect: func(ctx context.Context, _ Effect) error { close(entered); <-release; return ctx.Err() }}
	go func() {
		r := Result{Mode: "single_pi_neutral", Histories: map[string][]string{}}
		finished <- h.single(ctx, Fixture()[:1], &r)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never entered")
	}
	cancel()
	select {
	case err := <-finished:
		t.Fatalf("returned before admitted body exited: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	releaseBody()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not settle")
	}
	if len(h.effects) != 0 {
		t.Fatal("canceled body recorded effect")
	}
}
