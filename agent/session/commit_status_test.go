package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func statusBinding(t *testing.T, s *Session) *RunBinding {
	t.Helper()
	b, err := s.PrepareRun(MainLane, runtimeOptions())
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func readStatus(t *testing.T, b *RunBinding) CommitStatus {
	t.Helper()
	status, err := b.CommitStatus()
	if err != nil {
		t.Fatal(err)
	}
	return status
}
func statusTurn(text string) []agent.Message {
	return []agent.Message{agent.NewUserTextMessage(text), agent.NewTextMessage(agent.RoleAssistant, "answer")}
}

func TestCommitStatusInputAcknowledgedBeforeModelFailure(t *testing.T) {
	s := hookSession(t)
	b := statusBinding(t, s)
	initial := readStatus(t, b)
	if initial.Initialized || initial.Invalidated || initial.Commits != 0 || len(initial.Messages) != 0 {
		t.Fatalf("initial=%+v", initial)
	}
	cause := errors.New("model unavailable")
	runner, err := agent.NewRunner(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		status, statusErr := b.CommitStatus()
		if statusErr != nil {
			return nil, statusErr
		}
		if !status.Initialized || status.Commits != 1 || status.Pending || !reflect.DeepEqual(hookText(status.Messages), []string{"input"}) {
			return nil, errors.New("input not acknowledged before model")
		}
		return nil, cause
	})})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := b.Run(t.Context(), runner, []agent.Message{agent.NewUserTextMessage("input")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = drainHooks(t, stream)
	if !errors.Is(err, cause) {
		t.Fatal(err)
	}
	status := readStatus(t, b)
	if status.Invalidated || status.Commits != 1 || len(status.Messages) != 1 {
		t.Fatalf("status=%+v", status)
	}
}

type lostAckStorage struct {
	Storage
	calls  int
	failAt int
	cause  error
}

func (s *lostAckStorage) CompareAppend(v BranchVersion, e []NewEntry) (BranchVersion, []Entry, error) {
	version, entries, err := s.Storage.CompareAppend(v, e)
	if err != nil {
		return version, entries, err
	}
	s.calls++
	if s.calls == s.failAt {
		return BranchVersion{}, nil, s.cause
	}
	return version, entries, nil
}
func TestCommitStatusLostAckRetainsLastConfirmedPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lost-ack.jsonl")
	disk, err := CreateJSONLStorage(path, Header{ID: "lost-ack"})
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("acknowledgment lost")
	storage := &lostAckStorage{Storage: disk, failAt: 2, cause: cause}
	s, err := New(storage, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	b := statusBinding(t, s)
	first := statusTurn("confirmed")
	if err := b.Record(t.Context(), agent.JournalRecord{Messages: first}); err != nil {
		t.Fatal(err)
	}
	before := readStatus(t, b)
	next := append(append([]agent.Message(nil), first...), statusTurn("actually written but unacknowledged")...)
	if err := b.Record(t.Context(), agent.JournalRecord{Messages: next}); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	after := readStatus(t, b)
	if after.Version != before.Version || after.Commits != 1 || !after.Invalidated || !reflect.DeepEqual(after.Messages, before.Messages) {
		t.Fatalf("ack advanced: %+v", after)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJSONLStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = New(reopened, Options{})
	if err != nil {
		t.Fatal(err)
	}
	restored := readStatus(t, statusBinding(t, s))
	if restored.Version == before.Version || restored.Commits != 0 || restored.Invalidated || !restored.Initialized || !reflect.DeepEqual(hookText(restored.Messages), []string{"confirmed", "actually written but unacknowledged"}) {
		t.Fatalf("restored=%+v", restored)
	}
}
func TestCommitStatusScopeAndReadErrorDoNotMutateBinding(t *testing.T) {
	s := hookSession(t)
	scope := testScope
	b, err := s.PrepareRun(MainLane, RuntimeOptions{Scope: func() RuntimeScope { return scope }})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Record(t.Context(), agent.JournalRecord{Messages: statusTurn("secret")}); err != nil {
		t.Fatal(err)
	}
	scope.AuthEpoch++
	status, err := b.CommitStatus()
	if !errors.Is(err, ErrScopeMismatch) || !reflect.DeepEqual(status, CommitStatus{}) || b.failed {
		t.Fatalf("status=%+v err=%v invalidated=%v", status, err, b.failed)
	}
	scope.AuthEpoch--
	// Simulate an internally non-JSON value to exercise read-error policy only.
	b.messages[0].Metadata = map[string]any{"unsupported": make(chan int)}
	status, err = b.CommitStatus()
	if err == nil || !reflect.DeepEqual(status, CommitStatus{}) || b.failed {
		t.Fatalf("serialization err=%v status=%+v", err, status)
	}
	b.messages[0].Metadata = nil
	if status := readStatus(t, b); status.Commits != 1 || status.Invalidated {
		t.Fatal(status)
	}
}
func TestCommitStatusJSONProjectionIsDetached(t *testing.T) {
	s := hookSession(t)
	b := statusBinding(t, s)
	messages := statusTurn("input")
	messages[0].Metadata = map[string]any{"typed": map[string][]string{"value": {"original"}}}
	messages = append(messages, agent.Message{Role: agent.RoleTool, ToolResult: &agent.ToolResultPayload{ToolCallID: "x", Details: map[string]any{"nested": []any{"original"}}, Execution: &agent.ToolExecutionInfo{Local: agent.ToolLocalReturned, Attempts: []agent.ToolSendAttempt{{Number: 1}}}, Failure: &agent.ToolFailure{Reason: "original"}, ChildCalls: &agent.ChildCallReport{}}})
	if err := b.Record(t.Context(), agent.JournalRecord{Messages: messages}); err != nil {
		t.Fatal(err)
	}
	before := readStatus(t, b)
	altered := readStatus(t, b)
	altered.Messages[0].Parts[0].Text = "changed"
	altered.Messages[0].Metadata["typed"].(map[string]any)["value"].([]any)[0] = "changed"
	result := altered.Messages[2].ToolResult
	result.Execution.Local = agent.ToolLocalNotStarted
	result.Execution.Attempts[0].Number = 99
	result.Failure.Reason = "changed"
	result.Details.(map[string]any)["nested"].([]any)[0] = "changed"
	if result.ChildCalls != nil {
		t.Fatal("Go-only field leaked")
	}
	if got := readStatus(t, b); !reflect.DeepEqual(got, before) {
		t.Fatal("status mutation reached binding")
	}
}
func TestCommitStatusConcurrentReadersAndRecords(t *testing.T) {
	s := hookSession(t)
	b := statusBinding(t, s)
	var wg sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var previous uint64
			for i := 0; i < 100; i++ {
				status, err := b.CommitStatus()
				if err != nil {
					t.Error(err)
					return
				}
				if status.Commits < previous {
					t.Error("nonmonotonic status")
				}
				previous = status.Commits
				if len(status.Messages) > 0 {
					status.Messages[0].Parts[0].Text = "external mutation"
				}
			}
		}()
	}
	var messages []agent.Message
	for i := 0; i < 20; i++ {
		messages = append(messages, statusTurn(fmt.Sprintf("input-%d", i))...)
		if err := b.Record(t.Context(), agent.JournalRecord{Messages: messages}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	status := readStatus(t, b)
	if status.Commits != 20 || status.Messages[0].Parts[0].Text != "input-0" {
		t.Fatal(status)
	}
	// A no-op journal record is not another storage acknowledgment.
	if err := b.Record(t.Context(), agent.JournalRecord{Messages: messages}); err != nil {
		t.Fatal(err)
	}
	if readStatus(t, b).Commits != 20 {
		t.Fatal("counted no-op")
	}
}
func TestCommitStatusPendingAndCompactedBaseline(t *testing.T) {
	s := hookSession(t)
	b := statusBinding(t, s)
	messages := append(statusTurn("one"), statusTurn("two")...)
	if err := b.Record(t.Context(), agent.JournalRecord{Messages: messages, Pending: true}); err != nil {
		t.Fatal(err)
	}
	if !readStatus(t, b).Pending {
		t.Fatal("missing pending intent")
	}
	if err := b.Record(t.Context(), agent.JournalRecord{Messages: messages}); err != nil {
		t.Fatal(err)
	}
	if readStatus(t, b).Pending {
		t.Fatal("pending did not settle")
	}
	branch, err := s.ReadBranch(MainLane)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareCompaction(branch.Entries, CompactionOptions{KeepRecentTokens: 1, EstimateTokens: func(agent.Message) int64 { return 1 }})
	if err != nil || plan == nil {
		t.Fatalf("plan=%v err=%v", plan, err)
	}
	compacted, err := Compact(t.Context(), *plan, func(context.Context, SummaryRequest) (SummaryResult, error) {
		return SummaryResult{Summary: "host summary"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	version, _, err := s.CompareAppend(branch.Version, []NewEntry{{Type: EntryTypeCompaction, Compaction: &compacted}})
	if err != nil {
		t.Fatal(err)
	}
	restored := readStatus(t, statusBinding(t, s))
	if restored.Version != version || restored.Commits != 0 || !restored.Initialized || restored.Invalidated || len(restored.Messages) == 0 || restored.Messages[0].Parts[0].Text != "Previous conversation summary:\nhost summary" {
		t.Fatalf("restored=%+v", restored)
	}
	actual, _ := s.ReadBranch(MainLane)
	for _, entry := range actual.Entries {
		if entry.Message != nil && len(entry.Message.Parts) > 0 && entry.Message.Parts[0].Text == restored.Messages[0].Parts[0].Text {
			t.Fatal("fixture unexpectedly stored synthetic summary as input")
		}
	}
	_, err = json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCommitStatusDoesNotFollowOriginalRecordMutation(t *testing.T) {
	s := hookSession(t)
	b := statusBinding(t, s)
	original := statusTurn("confirmed")
	original[0].Metadata = map[string]any{"typed": map[string][]string{"values": {"confirmed"}}}
	original[1].ToolResult = &agent.ToolResultPayload{ToolCallID: "fixture", Execution: &agent.ToolExecutionInfo{Local: agent.ToolLocalReturned}, Failure: &agent.ToolFailure{Reason: "confirmed"}}
	if err := b.Record(t.Context(), agent.JournalRecord{Messages: original}); err != nil {
		t.Fatal(err)
	}
	confirmed := readStatus(t, b)
	original[0].Metadata["typed"].(map[string][]string)["values"][0] = "never acknowledged"
	original[1].ToolResult.Execution.Local = agent.ToolLocalNotStarted
	original[1].ToolResult.Failure.Reason = "never acknowledged"
	if status := readStatus(t, b); !reflect.DeepEqual(status, confirmed) {
		t.Fatalf("last acknowledgment drifted after caller mutation: %+v", status)
	}
}

type statusChangingJSON struct{ calls *int }

func (value statusChangingJSON) MarshalJSON() ([]byte, error) {
	*value.calls++
	return []byte(fmt.Sprintf("%d", *value.calls)), nil
}
func TestCommitStatusUsesOneJSONCandidateForStorageAndAcknowledgment(t *testing.T) {
	s := hookSession(t)
	b := statusBinding(t, s)
	calls := 0
	messages := statusTurn("input")
	messages[0].Metadata = map[string]any{"changing": statusChangingJSON{calls: &calls}}
	if err := b.Record(t.Context(), agent.JournalRecord{Messages: messages}); err != nil {
		t.Fatal(err)
	}
	status := readStatus(t, b)
	branch, err := s.ReadBranch(MainLane)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("custom marshaler invoked %d times", calls)
	}
	var stored *agent.Message
	for _, entry := range branch.Entries {
		if entry.Message != nil && entry.Message.Role == agent.RoleUser {
			stored = entry.Message
			break
		}
	}
	if stored == nil || !reflect.DeepEqual(stored.Metadata, status.Messages[0].Metadata) {
		t.Fatalf("storage/status diverged: stored=%+v status=%+v", stored, status.Messages[0])
	}
}
