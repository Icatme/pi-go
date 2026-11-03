package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
)

func receiptBinding(t *testing.T, s *Session) *ReceiptCallBinding {
	t.Helper()
	b, err := s.PrepareReceiptCall(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func receiptStatus(t *testing.T, s *Session, lane string) ReceiptStatus {
	t.Helper()
	status, err := s.ReadReceiptStatus(lane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	return status
}
func receiptWriteTool(writes *atomic.Int32) agent.ToolDefinition {
	return agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		writes.Add(1)
		if !executionreceipt.ReportLocalCommit(ctx) {
			return agent.ToolResult{}, errors.New("report rejected")
		}
		return agent.ToolResult{}, nil
	}}
}
func receiptRun(ctx context.Context, b *ReceiptCallBinding, tool agent.ToolDefinition) (agent.ToolCallOutcome, *executionreceipt.Handle, error) {
	return b.RunToolCall(ctx, agent.ToolCall{ID: "direct", Name: tool.Name}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}}, 8)
}

func TestReceiptCallsFileCommitHookFailureAndJSONLReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.jsonl")
	disk, err := CreateJSONLStorage(path, Header{ID: "receipts"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(disk, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	file := filepath.Join(t.TempDir(), "effect.txt")
	cause := errors.New("after-hook failed")
	tool := agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		pending := receiptStatus(t, s, MainLane)
		if !pending.Pending() || pending.CallToken() == "" {
			t.Error("intent not durable before I/O")
		}
		if _, err := s.PrepareRun(MainLane, runtimeOptions()); !errors.Is(err, ErrInterrupted) {
			t.Errorf("legacy runner not fenced: %v", err)
		}
		if err := os.WriteFile(file, []byte("committed"), 0600); err != nil {
			return agent.ToolResult{}, err
		}
		if !executionreceipt.ReportLocalCommit(ctx) {
			return agent.ToolResult{}, errors.New("commit rejected")
		}
		return agent.ToolResult{}, nil
	}}
	out, h, err := receiptBinding(t, s).RunToolCall(t.Context(), agent.ToolCall{ID: "direct", Name: "write"}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}, AfterToolCall: func(context.Context, agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		return agent.AfterToolCallResult{}, cause
	}}, 8)
	if err != nil || !errors.Is(out.Err, cause) || h == nil {
		t.Fatalf("wrapper=%v outcome=%+v handle=%v", err, out, h)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "committed" {
		t.Fatalf("effect=%q err=%v", data, err)
	}
	status := receiptStatus(t, s, MainLane)
	facts := status.Facts()
	if status.Pending() || status.CallToken() == "" || len(facts) != 1 || !facts[0].Committed() || !facts[0].Completed() || facts[0].CallID() != "direct" || facts[0].ToolName() != "write" || facts[0].Domain() != "agent" || facts[0].Token() == 0 || facts[0].ParentToken() != 0 {
		t.Fatalf("status=%+v facts=%+v", status, facts)
	}
	facts[0] = ReceiptFact{}
	if !status.Facts()[0].Committed() {
		t.Fatal("Facts aliases status")
	}
	if _, err := s.PrepareRun(MainLane, runtimeOptions()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("legacy fence removed after final: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	batches := 0
	for _, line := range lines {
		var item struct {
			Kind  string    `json:"kind"`
			Items []LogItem `json:"items"`
		}
		if err := json.Unmarshal(line, &item); err != nil {
			t.Fatal(err)
		}
		if item.Kind != "batch" {
			continue
		}
		batches++
		if len(item.Items) != 2 {
			t.Fatalf("non-atomic receipt pair: %s", line)
		}
		var old runtimeRecord
		if item.Items[0].Entry == nil || item.Items[0].Entry.Custom == nil || item.Items[0].Entry.Custom.CustomType != runtimeRecordType {
			t.Fatal("missing legacy fence")
		}
		if err := json.Unmarshal(item.Items[0].Entry.Custom.Payload, &old); err != nil || !old.Pending {
			t.Fatalf("legacy record=%+v err=%v", old, err)
		}
	}
	if batches != 2 {
		t.Fatalf("batches=%d log=%s", batches, raw)
	}
	disk, err = OpenJSONLStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(disk, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored := receiptStatus(t, reopened, MainLane)
	if restored.Pending() || restored.CallToken() != status.CallToken() || restored.Version() != status.Version() || len(restored.Facts()) != 1 || !restored.Facts()[0].Committed() {
		t.Fatalf("restored=%+v", restored)
	}
	if _, err := reopened.PrepareReceiptCall(MainLane, runtimeOptions()); err != nil {
		t.Fatalf("final prevents fresh direct call: %v", err)
	}
}

func TestReceiptCallsConcurrentBindingsCAS(t *testing.T) {
	storage, err := NewMemoryStorage(Header{ID: "receipt-cas"})
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := New(storage, Options{})
	s2, _ := New(storage, Options{})
	defer s1.Close()
	bindings := []*ReceiptCallBinding{receiptBinding(t, s1), receiptBinding(t, s2)}
	var writes atomic.Int32
	tool := receiptWriteTool(&writes)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, b := range bindings {
		go func() { <-start; _, _, err := receiptRun(t.Context(), b, tool); errs <- err }()
	}
	close(start)
	a, b := <-errs, <-errs
	if (a == nil) == (b == nil) || writes.Load() != 1 {
		t.Fatalf("errors=%v/%v writes=%d", a, b, writes.Load())
	}
	if a == nil {
		a = b
	}
	if errorCode(a) != ErrorConflict {
		t.Fatalf("loser not CAS conflict: %v", a)
	}
}

func TestReceiptCallsLostAcknowledgments(t *testing.T) {
	for _, stage := range []int{1, 2} {
		t.Run(map[int]string{1: "intent", 2: "final"}[stage], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lost.jsonl")
			disk, err := CreateJSONLStorage(path, Header{ID: "receipt-lost"})
			if err != nil {
				t.Fatal(err)
			}
			cause := errors.New("lost acknowledgment")
			storage := &lostAckStorage{Storage: disk, failAt: stage, cause: cause}
			s, _ := New(storage, Options{})
			defer s.Close()
			b := receiptBinding(t, s)
			var writes atomic.Int32
			_, _, err = receiptRun(t.Context(), b, receiptWriteTool(&writes))
			if !errors.Is(err, cause) {
				t.Fatalf("lost ACK=%v", err)
			}
			if writes.Load() != int32(stage-1) {
				t.Fatalf("writes=%d", writes.Load())
			}
			status := receiptStatus(t, s, MainLane)
			if status.Pending() != (stage == 1) {
				t.Fatalf("persisted status=%+v", status)
			}
			if err := b.Finalize(t.Context()); err == nil {
				t.Fatal("uncertain binding retried write")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenJSONLStorage(path)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := New(reopened, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			restored := receiptStatus(t, fresh, MainLane)
			if restored.Pending() != status.Pending() || restored.CallToken() != status.CallToken() {
				t.Fatalf("reopen changed receipt: %+v", restored)
			}
			_, err = fresh.PrepareReceiptCall(MainLane, runtimeOptions())
			if stage == 1 && !errors.Is(err, ErrInterrupted) {
				t.Fatalf("pending takeover: %v", err)
			}
			if stage == 2 && err != nil {
				t.Fatalf("fresh reader missed persisted final: %v", err)
			}
		})
	}
}

func TestReceiptCallsScopeCannotBeTakenOver(t *testing.T) {
	s := hookSession(t)
	scope := testScope
	options := RuntimeOptions{Scope: func() RuntimeScope { return scope }}
	b, err := s.PrepareReceiptCall(MainLane, options)
	if err != nil {
		t.Fatal(err)
	}
	scope.AuthEpoch++
	var writes atomic.Int32
	if _, _, err := receiptRun(t.Context(), b, receiptWriteTool(&writes)); !errors.Is(err, ErrScopeMismatch) || writes.Load() != 0 {
		t.Fatalf("changed scope executed: %v writes=%d", err, writes.Load())
	}
	// A permission hook changes authorization after admission but before the body.
	scope = testScope
	b, err = s.PrepareReceiptCall(MainLane, options)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = b.RunToolCall(t.Context(), agent.ToolCall{ID: "scope", Name: "write"}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{receiptWriteTool(&writes)}, CheckToolPermission: func(context.Context, agent.BeforeToolCallContext) error { scope.AuthEpoch++; return nil }}, 8)
	if writes.Load() != 0 {
		t.Fatal("permission epoch change reached I/O")
	}
	if err := b.Finalize(t.Context()); err == nil {
		t.Fatalf("old binding changed scope: %v", err)
	}
	if _, err := s.ReadReceiptStatus(MainLane, options); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("new epoch read old facts: %v", err)
	}
	if _, err := s.PrepareReceiptCall(MainLane, options); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("new epoch took over: %v", err)
	}
}

func TestReceiptCallsLateChildFinalizeAndForks(t *testing.T) {
	s := hookSession(t)
	b := receiptBinding(t, s)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	releaseChild := func() { once.Do(func() { close(release) }) }
	defer releaseChild()
	var writes atomic.Int32
	leaf := receiptWriteTool(&writes)
	base := leaf.Execute
	leaf.Execute = func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
		close(entered)
		<-release
		return base(ctx, in)
	}
	parent := agent.ToolDefinition{Name: "parent", ChildTools: []agent.ToolDefinition{leaf}, Execute: func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
		go in.ChildCaller.Call(ctx, agent.ToolCall{Name: "write"})
		<-entered
		return agent.ToolResult{}, nil
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, h, err := receiptRun(ctx, b, parent)
	if h == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late child handle=%v err=%v", h, err)
	}
	pending := receiptStatus(t, s, MainLane)
	if !pending.Pending() {
		t.Fatal("late child finalized early")
	}
	if err := s.CreateLane("pending-fork", pending.Version().Head); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareReceiptCall("pending-fork", runtimeOptions()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("fork took over pending: %v", err)
	}
	releaseChild()
	if err := b.Finalize(t.Context()); err != nil {
		t.Fatal(err)
	}
	done := receiptStatus(t, s, MainLane)
	facts := done.Facts()
	if done.Pending() || len(facts) != 2 || facts[0].Committed() || !facts[1].Committed() || !facts[1].Completed() || writes.Load() != 1 {
		t.Fatalf("facts=%+v writes=%d", facts, writes.Load())
	}
	if !receiptStatus(t, s, "pending-fork").Pending() {
		t.Fatal("main final changed pending fork")
	}
	if err := s.CreateLane("completed-fork", done.Version().Head); err != nil {
		t.Fatal(err)
	}
	inherited := receiptStatus(t, s, "completed-fork")
	if inherited.Pending() || inherited.CallToken() != done.CallToken() {
		t.Fatalf("fork=%+v", inherited)
	}
	forkBinding, err := s.PrepareReceiptCall("completed-fork", runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := receiptRun(t.Context(), forkBinding, receiptWriteTool(&writes)); err != nil {
		t.Fatal(err)
	}
	forkDone := receiptStatus(t, s, "completed-fork")
	if forkDone.Pending() || forkDone.CallToken() == done.CallToken() || len(forkDone.Facts()) != 1 || !forkDone.Facts()[0].Committed() {
		t.Fatalf("new fork call=%+v", forkDone)
	}
	if receiptStatus(t, s, MainLane).CallToken() != done.CallToken() {
		t.Fatal("fork changed main receipt")
	}
}

func TestReceiptCallsRejectInvalidCapacityBeforeIntent(t *testing.T) {
	for _, capacity := range []int{-1, 0, 129} {
		s := hookSession(t)
		before, err := s.ReadBranch(MainLane)
		if err != nil {
			t.Fatal(err)
		}
		b := receiptBinding(t, s)
		var writes atomic.Int32
		_, h, err := b.RunToolCall(t.Context(), agent.ToolCall{Name: "write"}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{receiptWriteTool(&writes)}}, capacity)
		after, readErr := s.ReadBranch(MainLane)
		if err == nil || h != nil || readErr != nil || writes.Load() != 0 || after.Version != before.Version {
			t.Fatalf("capacity=%d err=%v handle=%v writes=%d before=%+v after=%+v", capacity, err, h, writes.Load(), before.Version, after.Version)
		}
	}
}

func TestReceiptCallsScopeChangeAfterCommitKeepsIntent(t *testing.T) {
	s := hookSession(t)
	scope := testScope
	options := RuntimeOptions{Scope: func() RuntimeScope { return scope }}
	b, err := s.PrepareReceiptCall(MainLane, options)
	if err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int32
	tool := receiptWriteTool(&writes)
	out, h, err := b.RunToolCall(t.Context(), agent.ToolCall{ID: "committed", Name: tool.Name}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}, AfterToolCall: func(context.Context, agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		scope.AuthEpoch++
		return agent.AfterToolCallResult{}, nil
	}}, 8)
	if !errors.Is(err, ErrScopeMismatch) || h == nil || writes.Load() != 1 || !h.Snapshot()[0].Committed() {
		t.Fatalf("err=%v outcome=%+v handle=%v writes=%d", err, out, h, writes.Load())
	}
	if err := b.Finalize(t.Context()); err == nil {
		t.Fatalf("revoked binding finalized: %v", err)
	}
	status, err := s.ReadReceiptStatus(MainLane, runtimeOptions())
	if err != nil || !status.Pending() {
		t.Fatalf("unfinalized durable intent=%+v err=%v", status, err)
	}
	if _, err := s.PrepareReceiptCall(MainLane, runtimeOptions()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("fresh old-scope binding cleared intent: %v", err)
	}
}

// The storage acknowledgment itself is a revocation boundary: authorization
// can change while a successful atomic append is in progress.
type receiptScopeChangingStorage struct {
	Storage
	afterAppend func()
}

func (s *receiptScopeChangingStorage) CompareAppend(v BranchVersion, entries []NewEntry) (BranchVersion, []Entry, error) {
	next, written, err := s.Storage.CompareAppend(v, entries)
	if err == nil && s.afterAppend != nil {
		s.afterAppend()
	}
	return next, written, err
}

func TestReceiptCallsScopeChangesDuringStorageAcknowledgment(t *testing.T) {
	for _, stage := range []int{1, 2} {
		t.Run(map[int]string{1: "intent ACK", 2: "final ACK"}[stage], func(t *testing.T) {
			memory, err := NewMemoryStorage(Header{ID: "receipt-scope-ack"})
			if err != nil {
				t.Fatal(err)
			}
			scope := testScope
			appends := 0
			storage := &receiptScopeChangingStorage{Storage: memory, afterAppend: func() {
				appends++
				if appends == stage {
					scope.AuthEpoch++
				}
			}}
			s, err := New(storage, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			options := RuntimeOptions{Scope: func() RuntimeScope { return scope }}
			binding, err := s.PrepareReceiptCall(MainLane, options)
			if err != nil {
				t.Fatal(err)
			}
			var writes atomic.Int32
			_, handle, err := receiptRun(t.Context(), binding, receiptWriteTool(&writes))
			if !errors.Is(err, ErrScopeMismatch) || writes.Load() != int32(stage-1) {
				t.Fatalf("stage=%d err=%v writes=%d", stage, err, writes.Load())
			}
			if handle == nil {
				t.Fatal("capture handle lost after acknowledged write")
			}
			if stage == 2 {
				facts := handle.Snapshot()
				if len(facts) != 1 || !facts[0].Committed() || !facts[0].Completed() {
					t.Fatalf("committed memory facts lost: %+v", facts)
				}
			} else if len(handle.Snapshot()) != 0 {
				t.Fatal("intent revocation entered a body")
			}
			if err := binding.Finalize(t.Context()); err == nil {
				t.Fatal("revoked binding finalized")
			}
			if _, err := s.ReadReceiptStatus(MainLane, options); !errors.Is(err, ErrScopeMismatch) {
				t.Fatalf("current epoch read old facts: %v", err)
			}
			if _, err := s.PrepareReceiptCall(MainLane, options); !errors.Is(err, ErrScopeMismatch) {
				t.Fatalf("current epoch took over old call: %v", err)
			}
			old := receiptStatus(t, s, MainLane)
			if old.Pending() != (stage == 1) {
				t.Fatalf("acknowledged storage facts=%+v", old)
			}
			if stage == 1 {
				if _, err := s.PrepareReceiptCall(MainLane, runtimeOptions()); !errors.Is(err, ErrInterrupted) {
					t.Fatalf("old epoch recovered pending intent: %v", err)
				}
			} else {
				facts := old.Facts()
				if len(facts) != 1 || !facts[0].Committed() {
					t.Fatalf("acknowledged final missing commit: %+v", facts)
				}
			}
		})
	}
}
