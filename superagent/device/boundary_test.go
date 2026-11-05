package device

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent/background"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCurrentAuthorityAndPersistentFence(t *testing.T) {
	b := &fakeBackend{}
	d, c, p, cfg := fixture(t, "a", b)
	var current, requestEpoch atomic.Uint64
	current.Store(1)
	requestEpoch.Store(1)
	authorize := func(*http.Request) (Authority, error) {
		e := requestEpoch.Load()
		if e != current.Load() {
			return Authority{}, errors.New("SECRET")
		}
		return Authority{Domain: cfg.Binding.Domain, Device: cfg.Binding.Device, Scope: c.Scope(), Epoch: e}, nil
	}
	d.mu.Lock()
	d.config.Authorize = authorize
	d.mu.Unlock()
	ctx := context.Background()
	if _, err := c.Describe(ctx); err != nil {
		t.Fatal(err)
	}
	// Expiry/takeover is checked before any higher epoch ever reaches the device.
	current.Store(2)
	if err := c.Start(ctx, request(c, "expired")); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired authorization accepted: %v", err)
	}
	requestEpoch.Store(2)
	if _, err := c.Describe(ctx); err != nil {
		t.Fatal(err)
	}
	current.Store(1)
	requestEpoch.Store(1)
	for _, action := range []string{"start", "stop", "inspect", "describe"} {
		cmd := command{Action: action}
		if action != "describe" {
			cmd.ID = "old"
		}
		if action == "start" {
			r := request(c, "old")
			cmd.Request = &r
		}
		if _, err := c.exchange(ctx, cmd); !errors.Is(err, ErrDenied) {
			t.Fatalf("old %s accepted: %v", action, err)
		}
	}
	if b.starts != 0 || b.stops != 0 {
		t.Fatal("fenced backend invoked")
	}
	d.Close()
	cfg.Authorize = authorize
	d2, err := Open(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if d2.epoch != 2 {
		t.Fatal("epoch reset")
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if _, err = d2.handle(r, command{Version: 1, Action: "describe", Scope: c.Scope()}); !errors.Is(err, ErrDenied) {
		t.Fatal("restart lost fence")
	}
}
func TestNoRedirectOrTransportErrorDisclosure(t *testing.T) {
	var reached atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	opts := ClientOptions{URL: srv.URL, Transport: srv.Client().Transport, Binding: Binding{Domain: "d", Device: "a", Workspace: "w", PolicyRevision: "p"}, Tools: []string{"write"}}
	c, err := NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	if c.Start(context.Background(), request(c, "op")) == nil || reached.Load() != 0 {
		t.Fatal("redirect followed")
	}
	opts.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.GetBody != nil || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" {
			t.Error("request permits replay")
		}
		return nil, errors.New("DEVICE_SECRET")
	})
	c, _ = NewClient(opts)
	err = c.Start(context.Background(), request(c, "op"))
	if err == nil || strings.Contains(err.Error(), "DEVICE_SECRET") {
		t.Fatal("transport error leaked")
	}
	for _, url := range []string{"http://example.com", "https://secret:secret@example.com", "https://example.com?token=secret", "file:///tmp/secret"} {
		opts.URL = url
		if _, err = NewClient(opts); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", url)
		}
	}
}
func TestInvalidObservationsCannotClaimExit(t *testing.T) {
	cases := []background.Observation{{ID: "wrong", State: "exited"}, {ID: "op", State: "exited", Receipts: []background.Receipt{{Token: 1, Domain: "agent", Committed: true}}}, {ID: "op", State: "running", Receipts: []background.Receipt{{Token: 1, ParentToken: 2, Domain: "agent"}}}, {ID: "op", State: "other"}}
	for _, o := range cases {
		if _, ok := project(operation{ID: "op", State: "unknown"}, o); ok {
			t.Fatalf("invalid observation accepted: %+v", o)
		}
	}
}
func TestRegistryWriteFailurePreventsDispatchAndPoisons(t *testing.T) {
	b := &fakeBackend{}
	d, c, _, _ := fixture(t, "a", b)
	if err := d.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background(), request(c, "op")); err == nil {
		t.Fatal("closed store accepted")
	}
	if b.starts != 0 || !d.failed {
		t.Fatal("dispatch crossed failed persistence")
	}
	if _, err := c.Describe(context.Background()); err == nil {
		t.Fatal("poisoned registry still acknowledges")
	}
}
func TestAuthorityPanicIsPrivateAndPoisons(t *testing.T) {
	b := &fakeBackend{}
	d, c, _, _ := fixture(t, "a", b)
	d.mu.Lock()
	d.config.Authorize = func(*http.Request) (Authority, error) { panic("DEVICE_SECRET") }
	d.mu.Unlock()
	err := c.Start(context.Background(), request(c, "op"))
	if err == nil || strings.Contains(err.Error(), "DEVICE_SECRET") || !d.failed || b.starts != 0 {
		t.Fatalf("panic boundary: %v", err)
	}
}
func TestWireLimitsAndUnknownFields(t *testing.T) {
	b := &fakeBackend{}
	d, _, _, _ := fixture(t, "a", b)
	for _, body := range []string{strings.Repeat("x", maxWireBytes+1), `{"version":1,"credential":"SECRET"}`, `{} {}`} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		w := httptest.NewRecorder()
		d.ServeHTTP(w, r)
		raw, _ := io.ReadAll(w.Result().Body)
		if w.Code == http.StatusOK || strings.Contains(string(raw), "SECRET") {
			t.Fatal("invalid wire accepted/leaked")
		}
	}
}
func TestPrivatePathAndAllGrantFieldsPinned(t *testing.T) {
	b := &fakeBackend{}
	d, _, p, cfg := fixture(t, "a", b)
	d.Close()
	variants := []Config{cfg, cfg, cfg, cfg, cfg}
	variants[0].Binding.Domain = "other"
	variants[1].Binding.Device = "other"
	variants[2].Binding.Workspace = "other"
	variants[3].Binding.PolicyRevision = "other"
	variants[4].Tools = []string{"other"}
	for _, v := range variants {
		if x, err := Open(p, v); err == nil {
			x.Close()
			t.Fatal("grant mutation accepted")
		}
	}
	link := filepath.Join(filepath.Dir(p), "link.jsonl")
	if err := os.Symlink(p, link); err == nil {
		if x, e := Open(link, cfg); e == nil {
			x.Close()
			t.Fatal("symlink accepted")
		}
	}
}

func TestReceiptEvidenceCannotDisappearOrRegress(t *testing.T) {
	original := background.Receipt{Token: 1, Domain: "agent", CallID: "original", ToolName: "write", Committed: true, Completed: true}
	op, ok := project(operation{ID: "op", State: "running"}, background.Observation{ID: "op", State: "running", Receipts: []background.Receipt{original}})
	if !ok {
		t.Fatal("valid first evidence")
	}
	for _, change := range []string{"missing", "identity", "commit", "completion"} {
		next := original
		receipts := []background.Receipt{next}
		switch change {
		case "missing":
			receipts = nil
		case "identity":
			receipts[0].CallID = "different"
		case "commit":
			receipts[0].Committed = false
		case "completion":
			receipts[0].Completed = false
		}
		if _, ok := project(op, background.Observation{ID: "op", State: "exited", Receipts: receipts}); ok {
			t.Fatalf("accepted regression %s", change)
		}
	}
	merged, ok := project(op, background.Observation{ID: "op", State: "unknown"})
	if !ok || len(merged.Facts) != 1 || !merged.Committed {
		t.Fatal("unknown erased facts")
	}
}
func TestReplayRejectsForgedInitialAndTombstoneExit(t *testing.T) {
	d := &Device{epoch: 1, allowed: map[string]bool{"write": true}, operations: map[string]operation{}}
	forged := operation{ID: "op", Digest: strings.Repeat("a", 64), CallID: "call", Tool: "write", Accepted: true, State: "exited"}
	for _, kind := range []string{"reserve", "accepted", "observe", "stop", "bind"} {
		if d.validRecord(record{Kind: kind, Epoch: 1, Operation: &forged}) {
			t.Fatalf("forged initial %s accepted", kind)
		}
	}
	d.operations["op"] = operation{ID: "op", State: "unknown", Stopped: true}
	forged.Stopped = true
	if d.validRecord(record{Kind: "bind", Epoch: 1, Operation: &forged}) {
		t.Fatal("tombstone gained terminal evidence")
	}
}
func TestRestartThenStopCanBeReplayedAgain(t *testing.T) {
	b := &fakeBackend{}
	d, c, p, cfg := fixture(t, "a", b)
	if err := c.Start(context.Background(), request(c, "op")); err != nil {
		t.Fatal(err)
	}
	d.Close()
	cfg.Backend = &fakeBackend{}
	reopened, err := Open(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if _, err = reopened.handle(r, command{Version: 1, Action: "stop", Scope: c.Scope(), ID: "op"}); err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	again, err := Open(p, cfg)
	if err != nil {
		t.Fatalf("restart stop cannot replay: %v", err)
	}
	defer again.Close()
	if !again.operations["op"].Stopped || again.operations["op"].State != "unknown" {
		t.Fatal("lost conservative stop")
	}
}

type hookBackend struct {
	base          *fakeBackend
	afterStart    func()
	beforeStop    func()
	beforeInspect func()
}

func (b *hookBackend) Start(ctx context.Context, r background.Request) error {
	err := b.base.Start(ctx, r)
	if b.afterStart != nil {
		b.afterStart()
	}
	return err
}
func (b *hookBackend) Stop(ctx context.Context, id string) error {
	if b.beforeStop != nil {
		b.beforeStop()
	}
	return b.base.Stop(ctx, id)
}
func (b *hookBackend) Inspect(ctx context.Context, id string) (background.Observation, error) {
	if b.beforeInspect != nil {
		b.beforeInspect()
	}
	return b.base.Inspect(ctx, id)
}
func TestDispatchAcknowledgmentWriteFailureNeverReplays(t *testing.T) {
	b := &fakeBackend{}
	d, c, p, cfg := fixture(t, "a", b)
	d.config.Backend = &hookBackend{base: b, afterStart: func() { d.store.Close() }}
	r := request(c, "op")
	if err := c.Start(context.Background(), r); err == nil || b.starts != 1 || !d.failed {
		t.Fatalf("uncertain dispatch not quarantined: %v", err)
	}
	d.Close()
	fresh := &fakeBackend{}
	cfg.Backend = fresh
	reopened, err := Open(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.handle(httptest.NewRequest(http.MethodPost, "/", nil), command{Version: 1, Action: "start", Scope: c.Scope(), ID: r.ID, Request: &r}); err == nil || fresh.starts != 0 {
		t.Fatal("uncertain reservation replayed")
	}
}
func TestStopIntentPersistsBeforeBackendFailure(t *testing.T) {
	b := &fakeBackend{}
	d, c, p, cfg := fixture(t, "a", b)
	if err := c.Start(context.Background(), request(c, "op")); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.err = errors.New("SECRET")
	b.mu.Unlock()
	if err := c.Stop(context.Background(), "op"); err == nil {
		t.Fatal("backend failure absent")
	}
	d.Close()
	cfg.Backend = &fakeBackend{}
	reopened, err := Open(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.operations["op"].Stopped {
		t.Fatal("stop intent lost")
	}
}
func TestObservationPersistenceFailureDoesNotReleaseEvidence(t *testing.T) {
	b := &fakeBackend{}
	d, c, _, _ := fixture(t, "a", b)
	if err := c.Start(context.Background(), request(c, "op")); err != nil {
		t.Fatal(err)
	}
	b.set(background.Observation{ID: "op", State: "exited"})
	d.config.Backend = &hookBackend{base: b, beforeInspect: func() { d.store.Close() }}
	if _, err := c.Inspect(context.Background(), "op"); err == nil || !d.failed {
		t.Fatal("unsaved terminal evidence acknowledged")
	}
}

func TestMissingRegistryNeverCreatesRecoveryState(t *testing.T) {
	b := &fakeBackend{}
	d, _, p, cfg := fixture(t, "a", b)
	d.Close()
	missing := filepath.Join(filepath.Dir(p), "missing.jsonl")
	if x, err := Open(missing, cfg); err == nil {
		x.Close()
		t.Fatal("missing state recreated")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("Open created missing state")
	}
	if x, err := Create(p, cfg); err == nil {
		x.Close()
		t.Fatal("Create replaced registry")
	}
}

func TestQueuedOldOwnerIsAuthorizedAfterRegistryLock(t *testing.T) {
	b := &fakeBackend{}
	d, c, _, cfg := fixture(t, "a", b)
	var current, calls atomic.Uint64
	current.Store(1)
	d.config.Authorize = func(r *http.Request) (Authority, error) {
		calls.Add(1)
		if r.Header.Get("X-Epoch") != fmt.Sprint(current.Load()) {
			return Authority{}, ErrDenied
		}
		return Authority{Domain: cfg.Binding.Domain, Device: cfg.Binding.Device, Scope: c.Scope(), Epoch: current.Load()}, nil
	}
	d.mu.Lock()
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set("X-Epoch", "1")
		req := request(c, "old")
		close(started)
		_, err := d.handle(r, command{Version: 1, Action: "start", ID: req.ID, Scope: c.Scope(), Request: &req})
		result <- err
	}()
	<-started
	if calls.Load() != 0 {
		t.Fatal("authorized before registry lock")
	}
	current.Store(2)
	d.mu.Unlock()
	if err := <-result; !errors.Is(err, ErrDenied) || b.starts != 0 {
		t.Fatalf("queued old owner executed: %v", err)
	}
}
func TestTransportPanicCannotLeak(t *testing.T) {
	opts := ClientOptions{URL: "https://example.invalid", Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { panic("DEVICE_SECRET") }), Binding: Binding{Domain: "d", Device: "a", Workspace: "w", PolicyRevision: "p"}, Tools: []string{"write"}}
	c, err := NewClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Start(context.Background(), request(c, "op"))
	if err == nil || strings.Contains(err.Error(), "DEVICE_SECRET") {
		t.Fatal("transport panic leaked")
	}
}
