package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/internal/receiptcapture"
)

const receiptRecordType = "agent.direct_receipts.v1"
const maxReceiptRecordBytes = 8 << 20
const maxReceiptHistoryBytes = 32 << 20
const maxReceiptHistoryRecords = 4096

type receiptFactWire struct {
	Token     uint64 `json:"token"`
	Parent    uint64 `json:"parent"`
	Domain    string `json:"domain"`
	CallID    string `json:"call_id"`
	ToolName  string `json:"tool_name"`
	Committed bool   `json:"committed"`
	Completed bool   `json:"completed"`
}
type receiptRecord struct {
	Version   int               `json:"version"`
	Before    BranchVersion     `json:"before"`
	Scope     RuntimeScope      `json:"scope"`
	FenceID   string            `json:"fence_id"`
	IntentID  string            `json:"intent_id"`
	CallToken string            `json:"call_token"`
	Phase     string            `json:"phase"`
	Limit     int               `json:"limit"`
	Facts     []receiptFactWire `json:"facts,omitempty"`
}

// ReceiptFact is an immutable storage-acknowledged observation. Committed=false
// means unknown, not absence of effects. It does not prove disk durability.
type ReceiptFact struct{ fact receiptFactWire }

func (f ReceiptFact) Token() uint64       { return f.fact.Token }
func (f ReceiptFact) ParentToken() uint64 { return f.fact.Parent }
func (f ReceiptFact) Domain() string      { return f.fact.Domain }
func (f ReceiptFact) CallID() string      { return f.fact.CallID }
func (f ReceiptFact) ToolName() string    { return f.fact.ToolName }
func (f ReceiptFact) Committed() bool     { return f.fact.Committed }
func (f ReceiptFact) Completed() bool     { return f.fact.Completed }

// ReceiptStatus is a fresh branch read, not an external-effect reconciliation.
// Pending is retained after an unresolved intent, including a crash. A final
// status says bodies exited, not that the operation succeeded or is retryable.
type ReceiptStatus struct {
	version      BranchVersion
	pending      bool
	token        string
	facts        []receiptFactWire
	count, bytes int
}

func (s ReceiptStatus) Version() BranchVersion { return s.version }
func (s ReceiptStatus) Pending() bool          { return s.pending }
func (s ReceiptStatus) CallToken() string      { return s.token }
func (s ReceiptStatus) Facts() []ReceiptFact {
	out := make([]ReceiptFact, len(s.facts))
	for i, f := range s.facts {
		out[i] = ReceiptFact{f}
	}
	return out
}
func (ReceiptFact) MarshalJSON() ([]byte, error) {
	return nil, errors.New("session: use receipt accessors; no public receipt encoding")
}
func (*ReceiptFact) UnmarshalJSON([]byte) error {
	return errors.New("session: receipts can only be restored by the session reader")
}
func (ReceiptStatus) MarshalJSON() ([]byte, error) {
	return nil, errors.New("session: use receipt accessors; no public status encoding")
}
func (*ReceiptStatus) UnmarshalJSON([]byte) error {
	return errors.New("session: status can only be restored by the session reader")
}

// ReceiptCallBinding owns one direct call, not a model loop or conversation
// transcript. Its intent is acknowledged before the tool body can enter. It is
// single-use and never retries a call or rebases a failed conditional write.
type ReceiptCallBinding struct {
	mu                               sync.Mutex
	session                          *Session
	version                          BranchVersion
	scope                            RuntimeScope
	currentScope                     func() RuntimeScope
	status                           ReceiptStatus
	token, intentID                  string
	limit                            int
	handle                           *executionreceipt.Handle
	used, running, failed, finalized bool
}

// PrepareReceiptCall restores a trusted branch and refuses unresolved intents.
// Old sessions remain readable. Old PrepareRun cannot continue after the first
// receipt intent: every receipt pair retains its legacy Pending:true fence.
func (s *Session) PrepareReceiptCall(lane string, options RuntimeOptions) (*ReceiptCallBinding, error) {
	status, scope, err := s.readReceiptStatus(lane, options)
	if err != nil {
		return nil, err
	}
	if status.pending {
		return nil, ErrInterrupted
	}
	return &ReceiptCallBinding{session: s, version: status.version, scope: scope, currentScope: options.Scope, status: status}, nil
}

// ReadReceiptStatus inspects even unresolved intents without clearing them.
// No argument, file content, raw tool result or error text is retained.
func (s *Session) ReadReceiptStatus(lane string, options RuntimeOptions) (ReceiptStatus, error) {
	status, _, err := s.readReceiptStatus(lane, options)
	return status, err
}

func (s *Session) readReceiptStatus(lane string, options RuntimeOptions) (ReceiptStatus, RuntimeScope, error) {
	fail := func(err error) (ReceiptStatus, RuntimeScope, error) { return ReceiptStatus{}, RuntimeScope{}, err }
	if options.Scope == nil {
		return fail(errors.New("session: trusted scope provider is required"))
	}
	scope := options.Scope()
	if strings.TrimSpace(scope.Identity) == "" || len(scope.Identity) > 256 || !utf8.ValidString(scope.Identity) {
		return fail(errors.New("session: invalid trusted identity"))
	}
	branch, err := s.ReadBranch(lane)
	if err != nil {
		return fail(err)
	}
	status := ReceiptStatus{version: branch.Version}
	initialized, receiptMode := false, false
	var intent *receiptRecord
	seen := map[string]bool{}
	state := &RunBinding{states: make(map[runtimeStateKey]json.RawMessage)}
	for i := 0; i < len(branch.Entries); i++ {
		entry := branch.Entries[i]
		if entry.Custom == nil {
			continue
		}
		if entry.Custom.CustomType == receiptRecordType {
			return fail(corruptLog("orphan receipt record"))
		}
		if entry.Custom.CustomType != runtimeRecordType {
			continue
		}
		var legacy runtimeRecord
		if len(entry.Custom.Payload) > 16<<20 || decodeStrictJSON(entry.Custom.Payload, &legacy) != nil || legacy.Version != 1 || legacy.SessionID != branch.Version.SessionID || legacy.Lane != entry.Lane {
			return fail(corruptLog("invalid receipt runtime fence"))
		}
		if legacy.Scope != scope {
			return fail(ErrScopeMismatch)
		}
		if err = state.applyState(legacy.State); err != nil {
			return fail(err)
		}
		initialized = true
		paired := i+1 < len(branch.Entries) && branch.Entries[i+1].Custom != nil && branch.Entries[i+1].Custom.CustomType == receiptRecordType
		if !paired {
			if receiptMode {
				return fail(corruptLog("unpaired runtime record after receipt history"))
			}
			status.pending = legacy.Pending
			continue
		}
		next := branch.Entries[i+1]
		i++
		raw := next.Custom.Payload
		status.count++
		status.bytes += len(raw)
		if len(raw) > maxReceiptRecordBytes || status.count > maxReceiptHistoryRecords || status.bytes > maxReceiptHistoryBytes {
			return fail(corruptLog("receipt history exceeds limits"))
		}
		var record receiptRecord
		if decodeStrictJSON(raw, &record) != nil || record.Version != 1 || !legacy.Pending || len(legacy.State) != 0 || len(legacy.ChildCalls) != 0 || next.ParentID != entry.ID || next.Lane != entry.Lane || next.Seq != entry.Seq+1 || record.FenceID != entry.ID || record.Before.SessionID != branch.Version.SessionID || record.Before.Lane != entry.Lane || record.Before.Head != entry.ParentID || record.Before.Revision >= entry.Seq || record.Scope != scope || record.Limit < 1 || record.Limit > 128 || len(record.CallToken) == 0 || len(record.CallToken) > 128 || !utf8.ValidString(record.CallToken) {
			return fail(corruptLog("invalid receipt pairing"))
		}
		if record.Phase == "intent" {
			if status.pending || intent != nil || seen[record.CallToken] || record.IntentID != next.ID || len(record.Facts) != 0 {
				return fail(corruptLog("invalid or duplicate receipt intent"))
			}
			seen[record.CallToken] = true
			copy := record
			intent = &copy
			status.pending = true
			status.token = record.CallToken
			status.facts = nil
		} else if record.Phase == "final" {
			if intent == nil || record.IntentID != intent.IntentID || record.CallToken != intent.CallToken || record.Limit != intent.Limit || record.Before.Lane != intent.Before.Lane || len(record.Facts) > record.Limit {
				return fail(corruptLog("unmatched receipt final"))
			}
			for j, f := range record.Facts {
				if f.Token != uint64(j+1) || f.Parent >= f.Token || !f.Completed || (f.Domain != "agent" && f.Domain != "codemode") || len(f.CallID) > 4096 || len(f.ToolName) > 4096 || !utf8.ValidString(f.CallID) || !utf8.ValidString(f.ToolName) {
					return fail(corruptLog("invalid receipt fact"))
				}
			}
			status.pending = false
			status.facts = append([]receiptFactWire(nil), record.Facts...)
			intent = nil
		} else {
			return fail(corruptLog("unsupported receipt phase"))
		}
		receiptMode = true
	}
	if !initialized && len(branch.Entries) > 0 {
		return fail(errors.New("session: branch has no trusted runtime provenance"))
	}
	if _, err = completeTurnStarts(contextFromBranch(branch.Entries).Messages); err != nil {
		return fail(ErrInterrupted)
	}
	if options.Scope() != scope {
		return fail(ErrScopeMismatch)
	}
	return status, scope, nil
}

// RunToolCall writes an intent before execution, then attempts Finalize. The
// third return value reports capture/persistence failure independently of the
// tool outcome. Always retain the handle if non-nil. A failed acknowledgment
// does not prove a batch was absent. No business transcript messages are added.
func (b *ReceiptCallBinding) RunToolCall(ctx context.Context, call agent.ToolCall, options agent.RunToolCallOptions, maxCalls int) (agent.ToolCallOutcome, *executionreceipt.Handle, error) {
	b.mu.Lock()
	if b.used || b.failed {
		b.mu.Unlock()
		return agent.ToolCallOutcome{}, nil, errors.New("session: receipt binding already used or invalidated")
	}
	if maxCalls < 1 || maxCalls > 128 || b.status.count+2 > maxReceiptHistoryRecords || b.status.bytes+2*maxReceiptRecordBytes > maxReceiptHistoryBytes {
		b.mu.Unlock()
		return agent.ToolCallOutcome{}, nil, errors.New("session: receipt capacity exceeds limits")
	}
	runCtx, h, err := receiptcapture.Begin(ctx, maxCalls)
	if err != nil {
		b.mu.Unlock()
		return agent.ToolCallOutcome{}, nil, err
	}
	b.used = true
	b.handle = h
	b.limit = maxCalls
	b.token = newSessionID()
	err = b.appendReceipt(runCtx, "intent", nil)
	if err != nil {
		receiptcapture.CloseRegistration(h)
		b.mu.Unlock()
		return agent.ToolCallOutcome{}, h, err
	}
	b.running = true
	b.mu.Unlock()
	previousPermission := options.CheckToolPermission
	options.CheckToolPermission = func(ctx context.Context, call agent.BeforeToolCallContext) error {
		if b.currentScope() != b.scope {
			return ErrScopeMismatch
		}
		if previousPermission != nil {
			if err := previousPermission(ctx, call); err != nil {
				return err
			}
		}
		if b.currentScope() != b.scope {
			return ErrScopeMismatch
		}
		return nil
	}
	outcome := func() agent.ToolCallOutcome {
		defer func() { receiptcapture.CloseRegistration(h); b.mu.Lock(); b.running = false; b.mu.Unlock() }()
		return agent.RunToolCall(runCtx, call, options)
	}()
	return outcome, h, b.Finalize(ctx)
}

// Finalize waits for the original in-process handle to close, then conditionally
// records it. It never accepts caller-supplied facts, resumes a crashed process,
// reruns a tool or clears the legacy fence. Cancellation of this wait can be
// retried with a fresh context while this same binding remains valid. Scope or
// storage failures invalidate the binding; reopen for inspection, not replay.
func (b *ReceiptCallBinding) Finalize(ctx context.Context) error {
	b.mu.Lock()
	if b.failed || !b.used || b.running || b.handle == nil {
		b.mu.Unlock()
		return errors.New("session: receipt binding cannot finalize")
	}
	if b.currentScope() != b.scope {
		b.failed = true
		b.mu.Unlock()
		return ErrScopeMismatch
	}
	if b.finalized {
		b.mu.Unlock()
		return nil
	}
	h := b.handle
	b.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := h.Wait(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed {
		return errors.New("session: receipt binding invalidated")
	}
	if b.currentScope() != b.scope {
		b.failed = true
		return ErrScopeMismatch
	}
	if b.finalized {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	facts := h.Snapshot()
	wire := make([]receiptFactWire, len(facts))
	for i, f := range facts {
		wire[i] = receiptFactWire{f.Token(), f.ParentToken(), f.Domain(), f.CallID(), f.ToolName(), f.Committed(), f.Completed()}
	}
	if err := b.appendReceipt(ctx, "final", wire); err != nil {
		return err
	}
	b.finalized = true
	return nil
}

// appendReceipt runs under b.mu. Every failure conservatively invalidates this
// binding: storage may have accepted a batch whose acknowledgment was lost.
func (b *ReceiptCallBinding) appendReceipt(ctx context.Context, phase string, facts []receiptFactWire) (err error) {
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
	fenceID, err := b.session.allocateID()
	if err != nil {
		return err
	}
	recordID, err := b.session.allocateID()
	if err != nil {
		return err
	}
	intentID := b.intentID
	if phase == "intent" {
		intentID = recordID
	}
	data, err := json.Marshal(receiptRecord{Version: 1, Before: b.version, Scope: b.scope, FenceID: fenceID, IntentID: intentID, CallToken: b.token, Phase: phase, Limit: b.limit, Facts: facts})
	if err != nil {
		return err
	}
	if len(data) > maxReceiptRecordBytes {
		return errors.New("session: receipt record exceeds byte limit")
	}
	fence, err := json.Marshal(runtimeRecord{Version: 1, SessionID: b.version.SessionID, Lane: b.version.Lane, Scope: b.scope, Pending: true})
	if err != nil {
		return err
	}
	if b.currentScope() != b.scope {
		return ErrScopeMismatch
	}
	version, _, err := b.session.CompareAppend(b.version, []NewEntry{
		{ID: fenceID, Type: EntryTypeCustom, Custom: &CustomData{CustomType: runtimeRecordType, Payload: fence}},
		{ID: recordID, Type: EntryTypeCustom, Custom: &CustomData{CustomType: receiptRecordType, Payload: data}},
	})
	if err != nil {
		return err
	}
	b.version = version
	b.intentID = intentID
	// Revocation cannot be atomic with storage. Preserve the written batch, but
	// do not enter/continue the operation under a changed authorization epoch.
	if b.currentScope() != b.scope {
		return ErrScopeMismatch
	}
	return nil
}
