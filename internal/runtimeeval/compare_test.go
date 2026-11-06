package runtimeeval

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCompareFourStrategies(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	suite, err := RunCompare(ctx, t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Runs) != 4 || len(suite.Events) != 300 || suite.WorkerSlots != 4 {
		t.Fatal("incomplete suite")
	}
	for _, r := range suite.Runs {
		if err = ValidateCompare(suite.Events, r); err != nil {
			t.Fatal(err)
		}
	}
	a, b := suite.Runs[2], suite.Runs[3]
	if !reflect.DeepEqual(a.Histories, b.Histories) || !reflect.DeepEqual(compareEffects(a), compareEffects(b)) || a.Metrics != b.Metrics {
		t.Fatal("prompt changed scripted outcome")
	}
	raw, _ := json.Marshal(suite)
	t.Logf("full one-repeat suite bytes: %d", len(raw))
	for _, field := range []string{`"latency_ms":null`, `"input_tokens":null`, `"output_tokens":null`, `"cost_usd":null`} {
		if !strings.Contains(string(raw), field) {
			t.Fatal("missing", field)
		}
	}
	for _, run := range suite.Runs {
		for _, fault := range []string{"receipt", "task", "result_task", "role_calls", "history", "ack_reply", "admission_input", "peak_worker", "peak_tool", "effect", "reply", "duplicate", "cost", "input", "ack", "report"} {
			t.Run(run.Mode+"/"+fault, func(t *testing.T) {
				raw, _ := json.Marshal(run)
				var bad CompareResult
				if err := json.Unmarshal(raw, &bad); err != nil {
					t.Fatal(err)
				}
				switch fault {
				case "receipt":
					for i := range bad.Reports {
						if len(bad.Reports[i].Receipts) > 0 {
							bad.Reports[i].Receipts[0].CallID = "foreign"
							break
						}
					}
				case "role_calls":
					bad.MainScriptCalls++
				case "result_task":
					for i := range bad.Inputs {
						if bad.Inputs[i].Kind == "worker_result" {
							bad.Inputs[i].TaskID = "foreign"
							break
						}
					}
				case "task":
					for i := range bad.Reports {
						if bad.Reports[i].TaskID != "" {
							bad.Reports[i].TaskID = "foreign"
							break
						}
					}
				case "history":
					bad.Histories["work-00"] = append(bad.Histories["work-00"], "foreign/event")
				case "admission_input":
					for _, in := range bad.Inputs {
						if in.Kind == "user" {
							bad.Replies[0].InputID = in.ID
							break
						}
					}
				case "ack_reply":
					bad.Replies[0].Text = "admitted:" + bad.Replies[0].DeliveryID + ":" + bad.Replies[0].TaskID
				case "peak_worker":
					bad.PeakWorkers = 5
				case "peak_tool":
					bad.PeakTools = 5
				case "effect":
					bad.Effects[0].Value = "wrong"
				case "reply":
					bad.Replies[0].DeliveryID = "foreign"
				case "duplicate":
					bad.Deliveries[0].Accepted = false
				case "cost":
					n := 0.0
					bad.Metrics.CostUSD = &n
				case "input":
					bad.Inputs[0].DeliveryID = "foreign"
				case "ack":
					if len(bad.Acknowledgments) > 0 {
						bad.Acknowledgments[0].Receipt.Completed = false
					} else {
						bad.Acknowledgments = append(bad.Acknowledgments, CompareAck{})
					}
				case "report":
					bad.Reports = bad.Reports[1:]
				}
				if ValidateCompare(suite.Events, bad) == nil {
					t.Fatal("corrupt evidence passed")
				}
			})
		}
	}
}
func compareEffects(r CompareResult) []Effect {
	out := slices.Clone(r.Effects)
	slices.SortFunc(out, func(a, b Effect) int { return strings.Compare(a.Source+"/"+a.EventID, b.Source+"/"+b.EventID) })
	return out
}

func TestCompareOracleNotConsumed(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	for _, mode := range compareModes {
		t.Run(mode, func(t *testing.T) {
			events := LoadFixture()
			for i := range events {
				events[i].ExpectedWork = "poisoned-oracle"
			}
			r, err := runCompareArm(ctx, t.TempDir(), mode, 1, events, compareOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateCompare(LoadFixture(), r); err != nil {
				t.Fatalf("execution consumed oracle: %v", err)
			}
			if ValidateCompare(events, r) == nil {
				t.Fatal("mutated oracle accepted")
			}
		})
	}
}

func TestCompareAsyncAdmitsWhileBodiesHeld(t *testing.T) {
	for _, mode := range compareModes[2:] {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			entered := make(chan struct{}, 4)
			release := make(chan struct{})
			admitted := make(chan struct{})
			done := make(chan error, 1)
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			options := compareOptions{beforeTool: func(ctx context.Context, e Effect) error {
				if strings.HasPrefix(e.Value, "phase-0-") {
					select {
					case entered <- struct{}{}:
					default:
					}
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}, admitted: func(n int) {
				if n == 20 {
					close(admitted)
				}
			}}
			go func() {
				r, e := runCompareArm(ctx, t.TempDir(), mode, 1, LoadFixture(), options)
				if e == nil {
					e = ValidateCompare(LoadFixture(), r)
				}
				done <- e
			}()
			for range 4 {
				select {
				case <-entered:
				case e := <-done:
					t.Fatalf("early return: %v", e)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			select {
			case <-admitted:
			case e := <-done:
				t.Fatalf("early return: %v", e)
			case <-ctx.Done():
				t.Fatal("synchronous subtask prevented admission")
			}
			select {
			case e := <-done:
				t.Fatalf("completed with held bodies: %v", e)
			default:
			}
			unblock()
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestCompareCancellationJoinsBodies(t *testing.T) {
	for _, mode := range compareModes {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			var first, once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			options := compareOptions{beforeTool: func(ctx context.Context, _ Effect) error {
				first.Do(func() { close(entered) })
				<-release
				return ctx.Err()
			}}
			dir := t.TempDir()
			go func() { _, err := runCompareArm(ctx, dir, mode, 1, LoadFixture(), options); done <- err }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("no admitted body")
			}
			cancel()
			select {
			case err := <-done:
				t.Fatalf("returned before body exited: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			unblock()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled run: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("body not joined")
			}
		})
	}
}
