package runtimeeval

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadActualConcurrentEntrypoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	suite, err := RunLoad(ctx, t.TempDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Events) != 300 || len(suite.Runs) != 4 {
		t.Fatal("missing load fixture or arms")
	}
	for _, r := range suite.Runs {
		if err := ValidateLoad(suite.Events, r); err != nil {
			t.Fatal(err)
		}
		if len(r.Deliveries) != 300 || len(r.Reports) != 280 || len(r.Histories) != 20 || len(r.Effects) != 60 || r.PeakTools != 4 || r.VirtualToolEndMS != 150 {
			t.Fatalf("load counters: %+v", r.Metrics)
		}
		if r.Metrics.ScriptCalls != 120+len(r.Batches) {
			t.Fatal("calls not derived from real execution")
		}
	}
	for i := 0; i < len(suite.Runs); i += 2 {
		if !reflect.DeepEqual(suite.Runs[i].Histories, suite.Runs[i+1].Histories) || !reflect.DeepEqual(suite.Runs[i].Effects, suite.Runs[i+1].Effects) {
			t.Fatal("normalized conservation changed between repetitions")
		}
	}
}

func TestLoadScorerRejectsFaults(t *testing.T) {
	suite, err := RunLoad(t.Context(), t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"drop", "duplicate", "misroute", "receipt", "receipt_id", "receipt_domain", "receipt_commit", "reply", "history", "batch", "input", "peak", "clock", "metric", "cost", "reopen"} {
		t.Run(fault, func(t *testing.T) {
			raw, _ := json.Marshal(suite.Runs[1])
			var r LoadResult
			if err := json.Unmarshal(raw, &r); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "drop":
				r.Reports = r.Reports[1:]
			case "duplicate":
				r.Deliveries[0].Accepted = false
			case "misroute":
				r.Reports[0].TaskID = "foreign"
			case "receipt":
				for i := range r.Reports {
					if len(r.Reports[i].Receipts) > 0 {
						r.Reports[i].Receipts = nil
						break
					}
				}
			case "receipt_id":
				for i := range r.Reports {
					if len(r.Reports[i].Receipts) > 0 {
						r.Reports[i].Receipts[0].CallID = "wrong"
						break
					}
				}
			case "receipt_domain", "receipt_commit":
				for i := range r.Reports {
					if len(r.Reports[i].Receipts) > 0 {
						if fault == "receipt_domain" {
							r.Reports[i].Receipts[0].Domain = "foreign"
						} else {
							r.Reports[i].Receipts[0].Committed = true
						}
						break
					}
				}
			case "reply":
				r.Replies[0].DeliveryID = "wrong"
			case "history":
				r.Histories["work-00"] = append(r.Histories["work-00"], "foreign/event")
			case "batch":
				r.Batches[0].InputBytes = LoadBatchBytes + 1
			case "input":
				r.Batches[0].Inputs[0].Kind = "foreign"
			case "peak":
				r.PeakTools = 5
			case "clock":
				r.ToolReleases[0].ReleaseMS++
			case "metric":
				r.Metrics.WorkerInvocations++
			case "cost":
				x := 0.0
				r.Metrics.CostUSD = &x
			case "reopen":
				r.ReopenChecked = false
			}
			if ValidateLoad(suite.Events, r) == nil {
				t.Fatal("fault passed validation")
			}
		})
	}
}

func TestLoadCancellationJoinsBlockedTools(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	gate := newLoadToolGate()
	done := make(chan error, 1)
	go func() { done <- gate.beforeEffect(ctx, Effect{Source: "fixture", EventID: "one"}) }()
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("tool never entered")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("tool did not join")
	}
	if len(gate.releases) != 0 || gate.active != 0 {
		t.Fatal("canceled tool crossed gate or remained active")
	}
}

func TestLoadMalformedBatchFailsClosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := runLoadArm(ctx, t.TempDir(), "superagent_batch_subscribed", 1, loadOptions{malformedBatch: true})
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "decision") {
		t.Fatalf("incomplete batch did not fail explicitly: %v", err)
	}
}

func TestLoadRuntimeCancellationJoinsAllBlockedBodies(t *testing.T) {
	for _, mode := range loadModes {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			gate := newLoadToolGate()
			gate.ready, gate.holdRelease = make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() { _, err := runLoadArm(ctx, t.TempDir(), mode, 1, loadOptions{gate: gate}); done <- err }()
			select {
			case <-gate.ready:
			case err := <-done:
				t.Fatalf("never reached four tools: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled run: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runtime did not join blocked bodies")
			}
			gate.mu.Lock()
			defer gate.mu.Unlock()
			if gate.active != 0 || len(gate.releases) != 0 || gate.peak != 4 {
				t.Fatal("canceled runtime leaked gated work")
			}
		})
	}
}
