// Package device is an optional, host-authenticated execution protocol adapter.
// It starts no listener, authenticates no account, and provisions no credentials.
package device

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/agent/session"
)

const protocolVersion = 1
const maxWireBytes = 96 << 10

var ErrUnavailable = errors.New("device: unavailable; inspect the original operation")
var ErrDenied = errors.New("device: unauthorized or fenced")
var ErrInvalid = errors.New("device: invalid request or identity conflict")

// Binding is trusted host configuration, not a model-selected destination.
// Workspace is the device-local backend scope, never a credential or path grant.
type Binding struct {
	Domain         string `json:"domain"`
	Device         string `json:"device"`
	Workspace      string `json:"workspace"`
	PolicyRevision string `json:"policy_revision"`
}

// Authority must come from an authenticated host channel and a current lease
// check, not claims in the JSON body or unverified request headers. Epoch must
// be monotonic for this domain and survive coordinator replacement.
type Authority struct {
	Domain, Device, Scope string
	Epoch                 uint64
}
type Config struct {
	Binding Binding
	Tools   []string
	Backend background.Backend
	// Authorize runs under the registry lock for EVERY command, including reads.
	// It must verify current (not merely highest-seen) ownership and permissions.
	// Its errors and panic values are never returned across the protocol.
	Authorize func(*http.Request) (Authority, error)
}

// Scope pins the full device grant in the M4 coordinator's existing durable
// scope field. Changing a device, workspace, policy or tool set changes Scope.
func Scope(b Binding, tools []string) string {
	names := slices.Clone(tools)
	slices.Sort(names)
	raw, _ := json.Marshal(struct {
		Binding Binding
		Tools   []string
	}{b, names})
	sum := sha256.Sum256(raw)
	return "device-v1-" + hex.EncodeToString(sum[:])
}
func validID(s string) bool {
	return s != "" && len(s) <= 4096 && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func validConfig(b Binding, tools []string) bool {
	if !validID(b.Domain) || !validID(b.Device) || !validID(b.Workspace) || !validID(b.PolicyRevision) || len(tools) < 1 || len(tools) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, s := range tools {
		if !validID(s) || seen[s] {
			return false
		}
		seen[s] = true
	}
	raw, err := json.Marshal(struct {
		Binding Binding
		Tools   []string
	}{b, tools})
	return err == nil && len(raw) <= 64<<10
}
func nilBackend(b background.Backend) bool {
	if b == nil {
		return true
	}
	v := reflect.ValueOf(b)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

type command struct {
	Version int                 `json:"version"`
	Action  string              `json:"action"`
	Scope   string              `json:"scope"`
	ID      string              `json:"id,omitempty"`
	Request *background.Request `json:"request,omitempty"`
}
type response struct {
	Version     int                     `json:"version"`
	Scope       string                  `json:"scope"`
	ID          string                  `json:"id,omitempty"`
	Accepted    bool                    `json:"accepted"`
	Observation *background.Observation `json:"observation,omitempty"`
	Description *Description            `json:"description,omitempty"`
}

// Description is an authenticated point-in-time liveness/capability response.
// It is not a persistent online guarantee or discovery/registration service.
type Description struct {
	Binding Binding  `json:"binding"`
	Tools   []string `json:"tools"`
}
type receiptFact struct {
	Token       uint64 `json:"token"`
	ParentToken uint64 `json:"parent_token"`
	Fingerprint string `json:"fingerprint"`
	Committed   bool   `json:"committed"`
	Completed   bool   `json:"completed"`
}

type operation struct {
	ID        string        `json:"id"`
	Digest    string        `json:"digest,omitempty"`
	CallID    string        `json:"call_id,omitempty"`
	Tool      string        `json:"tool,omitempty"`
	Accepted  bool          `json:"accepted"`
	Stopped   bool          `json:"stopped"`
	State     string        `json:"state"`
	Failed    bool          `json:"failed"`
	Committed bool          `json:"committed"`
	Facts     []receiptFact `json:"facts,omitempty"`
}
type record struct {
	Kind      string     `json:"kind"`
	Epoch     uint64     `json:"epoch"`
	Operation *operation `json:"operation,omitempty"`
}

// Device owns a private, exclusive registry writer. Backend Start/Inspect/Stop
// must be bounded admission/query callbacks (not the execution body); they run
// serialized with authority validation. Backend bodies keep their own lifetime.
// Close only closes this protocol endpoint, never claims those bodies exited.
type Device struct {
	mu             sync.Mutex
	config         Config
	scope          string
	allowed        map[string]bool
	store          *session.JSONLStorage
	sequence       uint64
	epoch          uint64
	operations     map[string]operation
	closed, failed bool
}

// Create creates a NEW registry, never overwriting one. Use only for explicit
// first-time provisioning, not as recovery when an existing registry is missing.
func Create(path string, cfg Config) (*Device, error) { return openRegistry(path, cfg, true) }

// Open reopens an EXISTING locked, non-repairing registry in a host-owned
// private directory. Missing state fails closed; never replace it with Create
// to recover execution. It never resumes Start. Device-local credentials belong
// only in host backend closures; neither arguments nor raw results are stored.
func Open(path string, cfg Config) (*Device, error) { return openRegistry(path, cfg, false) }
func openRegistry(path string, cfg Config, create bool) (*Device, error) {
	if !validConfig(cfg.Binding, cfg.Tools) || nilBackend(cfg.Backend) || cfg.Authorize == nil {
		return nil, ErrInvalid
	}
	if err := privatePath(path); err != nil {
		return nil, err
	}
	cfg.Tools = slices.Clone(cfg.Tools)
	slices.Sort(cfg.Tools)
	scope := Scope(cfg.Binding, cfg.Tools)
	var store *session.JSONLStorage
	var err error
	if create {
		store, err = session.CreateJSONLStorage(path, session.Header{ID: scope})
	} else {
		store, err = session.OpenJSONLStorageStrict(path)
	}
	if err != nil {
		return nil, errors.New("device: cannot open private registry")
	}
	d := &Device{config: cfg, scope: scope, allowed: map[string]bool{}, store: store, operations: map[string]operation{}}
	for _, name := range cfg.Tools {
		d.allowed[name] = true
	}
	if store.Header().ID != scope {
		store.Close()
		return nil, errors.New("device: registry grant mismatch")
	}
	for _, item := range store.Log() {
		if item.Entry == nil || item.Entry.Type != session.EntryTypeCustom || item.Entry.Lane != session.MainLane || item.Entry.Custom == nil || item.Entry.Custom.CustomType != "device.v1" || item.Entry.ID != strconv.FormatUint(item.Seq, 10) {
			store.Close()
			return nil, errors.New("device: invalid registry")
		}
		var r record
		if strictJSON(item.Entry.Custom.Payload, &r) != nil || !d.validRecord(r) {
			store.Close()
			return nil, errors.New("device: invalid registry transition")
		}
		d.apply(r)
		d.sequence = item.Seq
	}
	// A cached running state is not evidence after a device process restart.
	for _, o := range d.operations {
		if o.State == "running" {
			o.State = "unknown"
			if err := d.persist("observe", &o); err != nil {
				store.Close()
				return nil, err
			}
		}
	}
	return d, nil
}
func privatePath(path string) error {
	if path == "" {
		return ErrInvalid
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || !dir.IsDir() {
		return ErrInvalid
	}
	if runtime.GOOS != "windows" && dir.Mode().Perm()&0077 != 0 {
		return errors.New("device: registry directory must be private")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return ErrInvalid
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return errors.New("device: registry must be private")
	}
	return nil
}
func strictJSON(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func validHash(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 32 && s == strings.ToLower(s)
}
func validFacts(facts []receiptFact) bool {
	if len(facts) > 256 {
		return false
	}
	seen := map[uint64]bool{}
	for _, f := range facts {
		if f.Token == 0 || seen[f.Token] || f.ParentToken != 0 && !seen[f.ParentToken] || !validHash(f.Fingerprint) {
			return false
		}
		seen[f.Token] = true
	}
	return true
}
func factsAdvance(old, next []receiptFact) bool {
	index := map[uint64]receiptFact{}
	for _, f := range next {
		index[f.Token] = f
	}
	for _, f := range old {
		n, ok := index[f.Token]
		if !ok || n.ParentToken != f.ParentToken || n.Fingerprint != f.Fingerprint || f.Committed && !n.Committed || f.Completed && !n.Completed {
			return false
		}
	}
	return true
}
func (d *Device) validRecord(r record) bool {
	if r.Epoch == 0 || r.Epoch < d.epoch {
		return false
	}
	if r.Operation == nil {
		return r.Kind == "epoch" && r.Epoch > d.epoch
	}
	if r.Epoch != d.epoch {
		return false
	}
	o := *r.Operation
	if !validID(o.ID) || (o.State != "unknown" && o.State != "running" && o.State != "exited") || !validFacts(o.Facts) {
		return false
	}
	committed := false
	for _, f := range o.Facts {
		committed = committed || f.Committed
		if o.State == "exited" && !f.Completed {
			return false
		}
	}
	if committed != o.Committed {
		return false
	}
	if o.Digest == "" {
		if !o.Stopped || o.CallID != "" || o.Tool != "" || o.Accepted || o.State != "unknown" || o.Failed || len(o.Facts) != 0 {
			return false
		}
	} else if !validHash(o.Digest) || !d.allowed[o.Tool] || !validID(o.CallID) {
		return false
	}
	old, exists := d.operations[o.ID]
	switch r.Kind {
	case "reserve":
		return !exists && o.Digest != "" && !o.Accepted && !o.Stopped && o.State == "unknown" && !o.Failed && len(o.Facts) == 0
	case "stop":
		if !exists {
			return o.Digest == ""
		}
		old.Stopped = true
		return reflect.DeepEqual(old, o)
	case "bind":
		if !exists || old.Digest != "" || !old.Stopped || o.Digest == "" {
			return false
		}
		old.Digest = o.Digest
		old.CallID = o.CallID
		old.Tool = o.Tool
		return reflect.DeepEqual(old, o)
	case "accepted":
		if !exists || old.Digest == "" || old.Accepted || old.Stopped || old.State != "unknown" {
			return false
		}
		old.Accepted = true
		old.State = "running"
		return reflect.DeepEqual(old, o)
	case "observe":
		if !exists || old.Digest == "" || old.State == "exited" || old.Digest != o.Digest || old.CallID != o.CallID || old.Tool != o.Tool || old.Stopped != o.Stopped || old.Accepted && !o.Accepted || old.Failed && !o.Failed || !factsAdvance(old.Facts, o.Facts) {
			return false
		}
		return o.Accepted == (old.Accepted || o.State != "unknown")
	}
	return false
}
func (d *Device) apply(r record) {
	d.epoch = r.Epoch
	if r.Operation != nil {
		d.operations[r.Operation.ID] = *r.Operation
	}
}
func (d *Device) persist(kind string, o *operation) error {
	r := record{Kind: kind, Epoch: d.epoch, Operation: o}
	if !d.validRecord(r) {
		d.failed = true
		return ErrUnavailable
	}
	raw, _ := json.Marshal(r)
	if _, err := d.store.AppendEntry(session.MainLane, session.NewEntry{ID: strconv.FormatUint(d.sequence+1, 10), Type: session.EntryTypeCustom, Custom: &session.CustomData{CustomType: "device.v1", Payload: raw}}); err != nil {
		d.failed = true
		return ErrUnavailable
	}
	d.sequence++
	d.apply(r)
	return nil
}
func (d *Device) advance(epoch uint64) error {
	if epoch < d.epoch {
		return ErrDenied
	}
	if epoch == d.epoch {
		return nil
	}
	r := record{Kind: "epoch", Epoch: epoch}
	raw, _ := json.Marshal(r)
	if _, err := d.store.AppendEntry(session.MainLane, session.NewEntry{ID: strconv.FormatUint(d.sequence+1, 10), Type: session.EntryTypeCustom, Custom: &session.CustomData{CustomType: "device.v1", Payload: raw}}); err != nil {
		d.failed = true
		return ErrUnavailable
	}
	d.sequence++
	d.apply(r)
	return nil
}
func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return d.store.Close()
}

// ServeHTTP only handles this protocol's POST endpoint. Authentication, TLS,
// listener limits, current lease authority and OS isolation are host-owned.
func (d *Device) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "device: invalid method", http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxWireBytes+1))
	if err != nil || len(raw) > maxWireBytes {
		http.Error(w, "device: invalid request", http.StatusBadRequest)
		return
	}
	var cmd command
	if strictJSON(raw, &cmd) != nil {
		http.Error(w, "device: invalid request", http.StatusBadRequest)
		return
	}
	out, err := d.handle(r, cmd)
	if err != nil {
		code := http.StatusServiceUnavailable
		if errors.Is(err, ErrDenied) {
			code = http.StatusForbidden
		} else if errors.Is(err, ErrInvalid) {
			code = http.StatusBadRequest
		}
		http.Error(w, err.Error(), code)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
func (d *Device) handle(r *http.Request, cmd command) (out response, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	defer func() {
		if recover() != nil {
			d.failed = true
			out = response{}
			err = ErrUnavailable
		}
	}()
	if d.closed || d.failed {
		return out, ErrUnavailable
	}
	if cmd.Version != protocolVersion || cmd.Scope != d.scope {
		return out, ErrInvalid
	}
	if cmd.Action != "describe" && cmd.Action != "start" && cmd.Action != "inspect" && cmd.Action != "stop" {
		return out, ErrInvalid
	}
	if cmd.Action == "describe" {
		if cmd.ID != "" || cmd.Request != nil {
			return out, ErrInvalid
		}
	} else if !validID(cmd.ID) {
		return out, ErrInvalid
	}
	if (cmd.Action == "start") != (cmd.Request != nil) {
		return out, ErrInvalid
	}
	auth, e := d.config.Authorize(r)
	if e != nil || auth.Domain != d.config.Binding.Domain || auth.Device != d.config.Binding.Device || auth.Scope != d.scope || auth.Epoch == 0 {
		return out, ErrDenied
	}
	if err = d.advance(auth.Epoch); err != nil {
		return out, err
	}
	out = response{Version: protocolVersion, Scope: d.scope, ID: cmd.ID}
	switch cmd.Action {
	case "describe":
		out.Description = &Description{Binding: d.config.Binding, Tools: slices.Clone(d.config.Tools)}
		return out, nil
	case "start":
		req := *cmd.Request
		if req.ID != cmd.ID || req.Scope != d.scope || !validID(req.Call.ID) || !d.allowed[req.Call.Name] || len(req.Call.Arguments) > 64<<10 {
			return out, ErrInvalid
		}
		var args map[string]json.RawMessage
		if json.Unmarshal(req.Call.Arguments, &args) != nil || args == nil {
			return out, ErrInvalid
		}
		identity, e := json.Marshal(req)
		if e != nil {
			return out, ErrInvalid
		}
		sum := sha256.Sum256(identity)
		digest := hex.EncodeToString(sum[:])
		if old, exists := d.operations[req.ID]; exists {
			if old.Digest != "" && old.Digest != digest {
				return out, ErrInvalid
			}
			if old.Digest == "" {
				old.Digest = digest
				old.CallID = req.Call.ID
				old.Tool = req.Call.Name
				if e = d.persist("bind", &old); e != nil {
					return out, e
				}
			}
			if old.Stopped || !old.Accepted {
				return out, ErrUnavailable
			}
			out.Accepted = true
			return out, nil
		}
		op := operation{ID: req.ID, Digest: digest, CallID: req.Call.ID, Tool: req.Call.Name, State: "unknown"}
		if e = d.persist("reserve", &op); e != nil {
			return out, e
		}
		// The backend receives the device-local scope, never credential material.
		req.Scope = d.config.Binding.Workspace
		if e = d.config.Backend.Start(r.Context(), req); e != nil {
			return out, ErrUnavailable
		}
		op.Accepted = true
		op.State = "running"
		if e = d.persist("accepted", &op); e != nil {
			return out, e
		}
		out.Accepted = true
		return out, nil
	case "stop":
		op, ok := d.operations[cmd.ID]
		if !ok {
			op = operation{ID: cmd.ID, State: "unknown"}
		}
		if !op.Stopped {
			op.Stopped = true
			if e = d.persist("stop", &op); e != nil {
				return out, e
			}
		}
		if e = d.config.Backend.Stop(r.Context(), cmd.ID); e != nil {
			return out, ErrUnavailable
		}
		return out, nil
	case "inspect":
		op, ok := d.operations[cmd.ID]
		if !ok {
			out.Observation = &background.Observation{ID: cmd.ID, State: "unknown"}
			return out, nil
		}
		if op.State != "exited" && op.Digest != "" {
			observed, e := d.config.Backend.Inspect(r.Context(), cmd.ID)
			if e != nil {
				return out, ErrUnavailable
			}
			next, ok := project(op, observed)
			if !ok {
				return out, ErrUnavailable
			}
			if !reflect.DeepEqual(next, op) {
				if e = d.persist("observe", &next); e != nil {
					return out, e
				}
				op = next
			}
		}
		observation := op.observation()
		out.Observation = &observation
		out.Accepted = op.Accepted
		return out, nil
	}
	return out, ErrInvalid
}

// project deliberately exports no raw output, errors or child receipt metadata.
// The synthetic root says only that some effect committed and all bodies exited.
func project(op operation, o background.Observation) (operation, bool) {
	if o.ID != op.ID || (o.State != "unknown" && o.State != "running" && o.State != "exited") || len(o.Receipts) > 256 {
		return op, false
	}
	facts := make([]receiptFact, 0, len(o.Receipts))
	seen := map[uint64]bool{}
	for _, r := range o.Receipts {
		if r.Token == 0 || seen[r.Token] || r.ParentToken != 0 && !seen[r.ParentToken] || (r.Domain != "agent" && r.Domain != "codemode") || (o.State == "exited" && !r.Completed) || !utf8.ValidString(r.CallID) || !utf8.ValidString(r.ToolName) || len(r.CallID) > 4096 || len(r.ToolName) > 4096 {
			return op, false
		}
		seen[r.Token] = true
		raw, _ := json.Marshal([]string{r.Domain, r.CallID, r.ToolName})
		sum := sha256.Sum256(raw)
		facts = append(facts, receiptFact{Token: r.Token, ParentToken: r.ParentToken, Fingerprint: hex.EncodeToString(sum[:]), Committed: r.Committed, Completed: r.Completed})
	}
	if o.State == "unknown" {
		merged := slices.Clone(op.Facts)
		indexes := map[uint64]int{}
		for i, f := range merged {
			indexes[f.Token] = i
		}
		for _, f := range facts {
			if i, ok := indexes[f.Token]; ok {
				old := merged[i]
				if old.ParentToken != f.ParentToken || old.Fingerprint != f.Fingerprint {
					return op, false
				}
				merged[i].Committed = old.Committed || f.Committed
				merged[i].Completed = old.Completed || f.Completed
			} else {
				merged = append(merged, f)
			}
		}
		facts = merged
	} else if !factsAdvance(op.Facts, facts) || op.Failed && !o.Failed {
		return op, false
	}
	if !validFacts(facts) {
		return op, false
	}
	op.Facts = facts
	op.Committed = false
	for _, f := range facts {
		op.Committed = op.Committed || f.Committed
	}
	op.State = o.State
	op.Failed = op.Failed || o.Failed
	if o.State != "unknown" {
		op.Accepted = true
	}
	return op, true
}
func (o operation) observation() background.Observation {
	result := background.Observation{ID: o.ID, State: o.State, Failed: o.Failed}
	if o.Digest != "" {
		result.Receipts = []background.Receipt{{Token: 1, Domain: "agent", CallID: o.CallID, ToolName: o.Tool, Committed: o.Committed, Completed: o.State == "exited"}}
	}
	return result
}
