// Package helper provides opt-in, same-host durable background identities for
// explicitly registered helper binaries. It is not a shell or a sandbox.
package helper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/agent/session"
)

var ErrInvalid = errors.New("helper: invalid configuration, request or identity conflict")
var ErrDenied = errors.New("helper: unauthorized or fenced")
var ErrUnavailable = errors.New("helper: unavailable; inspect the original identity")

// Command is trusted host configuration, never a model-selected command line.
// Executable must be an absolute, immutable helper binary implementing Run.
// Args are fixed by the host; the private operation directory is appended last.
// No environment, stdin, stdout, stderr or extra file descriptors are inherited.
type Command struct {
	Tool, Executable string
	Args             []string
}

type Authorization struct {
	Action, ID string
	Request    *background.Request
}

type Options struct {
	Scope, PolicyRevision string
	MaxConcurrent         int
	Helpers               []Command
	// Authorize must verify current host ownership on every command, including
	// reads. Return a positive, monotonically increasing durable lease epoch.
	// Highest-seen epoch fencing is additional protection, not a lease check.
	// The helper independently authorizes native invocation and pre-effect checks.
	Authorize func(context.Context, Authorization) (uint64, error)
}

type registration struct {
	Command
	SHA256 string
}
type manifest struct {
	Version        int                `json:"version"`
	Grant          string             `json:"grant"`
	PolicyRevision string             `json:"policy_revision"`
	Epoch          uint64             `json:"epoch"`
	Request        background.Request `json:"request"`
}
type snapshot struct {
	Digest      string                 `json:"digest"`
	Observation background.Observation `json:"observation"`
}
type operation struct {
	ID       string                 `json:"id"`
	Manifest *manifest              `json:"manifest,omitempty"`
	Stopped  bool                   `json:"stopped"`
	Reserved bool                   `json:"reserved"`
	Accepted bool                   `json:"accepted"`
	Observed background.Observation `json:"observed"`
}
type record struct {
	Epoch     uint64     `json:"epoch"`
	Operation *operation `json:"operation,omitempty"`
}

// Backend holds the exclusive registry writer, not the helper lifetime. Close
// releases only that writer. Helpers survive coordinator shutdown. Reopening
// never dispatches an old operation, even if its request or claim file is absent.
type Backend struct {
	mu         sync.Mutex
	dir, grant string
	opts       Options
	commands   map[string]registration
	store      *session.JSONLStorage
	seq, epoch uint64
	ops        map[string]operation
	live       map[string]<-chan struct{}
	closed     bool
	failed     bool
}

var _ background.Backend = (*Backend)(nil)

// Create provisions a new private directory. Never use it to recover lost state.
func Create(dir string, opts Options) (*Backend, error) { return open(dir, opts, true) }

// Open requires an existing, complete registry and does not repair it.
func Open(dir string, opts Options) (*Backend, error) { return open(dir, opts, false) }

func open(dir string, opts Options, create bool) (*Backend, error) {
	// Only Linux process/session and filesystem durability behavior is supported
	// in this slice. Other platforms fail before changing state.
	if runtime.GOOS != "linux" || !filepath.IsAbs(dir) || !validID(opts.Scope) || !validID(opts.PolicyRevision) || opts.Authorize == nil || opts.MaxConcurrent < 1 || opts.MaxConcurrent > 256 || len(opts.Helpers) < 1 || len(opts.Helpers) > 64 {
		return nil, ErrInvalid
	}
	commands := map[string]registration{}
	registrations := make([]registration, 0, len(opts.Helpers))
	for _, c := range opts.Helpers {
		if !validID(c.Tool) || !filepath.IsAbs(c.Executable) || commands[c.Tool].Tool != "" || len(c.Args) > 64 {
			return nil, ErrInvalid
		}
		c.Args = slices.Clone(c.Args)
		for _, a := range c.Args {
			if len(a) > 4096 || !utf8.ValidString(a) || strings.ContainsRune(a, 0) {
				return nil, ErrInvalid
			}
		}
		sum, err := executableDigest(c.Executable)
		if err != nil {
			return nil, ErrInvalid
		}
		r := registration{Command: c, SHA256: sum}
		commands[c.Tool] = r
		registrations = append(registrations, r)
	}
	slices.SortFunc(registrations, func(a, b registration) int { return strings.Compare(a.Tool, b.Tool) })
	grant := digest(struct {
		Scope, Policy string
		Limit         int
		Commands      []registration
	}{opts.Scope, opts.PolicyRevision, opts.MaxConcurrent, registrations})
	if create {
		if err := os.Mkdir(dir, 0700); err != nil {
			return nil, ErrUnavailable
		}
		if err := syncDir(filepath.Dir(dir)); err != nil {
			return nil, ErrUnavailable
		}
	}
	if privateDir(dir) != nil {
		return nil, ErrUnavailable
	}
	path := filepath.Join(dir, "registry.jsonl")
	var store *session.JSONLStorage
	var err error
	if create {
		store, err = session.CreateJSONLStorage(path, session.Header{ID: grant})
	} else if privateFile(path) == nil {
		store, err = session.OpenJSONLStorageStrict(path)
	} else {
		return nil, ErrUnavailable
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	b := &Backend{dir: dir, grant: grant, opts: opts, commands: commands, store: store, ops: map[string]operation{}, live: map[string]<-chan struct{}{}}
	if create && syncDir(dir) != nil || store.Header().ID != grant {
		store.Close()
		return nil, ErrUnavailable
	}
	for _, item := range store.Log() {
		if item.Entry == nil || item.Entry.Lane != session.MainLane || item.Entry.Type != session.EntryTypeCustom || item.Entry.Custom == nil || item.Entry.Custom.CustomType != "helper.v1" || item.Entry.ID != strconv.FormatUint(item.Seq, 10) {
			store.Close()
			return nil, ErrUnavailable
		}
		var r record
		if strictJSON(item.Entry.Custom.Payload, &r) != nil || !b.validRecord(r) {
			store.Close()
			return nil, ErrUnavailable
		}
		b.apply(r)
		b.seq = item.Seq
	}
	return b, nil
}

func (b *Backend) validRequest(r background.Request) bool {
	if !validID(r.ID) || r.Scope != b.opts.Scope || !validID(r.Call.ID) || b.commands[r.Call.Name].Tool == "" || len(r.Call.Arguments) > 64<<10 || len(r.Call.ParsedArgs) != 0 || r.Call.Namespace != "" || r.Call.OriginalID != "" || r.Call.ThoughtSignature != "" {
		return false
	}
	var args map[string]json.RawMessage
	return json.Unmarshal(r.Call.Arguments, &args) == nil && args != nil
}

func (b *Backend) validRecord(r record) bool {
	if r.Epoch == 0 || r.Epoch < b.epoch {
		return false
	}
	if r.Operation == nil {
		return r.Epoch > b.epoch
	}
	o := *r.Operation
	if r.Epoch != b.epoch || !validID(o.ID) || o.Observed.ID != o.ID || !validObservation(o.Observed) {
		return false
	}
	if o.Manifest == nil {
		if !o.Stopped || o.Accepted || o.Reserved || !reflect.DeepEqual(o.Observed, background.Observation{ID: o.ID, State: "unknown"}) {
			return false
		}
	} else if o.Manifest.Version != 1 || o.Manifest.Grant != b.grant || o.Manifest.PolicyRevision != b.opts.PolicyRevision || o.Manifest.Epoch == 0 || o.Manifest.Epoch > r.Epoch || o.Manifest.Request.ID != o.ID || !b.validRequest(o.Manifest.Request) {
		return false
	}
	old, exists := b.ops[o.ID]
	if !o.Reserved && (o.Accepted || o.Observed.State != "unknown" || len(o.Observed.Receipts) != 0 || o.Observed.Failed) {
		return false
	}
	if !exists {
		if o.Reserved && (o.Stopped || o.Manifest.Epoch != r.Epoch || b.inUse() >= b.opts.MaxConcurrent) {
			return false
		}
		return !o.Accepted && o.Reserved == (o.Manifest != nil) && reflect.DeepEqual(o.Observed, background.Observation{ID: o.ID, State: "unknown"})
	}
	if old.Reserved != o.Reserved || old.Manifest != nil && !reflect.DeepEqual(old.Manifest, o.Manifest) || old.Stopped && !o.Stopped || old.Accepted && !o.Accepted || !advances(old.Observed, o.Observed) {
		return false
	}
	// Binding an earlier stop never proves that a lost operation did not run.
	if old.Manifest == nil && o.Manifest != nil && (o.Accepted || o.Observed.State != "unknown" || o.Manifest.Epoch != r.Epoch) || !old.Accepted && o.Accepted && old.Stopped {
		return false
	}
	return true
}

func (b *Backend) inUse() int {
	n := 0
	for _, o := range b.ops {
		if o.Reserved && o.Observed.State != "exited" {
			n++
		}
	}
	return n
}
func (b *Backend) apply(r record) {
	b.epoch = r.Epoch
	if r.Operation != nil {
		b.ops[r.Operation.ID] = *r.Operation
	}
}
func (b *Backend) persist(o *operation, epoch uint64) error {
	r := record{Epoch: epoch, Operation: o}
	if !b.validRecord(r) {
		b.failed = true
		return ErrUnavailable
	}
	raw, _ := json.Marshal(r)
	if _, err := b.store.AppendEntry(session.MainLane, session.NewEntry{ID: strconv.FormatUint(b.seq+1, 10), Type: session.EntryTypeCustom, Custom: &session.CustomData{CustomType: "helper.v1", Payload: raw}}); err != nil {
		b.failed = true
		return ErrUnavailable
	}
	b.seq++
	b.apply(r)
	return nil
}
func (b *Backend) authorize(ctx context.Context, a Authorization) (epoch uint64, err error) {
	if ctx == nil || ctx.Err() != nil || b.closed || b.failed {
		return 0, ErrUnavailable
	}
	defer func() {
		if recover() != nil {
			epoch, err = 0, ErrDenied
		}
	}()
	if a.Request != nil {
		copy := clone(*a.Request)
		a.Request = &copy
	}
	epoch, err = b.opts.Authorize(ctx, a)
	if err != nil || ctx.Err() != nil || epoch == 0 || epoch < b.epoch {
		return 0, ErrDenied
	}
	if epoch > b.epoch {
		err = b.persist(nil, epoch)
	}
	return epoch, err
}

func (b *Backend) Start(ctx context.Context, r background.Request) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.validRequest(r) {
		return ErrInvalid
	}
	r = clone(r)
	epoch, err := b.authorize(ctx, Authorization{Action: "start", ID: r.ID, Request: &r})
	if err != nil {
		return err
	}
	if old, ok := b.ops[r.ID]; ok {
		if old.Manifest != nil && !reflect.DeepEqual(old.Manifest.Request, r) {
			return ErrInvalid
		}
		if old.Manifest == nil {
			old.Manifest = &manifest{Version: 1, Grant: b.grant, PolicyRevision: b.opts.PolicyRevision, Epoch: epoch, Request: r}
			if err := b.persist(&old, epoch); err != nil {
				return err
			}
		}
		if !old.Accepted || old.Stopped {
			return ErrUnavailable
		}
		return nil // Never dispatch an existing identity, including after restart.
	}
	if b.inUse() >= b.opts.MaxConcurrent {
		return ErrUnavailable
	}
	o := operation{ID: r.ID, Reserved: true, Manifest: &manifest{Version: 1, Grant: b.grant, PolicyRevision: b.opts.PolicyRevision, Epoch: epoch, Request: r}, Observed: background.Observation{ID: r.ID, State: "unknown"}}
	if err := b.persist(&o, epoch); err != nil {
		return err
	}
	dir := b.operationDir(r.ID)
	if os.Mkdir(dir, 0700) != nil || syncDir(b.dir) != nil || writeNew(filepath.Join(dir, "request.json"), *o.Manifest) != nil {
		return ErrUnavailable
	}
	c := b.commands[r.Call.Name]
	if sum, err := executableDigest(c.Executable); err != nil || sum != c.SHA256 {
		return ErrUnavailable
	}
	// Recheck current authority immediately before dispatch. A new epoch does
	// not retroactively authorize a reservation made under an expired grant.
	current, err := b.authorize(ctx, Authorization{Action: "start", ID: r.ID, Request: &r})
	if err != nil || current != epoch {
		return ErrDenied
	}
	done, err := launch(c.Command, dir)
	if err != nil {
		return ErrUnavailable
	}
	b.live[r.ID] = done
	o.Accepted = true
	if err := b.persist(&o, b.epoch); err != nil {
		return err
	}
	// A bounded native admission handshake prevents ordinary startup latency
	// from transiently quarantining the coordinator's next tool/input. This is
	// not a retry or a lifetime context: timeout/cancel leaves the identity and
	// helper intact and uncertain. Missing files never prove no dispatch.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var s snapshot
		if readJSON(filepath.Join(dir, "observation.json"), &s) == nil && s.Digest == digest(*o.Manifest) && s.Observation.ID == o.ID && validObservation(s.Observation) && (s.Observation.State == "running" || s.Observation.State == "exited") {
			o.Observed = s.Observation
			return b.persist(&o, b.epoch)
		}
		select {
		case <-ctx.Done():
			return ErrUnavailable
		case <-deadline.C:
			return ErrUnavailable
		case <-done:
			return ErrUnavailable
		case <-ticker.C:
		}
	}
}

func (b *Backend) operationDir(id string) string { return filepath.Join(b.dir, digest(id)) }

func (b *Backend) Inspect(ctx context.Context, id string) (background.Observation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !validID(id) {
		return background.Observation{}, ErrInvalid
	}
	if _, err := b.authorize(ctx, Authorization{Action: "inspect", ID: id}); err != nil {
		return background.Observation{}, err
	}
	o, ok := b.ops[id]
	if !ok {
		return background.Observation{ID: id, State: "unknown"}, nil
	}
	next := clone(o.Observed)
	if o.Manifest != nil && next.State != "exited" {
		var s snapshot
		fresh := false
		if readJSON(filepath.Join(b.operationDir(id), "observation.json"), &s) == nil && s.Digest == digest(*o.Manifest) && s.Observation.ID == id && validObservation(s.Observation) && advances(o.Observed, s.Observation) {
			next = s.Observation
			fresh = true
		}
		if next.State != "exited" {
			next.State = "unknown"
			if done := b.live[id]; fresh && s.Observation.State == "running" && done != nil {
				select {
				case <-done:
				default:
					next.State = "running"
				}
			}
		}
	}
	if !reflect.DeepEqual(next, o.Observed) {
		o.Observed = next
		if err := b.persist(&o, b.epoch); err != nil {
			return background.Observation{}, err
		}
	}
	if o.Stopped && o.Reserved && next.State != "exited" {
		// Read and preserve facts first: a failed/corrupt stop notification must
		// not hide terminal evidence. Redelivery still needs current stop authority.
		if _, err := b.authorize(ctx, Authorization{Action: "stop", ID: id}); err != nil {
			return clone(next), err
		}
		if err := b.notifyStop(o); err != nil {
			return clone(next), err
		}
	}
	return clone(next), nil
}

// Stop persists intent before notifying the helper. Neither a successful Stop,
// an OS exit nor a missing process is evidence that native effects did not occur.
func (b *Backend) Stop(ctx context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !validID(id) {
		return ErrInvalid
	}
	if _, err := b.authorize(ctx, Authorization{Action: "stop", ID: id}); err != nil {
		return err
	}
	o, ok := b.ops[id]
	if !ok {
		o = operation{ID: id, Observed: background.Observation{ID: id, State: "unknown"}}
	}
	if !o.Stopped {
		o.Stopped = true
		if err := b.persist(&o, b.epoch); err != nil {
			return err
		}
	}
	if !o.Reserved {
		return nil
	}
	return b.notifyStop(o)
}

func (b *Backend) notifyStop(o operation) error {
	path := filepath.Join(b.operationDir(o.ID), "stop.json")
	if err := writeNew(path, digest(*o.Manifest)); err != nil {
		var s string
		if readJSON(path, &s) != nil || s != digest(*o.Manifest) {
			return ErrUnavailable
		}
	}
	return nil
}

func (b *Backend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return b.store.Close()
}

func validID(s string) bool {
	return s != "" && len(s) <= 4096 && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func digest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func clone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func validObservation(o background.Observation) bool {
	if !validID(o.ID) || o.State != "unknown" && o.State != "running" && o.State != "exited" || len(o.Output) > 64<<10 || len(o.Output) > 0 && (!json.Valid(o.Output) || o.State != "exited") || len(o.Receipts) > 256 {
		return false
	}
	seen := map[uint64]bool{}
	for _, r := range o.Receipts {
		if r.Token == 0 || seen[r.Token] || r.ParentToken != 0 && !seen[r.ParentToken] || r.Domain != "agent" && r.Domain != "codemode" || len(r.CallID) > 4096 || len(r.ToolName) > 4096 || !utf8.ValidString(r.CallID) || !utf8.ValidString(r.ToolName) || o.State == "exited" && !r.Completed {
			return false
		}
		seen[r.Token] = true
	}
	return true
}
func advances(old, next background.Observation) bool {
	if old.ID != next.ID || old.Failed && !next.Failed || old.State == "exited" && !reflect.DeepEqual(old, next) {
		return false
	}
	if len(old.Output) != 0 && !bytes.Equal(old.Output, next.Output) {
		return false
	}
	index := map[uint64]background.Receipt{}
	for _, r := range next.Receipts {
		index[r.Token] = r
	}
	for _, r := range old.Receipts {
		n, ok := index[r.Token]
		if !ok || r.ParentToken != n.ParentToken || r.Domain != n.Domain || r.CallID != n.CallID || r.ToolName != n.ToolName || r.Committed && !n.Committed || r.Completed && !n.Completed {
			return false
		}
	}
	return true
}
