package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/checkpoint"
)

type receiptBranchStorage struct {
	Storage
	mutate func(*BranchSnapshot)
}

func (s *receiptBranchStorage) ReadBranch(lane string) (BranchSnapshot, error) {
	b, e := s.Storage.ReadBranch(lane)
	if e == nil {
		s.mutate(&b)
	}
	return b, e
}
func TestReceiptPairCorruptionIsRejected(t *testing.T) {
	base := hookSession(t)
	var writes atomic.Int32
	if _, _, err := receiptRun(t.Context(), receiptBinding(t, base), receiptWriteTool(&writes)); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"version", "scope", "fence", "intent", "token", "before_head", "before_lane", "before_revision", "phase", "duplicate_final", "incomplete_fact", "bad_parent", "bad_token", "domain", "unknown_field", "missing_intent", "unpaired_runtime", "wrong_pair_sequence"} {
		t.Run(kind, func(t *testing.T) {
			wrapped := &receiptBranchStorage{Storage: base.storage, mutate: func(b *BranchSnapshot) {
				if kind == "missing_intent" {
					b.Entries = b.Entries[2:]
					return
				}
				var r receiptRecord
				json.Unmarshal(b.Entries[3].Custom.Payload, &r)
				switch kind {
				case "version":
					r.Version = 2
				case "scope":
					r.Scope.AuthEpoch++
				case "fence":
					r.FenceID = "wrong"
				case "intent":
					r.IntentID = "wrong"
				case "token":
					r.CallToken = "wrong"
				case "before_head":
					r.Before.Head = "wrong"
				case "before_lane":
					r.Before.Lane = "wrong"
				case "before_revision":
					r.Before.Revision = b.Entries[2].Seq
				case "phase":
					r.Phase = "unknown"
				case "duplicate_final":
					b.Entries = append(b.Entries, b.Entries[2:]...)
				case "incomplete_fact":
					r.Facts[0].Completed = false
				case "bad_parent":
					r.Facts[0].Parent = r.Facts[0].Token
				case "bad_token":
					r.Facts[0].Token = 2
				case "domain":
					r.Facts[0].Domain = "unrecognized"
				case "unknown_field":
					b.Entries[3].Custom.Payload = append(b.Entries[3].Custom.Payload[:len(b.Entries[3].Custom.Payload)-1], []byte(`,"extra":true}`)...)
					return
				case "unpaired_runtime":
					b.Entries = append(b.Entries, b.Entries[2])
					return
				case "wrong_pair_sequence":
					b.Entries[3].Seq++
					return
				}
				b.Entries[3].Custom.Payload, _ = json.Marshal(r)
			}}
			s, err := New(wrapped, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ReadReceiptStatus(MainLane, runtimeOptions()); err == nil {
				t.Fatal("corruption accepted")
			}
			if _, err = s.PrepareReceiptCall(MainLane, runtimeOptions()); err == nil {
				t.Fatal("corruption admitted execution")
			}
		})
	}
}
func TestReceiptTruncatedPhysicalBatchDoesNotResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.jsonl")
	disk, err := CreateJSONLStorage(path, Header{ID: "truncated"})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := New(disk, Options{})
	var writes atomic.Int32
	if _, _, err = receiptRun(context.Background(), receiptBinding(t, s), receiptWriteTool(&writes)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Existing JSONL recovery trims only an unterminated torn final fragment.
	// The complete intent must survive, and neither runner may continue it.
	if err = os.WriteFile(path, raw[:len(raw)-20], 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJSONLStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := New(reopened, Options{})
	defer restored.Close()
	status, err := restored.ReadReceiptStatus(MainLane, runtimeOptions())
	if err != nil || !status.Pending() || len(status.Facts()) != 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if _, err = restored.PrepareReceiptCall(MainLane, runtimeOptions()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("new runner: %v", err)
	}
	if _, err = restored.PrepareRun(MainLane, runtimeOptions()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("old runner: %v", err)
	}
	// Newline-terminated corruption is not the recoverable torn-tail case.
	corruptPath := filepath.Join(t.TempDir(), "corrupt.jsonl")
	damaged := append(append([]byte(nil), raw[:len(raw)-20]...), '\n')
	if err = os.WriteFile(corruptPath, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenJSONLStorage(corruptPath); err == nil {
		bad.Close()
		t.Fatal("terminated corrupted batch accepted")
	}

}
func TestReceiptLegacyRuntimeCannotClearFence(t *testing.T) {
	s := hookSession(t)
	var writes atomic.Int32
	if _, _, err := receiptRun(t.Context(), receiptBinding(t, s), receiptWriteTool(&writes)); err != nil {
		t.Fatal(err)
	}
	branch, _ := s.ReadBranch(MainLane)
	data, _ := json.Marshal(runtimeRecord{Version: 1, SessionID: branch.Version.SessionID, Lane: MainLane, Scope: testScope, Pending: false})
	if _, _, err := s.CompareAppend(branch.Version, []NewEntry{{Type: EntryTypeCustom, Custom: &CustomData{CustomType: runtimeRecordType, Payload: data}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareReceiptCall(MainLane, runtimeOptions()); err == nil {
		t.Fatal("new reader accepted a manually removed fence")
	}
	// Legacy raw storage APIs are trusted host powers, not prevented by this API.
	// Their deliberate removal of the fence is outside the old-reader guarantee.
}
func TestReceiptOldPendingNeverBecomesNewIntent(t *testing.T) {
	s := hookSession(t)
	b, err := s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Record(t.Context(), agent.JournalRecord{Pending: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrepareReceiptCall(MainLane, runtimeOptions()); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("got %v", err)
	}
}

func TestReceiptHistoryCapacityReservedBeforeIntent(t *testing.T) {
	for _, which := range []string{"records", "bytes"} {
		t.Run(which, func(t *testing.T) {
			s := hookSession(t)
			b := receiptBinding(t, s)
			if which == "records" {
				b.status.count = maxReceiptHistoryRecords - 1
			} else {
				b.status.bytes = maxReceiptHistoryBytes - 2*maxReceiptRecordBytes + 1
			}
			before, _ := s.ReadBranch(MainLane)
			var writes atomic.Int32
			if _, _, err := receiptRun(t.Context(), b, receiptWriteTool(&writes)); err == nil {
				t.Fatal("unreserved history capacity admitted")
			}
			after, _ := s.ReadBranch(MainLane)
			if writes.Load() != 0 || after.Version != before.Version {
				t.Fatal("capacity failure changed storage or invoked body")
			}
		})
	}
}
func TestConcurrentFinalizeWritesExactlyOneFinal(t *testing.T) {
	s := hookSession(t)
	b := receiptBinding(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	var writes atomic.Int32
	tool := receiptWriteTool(&writes)
	out, h, err := b.RunToolCall(ctx, agent.ToolCall{ID: "once", Name: tool.Name}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}, AfterToolCall: func(context.Context, agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		cancel()
		return agent.AfterToolCallResult{}, nil
	}}, 8)
	_ = out
	if !errors.Is(err, context.Canceled) || h == nil || writes.Load() != 1 {
		t.Fatalf("err=%v handle=%v writes=%d", err, h, writes.Load())
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.Finalize(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	branch, _ := s.ReadBranch(MainLane)
	if len(branch.Entries) != 4 || receiptStatus(t, s, MainLane).Pending() {
		t.Fatal("final duplicated or not persisted")
	}
}

func TestReceiptReadsLegacyHistoryAcrossCompaction(t *testing.T) {
	s := hookSession(t)
	old, err := s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	messages := []agent.Message{agent.NewUserTextMessage("old request"), agent.NewTextMessage(agent.RoleAssistant, "old answer")}
	if err = old.Record(t.Context(), agent.JournalRecord{Messages: messages}); err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int32
	if _, _, err = receiptRun(t.Context(), receiptBinding(t, s), receiptWriteTool(&writes)); err != nil {
		t.Fatal(err)
	}
	token := receiptStatus(t, s, MainLane).CallToken()
	if _, err = s.AppendCompaction(MainLane, CompactionData{Summary: "previous work", RetainedTail: messages}); err != nil {
		t.Fatal(err)
	}
	status := receiptStatus(t, s, MainLane)
	if status.Pending() || status.CallToken() != token || len(status.Facts()) != 1 || !status.Facts()[0].Committed() {
		t.Fatal("compaction lost receipts")
	}
	if _, err = s.PrepareReceiptCall(MainLane, runtimeOptions()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrepareRun(MainLane, runtimeOptions()); !errors.Is(err, ErrInterrupted) {
		t.Fatal("compaction cleared legacy fence")
	}
}
func TestReceiptNativeCodemodeLeafPersistsDespiteOuterHookFailure(t *testing.T) {
	s := hookSession(t)
	sandbox := runtimeSandbox(t)
	var writes atomic.Int32
	leaf := receiptWriteTool(&writes)
	container := runtimeTool(t, sandbox, leaf.Execute)
	outcome, h, err := receiptBinding(t, s).RunToolCall(t.Context(), agent.ToolCall{ID: "script", Name: container.Name, ParsedArgs: map[string]any{"code": "await tools.write({});"}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{container}, AfterToolCall: func(_ context.Context, c agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		if c.ToolCall.Name == container.Name {
			return agent.AfterToolCallResult{}, errors.New("parent result withheld")
		}
		return agent.AfterToolCallResult{}, nil
	}}, 3)
	if err != nil || outcome.Err == nil || h == nil || writes.Load() != 1 {
		t.Fatalf("err=%v outcome=%+v writes=%d", err, outcome, writes.Load())
	}
	facts := receiptStatus(t, s, MainLane).Facts()
	if len(facts) != 3 {
		t.Fatalf("facts=%+v", facts)
	}
	commits := 0
	for _, f := range facts {
		if f.Committed() {
			commits++
			if f.Domain() != "agent" || f.CallID() != "script/1" {
				t.Fatal("commit attributed to wrong invocation")
			}
		}
	}
	if commits != 1 {
		t.Fatal("leaf commit missing or duplicated")
	}
}

func TestCheckpointCannotStartSessionReceiptCall(t *testing.T) {
	s := hookSession(t)
	binding := receiptBinding(t, s)
	before, _ := s.ReadBranch(MainLane)
	var writes atomic.Int32
	leaf := receiptWriteTool(&writes)
	requests := 0
	probed := false
	probe := agent.ToolDefinition{Name: "probe", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		probed = true
		_, h, err := receiptRun(ctx, binding, leaf)
		if err == nil || h != nil {
			t.Error("checkpoint entered session receipt mode")
		}
		return agent.ToolResult{}, nil
	}}
	definition := agent.AgentDefinition{Tools: []agent.ToolDefinition{probe}, Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		requests++
		m := agent.NewTextMessage(agent.RoleAssistant, "done")
		m.StopReason = agent.StopReasonStop
		if requests == 1 {
			m.Parts = nil
			m.StopReason = agent.StopReasonToolUse
			m.ToolCalls = []agent.ToolCall{{ID: "probe", Name: probe.Name, Arguments: json.RawMessage(`{}`)}}
		}
		return runtimeReply(m), nil
	})}
	runner, err := checkpoint.NewRunner(checkpoint.RunnerConfig{Definition: definition, DefinitionVersion: "receipt-rejection-v1", Store: checkpoint.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	stream := runner.Run(t.Context(), "cp", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("probe")})
	for range stream.Events() {
	}
	outcome, err := stream.Wait()
	if err != nil || !outcome.Persisted || !probed || requests != 2 {
		t.Fatalf("outcome=%+v err=%v probed=%v requests=%d", outcome, err, probed, requests)
	}
	after, _ := s.ReadBranch(MainLane)
	if writes.Load() != 0 || after.Version != before.Version {
		t.Fatal("unsupported checkpoint mode wrote receipt intent or business data")
	}
}
