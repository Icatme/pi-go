package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Icatme/pi-go/internal/jsontext"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	ErrRawSnapshotUnavailable = errors.New("raw_snapshot_unavailable: no observed response is bound to this SDK result")
	ErrWireLimit              = errors.New("wire_limit: MCP observation bound exceeded")
	ErrWireReplay             = errors.New("wire_replay_blocked: MCP request has already been sent")
	ErrWireInvalidated        = errors.New("raw_snapshot_invalidated: MCP cache changed during request")
	ErrObserverClosed         = errors.New("MCP wire observer is closed")
	ErrDispatchDenied         = errors.New("MCP dispatch denied")
)

// DispatchDeniedError identifies a host denial before transport handoff. Its
// text does not expose a policy callback's potentially sensitive explanation.
type DispatchDeniedError struct{ Err error }

func (*DispatchDeniedError) Error() string        { return ErrDispatchDenied.Error() }
func (e *DispatchDeniedError) Unwrap() error      { return e.Err }
func (*DispatchDeniedError) Is(target error) bool { return target == ErrDispatchDenied }

type dispatchCheckKey struct{}

// WithDispatchCheck attaches a trusted, synchronous host authorization check
// immediately before a byte-boundary handoff. Checks compose in attachment
// order and run once, outside observer locks; they cannot authorize replay.
func WithDispatchCheck(ctx context.Context, check func(context.Context) error) context.Context {
	if check == nil {
		return ctx
	}
	previous, _ := ctx.Value(dispatchCheckKey{}).(func(context.Context) error)
	combined := func(current context.Context) error {
		if previous != nil {
			if err := previous(current); err != nil {
				return err
			}
		}
		return check(current)
	}
	return context.WithValue(ctx, dispatchCheckKey{}, combined)
}

// WireLimits bound frame buffering, in-flight requests, retained raw responses,
// and cancelled/completed request tombstones. Zero values select the defaults.
// A negative value also selects the default; bounds cannot be disabled.
type WireLimits struct {
	MaxFrameBytes int
	MaxPending    int
	MaxBindings   int
	MaxRawBytes   int
	MaxTombstones int
}

const wireTokenKey = "dev.pi-go/wire-call"

var observerSequence atomic.Uint64

type wirePending struct {
	method   string
	id       string
	sent     bool
	raw      json.RawMessage
	err      error
	cancel   context.CancelFunc
	ctx      context.Context
	trace    *wireTrace
	uri      string
	check    func(context.Context) error
	claiming bool
}

// DispatchRecord contains bounded execution facts, never arguments, results,
// credentials or headers. Attempts counts accepted byte-boundary handoffs;
// it does not prove socket delivery or whether a remote operation executed.
// ResultType is set only after the SDK returned a trusted successful result.
type DispatchRecord struct {
	LogicalID        string
	RPCID            string
	Method           string
	Attempts         int
	ResponseReceived bool
	RPCError         bool
	ResultType       string
}
type wireTraceKey struct{}
type wireTrace struct {
	observer *Observer
	value    DispatchRecord
}

// Track creates one logical-operation trace. The snapshot remains readable
// after cancellation or Close, and returns a detached value. The context must
// be used for only one SDK operation; SDK cache hits perform zero handoffs.
func (o *Observer) Track(ctx context.Context) (context.Context, func() DispatchRecord) {
	o.mu.Lock()
	o.sequence++
	trace := &wireTrace{observer: o, value: DispatchRecord{LogicalID: o.prefix + strconv.FormatUint(o.sequence, 36)}}
	o.mu.Unlock()
	return context.WithValue(ctx, wireTraceKey{}, trace), func() DispatchRecord {
		o.mu.Lock()
		defer o.mu.Unlock()
		return trace.value
	}
}

type rawBinding struct {
	result sdk.Result // Keep the object alive: pointer reuse must not match old data.
	raw    json.RawMessage
	method string
	uri    string
}

// Observer binds real wire response bytes to the exact object returned by the
// pinned MCP SDK. Allocate one observer per identity and connection generation.
// It does not own transports, negotiate protocols, authenticate, or retry calls.
// It must be installed both as sending middleware and at the byte boundary.
type Observer struct {
	mu         sync.Mutex
	limits     WireLimits
	prefix     string
	sequence   uint64
	closed     bool
	pending    map[string]*wirePending
	ids        map[string]string
	tombstones map[string]struct{}
	tombOrder  []string
	retiredID  int64
	hasRetired bool
	bindings   map[sdk.Result]*rawBinding
	bindOrder  []sdk.Result
	rawBytes   int
}

func NewObserver(limits WireLimits) *Observer {
	setDefault := func(value *int, fallback int) {
		if *value <= 0 {
			*value = fallback
		}
	}
	setDefault(&limits.MaxFrameBytes, 8<<20)
	setDefault(&limits.MaxPending, 64)
	setDefault(&limits.MaxBindings, 256)
	setDefault(&limits.MaxRawBytes, 32<<20)
	setDefault(&limits.MaxTombstones, 256)
	return &Observer{
		limits:     limits,
		prefix:     strconv.FormatUint(observerSequence.Add(1), 36) + ":",
		pending:    make(map[string]*wirePending),
		ids:        make(map[string]string),
		tombstones: make(map[string]struct{}),
		bindings:   make(map[sdk.Result]*rawBinding),
	}
}

func observedMethod(method string) bool {
	switch method {
	case "tools/list", "tools/call", "resources/list", "resources/templates/list", "resources/read", "prompts/list", "prompts/get":
		return true
	}
	return false
}

// Middleware clones request params and metadata before adding a host correlation
// token. The token establishes outgoing RPC ID ownership, is not a permission
// credential, and does not require a server echo. Cached SDK list/read methods
// bypass sending middleware and reuse the original result object binding.
func (o *Observer) Middleware() sdk.Middleware {
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if !observedMethod(method) {
				return next(ctx, method, req)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			trace, _ := ctx.Value(wireTraceKey{}).(*wireTrace)
			token, slot, err := o.beginTraced(method, trace)
			if err != nil {
				return nil, err
			}
			defer o.finish(token)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			o.mu.Lock()
			slot.cancel = cancel
			slot.ctx = ctx
			slot.check, _ = ctx.Value(dispatchCheckKey{}).(func(context.Context) error)
			if params, ok := req.GetParams().(*sdk.ReadResourceParams); ok && params != nil {
				slot.uri = params.URI
			}
			o.mu.Unlock()
			req, err = markedRequest(req, token)
			if err != nil {
				return nil, err
			}
			result, callErr := next(ctx, method, req)
			o.mu.Lock()
			defer o.mu.Unlock()
			if callErr != nil {
				// Never turn an RPC/transport error into a completed tool result.
				return result, errors.Join(callErr, slot.err)
			}
			if slot.trace != nil && result != nil && !(reflect.ValueOf(result).Kind() == reflect.Pointer && reflect.ValueOf(result).IsNil()) {
				slot.trace.value.ResultType = "complete"
				if res, ok := result.(interface{ NeedsInput() bool }); ok && res.NeedsInput() {
					slot.trace.value.ResultType = "input_required"
				}
			}
			if o.closed {
				return nil, ErrObserverClosed
			}
			if slot.err != nil {
				return nil, slot.err
			}
			if len(slot.raw) == 0 || result == nil || !reflect.ValueOf(result).Comparable() {
				return nil, ErrRawSnapshotUnavailable
			}
			o.bindLocked(result, slot.raw, slot.method, slot.uri)
			// Move the allocation, rather than reserving the same bytes twice.
			slot.raw = nil
			return result, nil
		}
	}
}

// A malformed/bounded reader is a connection-level failure. The SDK SSE reader
// may classify generic reader errors as disconnection, so cancel pending calls
// explicitly rather than letting them wait for a response that cannot arrive.
func (o *Observer) failPending(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, slot := range o.pending {
		if slot.err == nil {
			slot.err = err
		}
		if slot.cancel != nil {
			slot.cancel()
		}
	}
}

func (o *Observer) failRequest(id string, err error) {
	if id == "" {
		o.failPending(err)
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if slot := o.pending[o.ids[id]]; slot != nil {
		if slot.err == nil {
			slot.err = err
		}
		if slot.cancel != nil {
			slot.cancel()
		}
	}
}

func markedRequest(req sdk.Request, token string) (sdk.Request, error) {
	// SDK request types expose GetParams, but no setter. Clone the public Params
	// field to avoid mutating caller-owned params/maps or losing the SDK session.
	rv := reflect.ValueOf(req)
	if !rv.IsValid() || rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return nil, ErrRawSnapshotUnavailable
	}
	copyReq := reflect.New(rv.Elem().Type())
	copyReq.Elem().Set(rv.Elem())
	field := copyReq.Elem().FieldByName("Params")
	if !field.IsValid() || !field.CanSet() {
		return nil, ErrRawSnapshotUnavailable
	}
	pv := reflect.ValueOf(req.GetParams())
	var params sdk.Params
	if !pv.IsValid() {
		params = &sdk.ParamsBase{}
	} else {
		if pv.Kind() != reflect.Pointer || pv.Elem().Kind() != reflect.Struct {
			// A nil typed pointer needs allocation before inspecting its element.
			if pv.Kind() != reflect.Pointer || !pv.IsNil() || pv.Type().Elem().Kind() != reflect.Struct {
				return nil, ErrRawSnapshotUnavailable
			}
		}
		copyParams := reflect.New(pv.Type().Elem())
		if !pv.IsNil() {
			copyParams.Elem().Set(pv.Elem())
		}
		var ok bool
		params, ok = copyParams.Interface().(sdk.Params)
		if !ok {
			return nil, ErrRawSnapshotUnavailable
		}
	}
	meta := make(map[string]any)
	for key, value := range params.GetMeta() {
		meta[key] = value
	}
	meta[wireTokenKey] = token
	params.SetMeta(meta)
	value := reflect.ValueOf(params)
	if !value.Type().AssignableTo(field.Type()) {
		return nil, ErrRawSnapshotUnavailable
	}
	field.Set(value)
	return copyReq.Interface().(sdk.Request), nil
}

func (o *Observer) begin(method string) (string, *wirePending, error) {
	return o.beginTraced(method, nil)
}
func (o *Observer) beginTraced(method string, trace *wireTrace) (string, *wirePending, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return "", nil, ErrObserverClosed
	}
	if len(o.pending) >= o.limits.MaxPending {
		return "", nil, ErrWireLimit
	}
	var token string
	if trace != nil {
		if trace.observer != o || trace.value.Method != "" {
			return "", nil, ErrRawSnapshotUnavailable
		}
		token = trace.value.LogicalID
		trace.value.Method = method
	} else {
		o.sequence++
		token = o.prefix + strconv.FormatUint(o.sequence, 36)
	}
	slot := &wirePending{method: method, trace: trace}
	o.pending[token] = slot
	return token, slot, nil
}

func (o *Observer) finish(token string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	slot := o.pending[token]
	if slot == nil {
		return
	}
	delete(o.pending, token)
	o.rawBytes -= len(slot.raw)
	if slot.id != "" {
		delete(o.ids, slot.id)
		o.retireLocked(slot.id)
	}
}

func (o *Observer) retireLocked(id string) {
	o.tombstones[id] = struct{}{}
	o.tombOrder = append(o.tombOrder, id)
	if len(o.tombOrder) <= o.limits.MaxTombstones {
		return
	}
	old := o.tombOrder[0]
	o.tombOrder = o.tombOrder[1:]
	delete(o.tombstones, old)
	if strings.HasPrefix(old, "n:") {
		n, _ := strconv.ParseInt(old[2:], 10, 64)
		if !o.hasRetired || n > o.retiredID {
			o.retiredID = n
			o.hasRetired = true
		}
	} else {
		// The SDK issues numeric IDs. If an unsupported string-ID source exhausts
		// tombstones, stop accepting requests rather than risk a late response bind.
		o.closed = true
	}
}

func (o *Observer) bindLocked(result sdk.Result, raw json.RawMessage, method, uri string) {
	if o.bindings[result] != nil {
		o.forgetLocked(result)
	}
	for len(o.bindings) >= o.limits.MaxBindings && len(o.bindOrder) > 0 {
		o.forgetLocked(o.bindOrder[0])
	}
	o.bindings[result] = &rawBinding{result: result, raw: raw, method: method, uri: uri}
	o.bindOrder = append(o.bindOrder, result)
}

func (o *Observer) forgetLocked(result sdk.Result) {
	if binding := o.bindings[result]; binding != nil {
		o.rawBytes -= len(binding.raw)
		delete(o.bindings, result)
	}
	for i, value := range o.bindOrder {
		if value == result {
			o.bindOrder = append(o.bindOrder[:i], o.bindOrder[i+1:]...)
			break
		}
	}
}

// Raw returns a detached copy of the captured result JSON (not the RPC envelope).
// It is non-consuming so a true SDK cache hit can reuse the same source bytes.
// A typed result from another connection or one whose bounded binding was evicted
// fails explicitly; reserializing it is never a substitute for observation.
func (o *Observer) Raw(result sdk.Result) (json.RawMessage, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || result == nil || !reflect.ValueOf(result).Comparable() {
		return nil, ErrRawSnapshotUnavailable
	}
	binding := o.bindings[result]
	if binding == nil {
		return nil, ErrRawSnapshotUnavailable
	}
	return bytes.Clone(binding.raw), nil
}

// Forget releases a non-cached result after its caller has copied the raw data.
func (o *Observer) Forget(result sdk.Result) {
	if result == nil || !reflect.ValueOf(result).Comparable() {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.forgetLocked(result)
}

// Close retires this connection generation and drops every raw/object binding.
// The connection owner remains responsible for closing the actual transport.
func (o *Observer) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	for _, slot := range o.pending {
		if slot.cancel != nil {
			slot.cancel()
		}
	}
	clear(o.pending)
	clear(o.ids)
	clear(o.bindings)
	clear(o.tombstones)
	o.bindOrder = nil
	o.tombOrder = nil
	o.rawBytes = 0
}

type wireEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Meta map[string]json.RawMessage `json:"_meta"`
		URI  json.RawMessage            `json:"uri"`
	} `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func rpcKey(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		return "s:" + value, nil
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid MCP RPC ID: %w", err)
	}
	return "n:" + strconv.FormatInt(n, 10), nil
}

func (o *Observer) observeFrame(frame []byte, outgoing bool) error {
	if len(frame) > o.limits.MaxFrameBytes {
		return ErrWireLimit
	}
	if err := jsontext.ValidateUnicode(frame); err != nil {
		return err
	}
	frame = bytes.TrimSpace(frame)
	if frame[0] == '[' {
		var messages []json.RawMessage
		if err := json.Unmarshal(frame, &messages); err != nil {
			return err
		}
		for _, message := range messages {
			if err := o.observeMessage(message, outgoing); err != nil {
				return err
			}
		}
		return nil
	}
	return o.observeMessage(frame, outgoing)
}

func (o *Observer) observeMessage(frame []byte, outgoing bool) error {
	var message wireEnvelope
	if err := json.Unmarshal(frame, &message); err != nil {
		return err
	}
	id, err := rpcKey(message.ID)
	if err != nil {
		return err
	}
	if outgoing && id != "" {
		return o.observeOutgoing(id, message)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return ErrObserverClosed
	}
	if id == "" {
		if !outgoing {
			var uri string
			if message.Method == "notifications/resources/updated" {
				if err := json.Unmarshal(message.Params.URI, &uri); err != nil {
					return err
				}
			}
			o.invalidateLocked(message.Method, uri)
		}
		return nil // Notifications are never a call's result, even when interleaved.
	}
	if message.Method != "" {
		return nil // Server requests use the opposite RPC ID direction.
	}
	slot := o.pending[o.ids[id]]
	if slot == nil {
		return nil // A cancelled/old generation response cannot bind a new result.
	}
	if slot.trace != nil && (len(message.Result) > 0 || len(message.Error) > 0) {
		slot.trace.value.ResponseReceived = true
		slot.trace.value.RPCError = len(message.Error) > 0
	}
	if errors.Is(slot.err, ErrWireInvalidated) {
		return nil
	}
	if len(message.Result) == 0 {
		return nil // Preserve SDK RPC error decoding and its error chain.
	}
	if len(slot.raw) != 0 {
		slot.err = ErrRawSnapshotUnavailable
		return slot.err
	}
	for o.rawBytes+len(message.Result) > o.limits.MaxRawBytes && len(o.bindOrder) > 0 {
		o.forgetLocked(o.bindOrder[0])
	}
	if o.rawBytes+len(message.Result) > o.limits.MaxRawBytes {
		slot.err = ErrWireLimit
		return slot.err
	}
	slot.raw = message.Result
	o.rawBytes += len(slot.raw)
	return nil
}

func (o *Observer) sendCheckLocked(id string, slot *wirePending) error {
	if o.closed {
		return ErrObserverClosed
	}
	if slot.err != nil {
		return slot.err
	}
	if slot.ctx != nil {
		if err := slot.ctx.Err(); err != nil {
			return err
		}
	}
	_, tombstone := o.tombstones[id]
	retired := false
	if strings.HasPrefix(id, "n:") && o.hasRetired {
		n, _ := strconv.ParseInt(id[2:], 10, 64)
		retired = n <= o.retiredID
	}
	if slot.sent || o.ids[id] != "" || tombstone || retired {
		slot.err = ErrWireReplay
		return ErrWireReplay
	}
	return nil
}

func (o *Observer) observeOutgoing(id string, message wireEnvelope) error {
	if !observedMethod(message.Method) {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.closed {
			return ErrObserverClosed
		}
		return nil
	}
	var token string
	if err := json.Unmarshal(message.Params.Meta[wireTokenKey], &token); err != nil {
		return ErrRawSnapshotUnavailable
	}
	o.mu.Lock()
	slot := o.pending[token]
	if slot == nil || slot.method != message.Method {
		o.mu.Unlock()
		return ErrRawSnapshotUnavailable
	}
	if err := o.sendCheckLocked(id, slot); err != nil {
		o.mu.Unlock()
		return err
	}
	if slot.claiming {
		slot.err = ErrWireReplay
		o.mu.Unlock()
		return ErrWireReplay
	}
	slot.claiming = true
	check, ctx := slot.check, slot.ctx
	o.mu.Unlock()
	var denial error
	if check != nil {
		if err := check(ctx); err != nil {
			denial = &DispatchDeniedError{Err: err}
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	slot.claiming = false
	if o.pending[token] != slot {
		return ErrObserverClosed
	}
	if denial != nil {
		slot.err = denial
		return denial
	}
	if err := o.sendCheckLocked(id, slot); err != nil {
		return err
	}
	slot.sent, slot.id = true, id
	if slot.trace != nil {
		slot.trace.value.RPCID = string(message.ID)
		slot.trace.value.Attempts++
	}
	o.ids[id] = token
	return nil
}

// Run at the incoming byte boundary, before SDK notification dispatch. The SDK
// cache has no in-flight invalidation generation: otherwise an old response can
// arrive after invalidate() and be put back as a new cached page. Reject that
// response in middleware before SDK put, and invalidate matching old bindings.
// URI matching touches only bounded pending/binding maps, never a growing map
// populated by server-controlled notification identifiers.
func (o *Observer) invalidateLocked(notification, uri string) {
	match := func(method, resourceURI string) bool {
		switch notification {
		case "notifications/tools/list_changed":
			return method == "tools/list"
		case "notifications/resources/list_changed":
			return method == "resources/list" || method == "resources/templates/list"
		case "notifications/prompts/list_changed":
			return method == "prompts/list"
		case "notifications/resources/updated":
			return method == "resources/read" && resourceURI == uri
		}
		return false
	}
	for _, slot := range o.pending {
		if slot.sent && match(slot.method, slot.uri) {
			if slot.err == nil {
				slot.err = ErrWireInvalidated
			}
			o.rawBytes -= len(slot.raw)
			slot.raw = nil
		}
	}
	for result, binding := range o.bindings {
		if match(binding.method, binding.uri) {
			// Also release the strongly retained SDK object, not only its raw
			// bytes; old typed schemas/results can otherwise escape byte bounds.
			o.forgetLocked(result)
		}
	}
}
