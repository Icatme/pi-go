package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"
)

// RunJournal is a host-owned commit boundary. Record must atomically persist
// the canonical transcript suffix, Pending marker and accepted state changes,
// or return an error. Errors stop the loop; the engine never retries Record or
// replays tools. Events remain observations, not durable commit receipts.
type RunJournal interface {
	Record(context.Context, JournalRecord) error
	LoadState(tool, namespace string) (json.RawMessage, error)
}

type JournalRecord struct {
	Messages []Message
	Pending  bool
	State    []ToolStateChange
}

// ToolStateChange replaces one bounded, host-namespaced JSON value. It contains
// data only; executors, credentials and restored execution rights belong to the host.
type ToolStateChange struct {
	Tool      string          `json:"tool"`
	Namespace string          `json:"namespace"`
	Value     json.RawMessage `json:"value"`
}

type journalKey struct{}
type stateKey struct{ tool, namespace string }
type journalScope struct {
	journal  RunJournal
	mu       sync.Mutex
	accepted map[stateKey]json.RawMessage
	failed   bool
}

func withJournal(ctx context.Context, journal RunJournal) context.Context {
	return context.WithValue(ctx, journalKey{}, &journalScope{journal: journal, accepted: make(map[stateKey]json.RawMessage)})
}

// ToolState is available only to sequential top-level tools in a journaled run.
// Stage does not commit: the runtime accepts it only after the executor, after
// hook and result validator succeed. The capability closes when that call ends.
type ToolState struct {
	mu     sync.Mutex
	scope  *journalScope
	tool   string
	closed bool
	staged map[string]json.RawMessage
}

func newToolState(ctx context.Context, prepared preparedToolCall) *ToolState {
	scope, _ := ctx.Value(journalKey{}).(*journalScope)
	if scope == nil || scope.journal == nil || prepared.child || prepared.tool.ExecutionMode != ToolExecutionSequential {
		return nil
	}
	return &ToolState{scope: scope, tool: prepared.call.Name, staged: make(map[string]json.RawMessage)}
}

func validStateNamespace(namespace string) bool {
	return len(namespace) > 0 && len(namespace) <= 128 && utf8.ValidString(namespace)
}

func (s *ToolState) Load(namespace string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !validStateNamespace(namespace) {
		return nil, errors.New("tool state: closed capability or invalid namespace")
	}
	// Check the host's current scope even when a preceding call in this batch
	// has staged a newer value. Cached data never bypasses revocation.
	value, err := loadJournalState(s.scope.journal, s.tool, namespace)
	if err != nil {
		return nil, err
	}
	if len(value) > 1<<20 || len(value) > 0 && !json.Valid(value) {
		return nil, errors.New("tool state: invalid or oversized saved value")
	}
	if staged, ok := s.staged[namespace]; ok {
		return append(json.RawMessage(nil), staged...), nil
	}
	s.scope.mu.Lock()
	defer s.scope.mu.Unlock()
	if accepted, ok := s.scope.accepted[stateKey{s.tool, namespace}]; ok {
		return append(json.RawMessage(nil), accepted...), nil
	}
	return append(json.RawMessage(nil), value...), nil
}

func (s *ToolState) Stage(namespace string, value json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !validStateNamespace(namespace) || len(value) > 1<<20 || !json.Valid(value) {
		return errors.New("tool state: closed capability or invalid staged value")
	}
	if _, exists := s.staged[namespace]; !exists && len(s.staged) >= 16 {
		return errors.New("tool state: namespace limit exceeded")
	}
	s.staged[namespace] = append(json.RawMessage(nil), value...)
	return nil
}

func (s *ToolState) finish(accept bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if accept {
		s.scope.mu.Lock()
		defer s.scope.mu.Unlock()
		for namespace, value := range s.staged {
			s.scope.accepted[stateKey{s.tool, namespace}] = value
		}
	}
	s.staged = nil
}

func recordJournal(ctx context.Context, messages []Message, pending bool) (err error) {
	scope, _ := ctx.Value(journalKey{}).(*journalScope)
	if scope == nil || scope.journal == nil {
		return nil
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.failed {
		return errors.New("run journal: previous commit failed")
	}
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("run journal callback: %w", &toolCallbackPanic{value: value})
		}
		if err != nil {
			scope.failed = true
		}
	}()
	record := JournalRecord{Messages: cloneMessages(messages), Pending: pending}
	for key, value := range scope.accepted {
		record.State = append(record.State, ToolStateChange{Tool: key.tool, Namespace: key.namespace, Value: append(json.RawMessage(nil), value...)})
	}
	err = scope.journal.Record(ctx, record)
	if err == nil {
		clear(scope.accepted)
	}
	return err
}

func loadJournalState(journal RunJournal, tool, namespace string) (value json.RawMessage, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("run journal state callback: %w", &toolCallbackPanic{value: v})
		}
	}()
	return journal.LoadState(tool, namespace)
}
