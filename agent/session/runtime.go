package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/internal/jsontext"
)

const runtimeRecordType = "agent.runtime.v1"

var (
	ErrInterrupted   = errors.New("session: unfinished tool execution; reconcile external effects before starting another run")
	ErrScopeMismatch = errors.New("session: trusted identity or authorization epoch changed")
)

// RuntimeScope comes from the authenticated host, never model messages.
type RuntimeScope struct {
	Identity  string `json:"identity"`
	AuthEpoch uint64 `json:"auth_epoch"`
}

type RuntimeOptions struct {
	// Scope is rechecked on reads and commits. Revocation invalidates the run.
	Scope func() RuntimeScope
}

type runtimeRecord struct {
	Version   int                     `json:"version"`
	SessionID string                  `json:"session_id"`
	Lane      string                  `json:"lane"`
	Scope     RuntimeScope            `json:"scope"`
	Pending   bool                    `json:"pending"`
	State     []agent.ToolStateChange `json:"state,omitempty"`
	// Basic execution facts are host-only data, outside model-facing messages.
	ChildCalls []agent.ChildCallReport `json:"child_calls,omitempty"`
}

type runtimeStateKey struct{ tool, namespace string }

// RunBinding restores one consistent branch snapshot, then owns its conditional
// commits. It is single-use. Create a fresh binding after completion or moving a
// lane; a stale binding never rebases or retries a script.
type RunBinding struct {
	mu                                 sync.Mutex
	session                            *Session
	version                            BranchVersion
	scope                              RuntimeScope
	currentScope                       func() RuntimeScope
	provenance                         string
	snapshot                           agent.AgentSnapshot
	messages                           []agent.Message
	states                             map[runtimeStateKey]json.RawMessage
	pending, initialized, used, failed bool
}

// PrepareRun restores data from the actual branch, including records preceding
// compaction. Forks inherit their ancestor state but subsequently commit to their
// own lane. Existing unjournaled transcripts require explicit host import; this
// method does not grant trust to old tool declarations implicitly.
func (s *Session) PrepareRun(lane string, options RuntimeOptions) (*RunBinding, error) {
	if options.Scope == nil {
		return nil, errors.New("session: trusted scope provider is required")
	}
	scope := options.Scope()
	if strings.TrimSpace(scope.Identity) == "" || len(scope.Identity) > 256 || !utf8.ValidString(scope.Identity) {
		return nil, errors.New("session: invalid trusted identity")
	}
	branch, err := s.ReadBranch(lane)
	if err != nil {
		return nil, err
	}
	b := &RunBinding{session: s, version: branch.Version, scope: scope, currentScope: options.Scope, provenance: newSessionID(), states: make(map[runtimeStateKey]json.RawMessage)}
	for _, entry := range branch.Entries {
		if entry.Custom == nil || entry.Custom.CustomType != runtimeRecordType {
			continue
		}
		var record runtimeRecord
		if len(entry.Custom.Payload) > 16<<20 || decodeStrictJSON(entry.Custom.Payload, &record) != nil || record.Version != 1 || record.SessionID != branch.Version.SessionID || record.Lane != entry.Lane {
			return nil, corruptLog("invalid runtime record %q", entry.ID)
		}
		if record.Scope != scope {
			return nil, ErrScopeMismatch
		}
		if err := b.applyState(record.State); err != nil {
			return nil, err
		}
		b.pending, b.initialized = record.Pending, true
	}
	if !b.initialized && len(branch.Entries) > 0 {
		return nil, errors.New("session: branch has no trusted runtime provenance")
	}
	if b.pending {
		return nil, ErrInterrupted
	}
	effective := contextFromBranch(branch.Entries)
	if _, err := completeTurnStarts(effective.Messages); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInterrupted, err)
	}
	b.messages = effective.Messages
	if effective.Summary != "" {
		// Summary is conversation data, never a system instruction or authority.
		b.messages = append([]agent.Message{agent.NewUserTextMessage("Previous conversation summary:\n" + effective.Summary)}, b.messages...)
	}
	b.snapshot = agent.AgentSnapshot{SessionID: branch.Version.SessionID, Messages: cloneMessages(b.messages), Metadata: map[string]any{"session.runtime.provenance": b.provenance}}
	return b, nil
}

// TrustedScope proves that a snapshot originated from this host-bound branch.
// The ephemeral token is not restored from disk or projected to the model.
func (b *RunBinding) TrustedScope(snapshot agent.AgentSnapshot) (RuntimeScope, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.scope, !b.failed && snapshot.SessionID == b.version.SessionID && snapshot.Metadata["session.runtime.provenance"] == b.provenance && b.currentScope() == b.scope
}

// Run delegates to the existing Runner. Drain Events before Wait; only a
// successful Wait confirms all completed tool batches were durably committed.
func (b *RunBinding) Run(ctx context.Context, runner *agent.Runner, prompts []agent.Message) (*agent.RunStream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if runner == nil || b.used {
		return nil, errors.New("session: runner is nil or binding already used")
	}
	if b.currentScope() != b.scope {
		return nil, ErrScopeMismatch
	}
	b.used = true
	return runner.RunWithHooks(ctx, b.snapshot, prompts, agent.LoopHooks{Journal: b}), nil
}

func (b *RunBinding) LoadState(tool, namespace string) (json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed {
		return nil, errors.New("session: run binding invalidated")
	}
	if b.currentScope() != b.scope {
		return nil, ErrScopeMismatch
	}
	return append(json.RawMessage(nil), b.states[runtimeStateKey{tool, namespace}]...), nil
}

// Record implements agent.RunJournal. A failed commit invalidates this binding;
// final results remain in RunStream.Wait's snapshot for host reconciliation.
func (b *RunBinding) Record(ctx context.Context, record agent.JournalRecord) (err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed {
		return errors.New("session: run binding invalidated")
	}
	defer func() {
		if err != nil {
			b.failed = true
		}
	}()
	if b.currentScope() != b.scope {
		return ErrScopeMismatch
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if len(record.Messages) < len(b.messages) {
		return errors.New("session: journal transcript shrank")
	}
	for i, saved := range b.messages {
		left, e1 := json.Marshal(saved)
		right, e2 := json.Marshal(record.Messages[i])
		if e1 != nil || e2 != nil || !bytes.Equal(left, right) {
			return errors.New("session: journal transcript prefix changed")
		}
	}
	if b.initialized && len(record.Messages) == len(b.messages) && record.Pending == b.pending && len(record.State) == 0 {
		return nil
	}
	// Validate a detached candidate before the storage write.
	candidate := &RunBinding{states: make(map[runtimeStateKey]json.RawMessage, len(b.states))}
	for key, value := range b.states {
		candidate.states[key] = value
	}
	changes := append([]agent.ToolStateChange(nil), record.State...)
	for i := range changes {
		if len(changes[i].Value) > 1<<20 || jsontext.ValidateUnicode(changes[i].Value) != nil {
			return corruptLog("invalid or oversized runtime state")
		}
		// encoding/json escapes HTML characters inside RawMessage values.
		// Enforce restore limits on the exact representation storage will keep.
		changes[i].Value, err = json.Marshal(changes[i].Value)
		if err != nil {
			return err
		}
	}
	if err = candidate.applyState(changes); err != nil {
		return err
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Tool == changes[j].Tool {
			return changes[i].Namespace < changes[j].Namespace
		}
		return changes[i].Tool < changes[j].Tool
	})
	var childCalls []agent.ChildCallReport
	for _, message := range record.Messages[len(b.messages):] {
		if message.ToolResult != nil && message.ToolResult.ChildCalls != nil {
			report := *message.ToolResult.ChildCalls
			// Keep the complete basic ledger, but do not persist optional output
			// summaries or raw error details that may have been withheld by hooks.
			report.DetailsTruncated = report.DetailsTruncated || len(report.Details) > 0
			report.Details = nil
			childCalls = append(childCalls, report)
		}
	}
	data, err := json.Marshal(runtimeRecord{Version: 1, SessionID: b.version.SessionID, Lane: b.version.Lane, Scope: b.scope, Pending: record.Pending, State: changes, ChildCalls: childCalls})
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return errors.New("session: runtime record exceeds byte limit")
	}
	entries := make([]NewEntry, 0, len(record.Messages)-len(b.messages)+1)
	for _, message := range record.Messages[len(b.messages):] {
		entries = append(entries, NewEntry{Type: EntryTypeMessage, Message: &message})
	}
	entries = append(entries, NewEntry{Type: EntryTypeCustom, Custom: &CustomData{CustomType: runtimeRecordType, Payload: data}})
	version, _, err := b.session.CompareAppend(b.version, entries)
	if err != nil {
		return err
	}
	b.version, b.messages, b.states, b.pending, b.initialized = version, cloneMessages(record.Messages), candidate.states, record.Pending, true
	return nil
}

func (b *RunBinding) applyState(changes []agent.ToolStateChange) error {
	if len(changes) > 64 {
		return corruptLog("too many runtime state changes")
	}
	seen := make(map[runtimeStateKey]bool, len(changes))
	for _, change := range changes {
		key := runtimeStateKey{change.Tool, change.Namespace}
		if len(change.Tool) == 0 || len(change.Tool) > 128 || len(change.Namespace) == 0 || len(change.Namespace) > 128 || !utf8.ValidString(change.Tool) || !utf8.ValidString(change.Namespace) || len(change.Value) > 1<<20 || jsontext.ValidateUnicode(change.Value) != nil || seen[key] {
			return corruptLog("invalid or duplicate runtime state")
		}
		seen[key] = true
		b.states[key] = append(json.RawMessage(nil), change.Value...)
	}
	if len(b.states) > 64 {
		return corruptLog("runtime state namespace limit exceeded")
	}
	total := 0
	for key, value := range b.states {
		total += len(key.tool) + len(key.namespace) + len(value)
	}
	if total > 4<<20 {
		return corruptLog("runtime state byte limit exceeded")
	}
	return nil
}
