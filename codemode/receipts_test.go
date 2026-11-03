package codemode

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Icatme/pi-go/executionreceipt"
	"sync/atomic"
	"testing"
	"time"
)

// Compile historical positional layouts without adding fields to old structs.
var _ = Execution{"returned", "not_applicable"}
var _ = Tool{"name", "", "", "", nil, nil, nil}

func TestReceiptsHostFailureAndProjection(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	for _, kind := range []string{"error", "panic", "invalid_json", "success"} {
		t.Run(kind, func(t *testing.T) {
			tool := Tool{Name: "mutate", Invoke: func(ctx context.Context, _ HostCall) (json.RawMessage, error) {
				if err := executionreceipt.RequireCapture(ctx); err != nil {
					return nil, err
				}
				if !executionreceipt.ReportLocalCommit(ctx) {
					panic("capture missing")
				}
				switch kind {
				case "error":
					return nil, errors.New("failure after commit")
				case "panic":
					panic("failure after commit")
				case "invalid_json":
					return json.RawMessage(`{`), nil
				}
				return json.RawMessage(`true`), nil
			}}
			_, h, err := s.RunWithReceipts(context.Background(), `return await tools.mutate({})`, RunOptions{Tools: []Tool{tool}}, 1)
			if (err == nil) != (kind == "success") {
				t.Fatalf("err=%v", err)
			}
			records := h.Snapshot()
			if len(records) != 1 || !records[0].Committed() || !records[0].Completed() {
				t.Fatal(records)
			}
			if err = h.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestReceiptsCancelKeepsActualHostLifetime(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var late context.Context
	tool := Tool{Name: "wait", Invoke: func(ctx context.Context, _ HostCall) (json.RawMessage, error) {
		late = ctx
		close(entered)
		<-release
		if !executionreceipt.ReportLocalCommit(ctx) {
			return nil, errors.New("commit rejected")
		}
		return json.RawMessage(`true`), nil
	}}
	type answer struct {
		h   *executionreceipt.Handle
		err error
	}
	done := make(chan answer, 1)
	go func() {
		_, h, err := s.RunWithReceipts(ctx, `return await tools.wait({})`, RunOptions{Tools: []Tool{tool}}, 1)
		done <- answer{h, err}
	}()
	<-entered
	cancel()
	a := <-done
	if a.err == nil {
		t.Error("canceled run succeeded")
	}
	first := a.h.Snapshot()
	if len(first) != 1 || first[0].Completed() || first[0].Committed() {
		t.Fatal(first)
	}
	waitCtx, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	if a.h.Wait(waitCtx) == nil {
		t.Error("active call reported finished")
	}
	close(release)
	waitCtx2, stop2 := context.WithTimeout(context.Background(), time.Second)
	defer stop2()
	if err := a.h.Wait(waitCtx2); err != nil {
		t.Fatal(err)
	}
	if !a.h.Snapshot()[0].Committed() || first[0].Committed() {
		t.Fatal("late completion lost or old snapshot changed")
	}
	if executionreceipt.ReportLocalCommit(late) {
		t.Fatal("late report accepted")
	}
}
func TestReceiptAdmissionAndLegacyInvoke(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	var writes atomic.Int32
	tool := Tool{Name: "mutate", Invoke: func(ctx context.Context, _ HostCall) (json.RawMessage, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return nil, err
		}
		writes.Add(1)
		executionreceipt.ReportLocalCommit(ctx)
		return json.RawMessage(`true`), nil
	}}
	_, err := s.Run(context.Background(), `return await tools.mutate({})`, RunOptions{Tools: []Tool{tool}})
	if err == nil || writes.Load() != 0 {
		t.Fatal("legacy call performed I/O")
	}
	_, h, err := s.RunWithReceipts(context.Background(), `await tools.mutate({}); await tools.mutate({})`, RunOptions{Tools: []Tool{tool}, Sequential: true}, 1)
	if err == nil || writes.Load() != 1 || len(h.Snapshot()) != 1 {
		t.Fatal("unreserved invocation entered")
	}
	b, _ := json.Marshal(Execution{"returned", "not_applicable"})
	if string(b) != `{"local":"returned","remote":"not_applicable"}` {
		t.Fatal(string(b))
	}
}
