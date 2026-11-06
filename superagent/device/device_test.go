package device

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
)

type fakeBackend struct {
	mu            sync.Mutex
	starts, stops int
	observation   background.Observation
	err           error
}

func (b *fakeBackend) Start(_ context.Context, r background.Request) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.starts++
	b.observation.ID = r.ID
	if b.observation.State == "" {
		b.observation.State = "running"
	}
	return b.err
}
func (b *fakeBackend) Inspect(_ context.Context, id string) (background.Observation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o := b.observation
	if o.ID == "" {
		o = background.Observation{ID: id, State: "unknown"}
	}
	return o, b.err
}
func (b *fakeBackend) Stop(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stops++
	return b.err
}
func (b *fakeBackend) set(o background.Observation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observation = o
}
func fixture(t *testing.T, name string, b *fakeBackend) (*Device, *Client, string, Config) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "device.jsonl")
	cfg := Config{Binding: Binding{Domain: "team", Device: name, Workspace: name + "-workspace", PolicyRevision: "v1"}, Tools: []string{"write"}, Backend: b}
	cfg.Authorize = func(*http.Request) (Authority, error) {
		return Authority{Domain: cfg.Binding.Domain, Device: cfg.Binding.Device, Scope: Scope(cfg.Binding, cfg.Tools), Epoch: 1}, nil
	}
	d, err := Create(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { d.Close() })
	c, err := NewClient(ClientOptions{URL: srv.URL, Transport: srv.Client().Transport, Binding: cfg.Binding, Tools: cfg.Tools})
	if err != nil {
		t.Fatal(err)
	}
	return d, c, p, cfg
}
func request(c *Client, id string) background.Request {
	return background.Request{ID: id, Scope: c.Scope(), Call: agent.ToolCall{ID: "call", Name: "write", Arguments: json.RawMessage(`{"path":"approved"}`)}}
}
func TestTwoDevicesAndImmutableIdentity(t *testing.T) {
	a, b := &fakeBackend{}, &fakeBackend{}
	_, ca, _, _ := fixture(t, "a", a)
	_, cb, _, _ := fixture(t, "b", b)
	ctx := context.Background()
	r := request(ca, "operation")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := ca.Start(ctx, r); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if a.starts != 1 {
		t.Fatalf("starts %d", a.starts)
	}
	if err := cb.Start(ctx, r); err == nil {
		t.Fatal("cross-device scope accepted")
	}
	if b.starts != 0 {
		t.Fatal("cross-device dispatched")
	}
	conflict := r
	conflict.Call.Arguments = json.RawMessage(`{"path":"different"}`)
	if err := ca.Start(ctx, conflict); err == nil {
		t.Fatal("identity reuse accepted")
	}
	wrong := r
	wrong.ID = "other"
	wrong.Call.Name = "shell"
	if err := ca.Start(ctx, wrong); err == nil {
		t.Fatal("unregistered tool accepted")
	}
}
func TestDurableRestartNeverReplaysAndRetainsFacts(t *testing.T) {
	b := &fakeBackend{}
	d, c, p, cfg := fixture(t, "a", b)
	ctx := context.Background()
	r := request(c, "operation")
	if err := c.Start(ctx, r); err != nil {
		t.Fatal(err)
	}
	b.set(background.Observation{ID: r.ID, State: "running", Output: json.RawMessage(`"DEVICE_SECRET"`), Receipts: []background.Receipt{{Token: 1, Domain: "agent", CallID: "DEVICE_SECRET", ToolName: "DEVICE_SECRET", Committed: true, Completed: true}}})
	o, err := c.Inspect(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.State != "running" || len(o.Output) != 0 || len(o.Receipts) != 1 || !o.Receipts[0].Committed || o.Receipts[0].Completed {
		t.Fatalf("wrong aggregate: %+v", o)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := &fakeBackend{}
	cfg.Backend = fresh
	reopened, err := Open(p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	srv := httptest.NewServer(reopened)
	defer srv.Close()
	c2, err := NewClient(ClientOptions{URL: srv.URL, Transport: srv.Client().Transport, Binding: cfg.Binding, Tools: cfg.Tools})
	if err != nil {
		t.Fatal(err)
	}
	if err = c2.Start(ctx, r); err != nil {
		t.Fatal(err)
	}
	if fresh.starts != 0 {
		t.Fatal("replayed start")
	}
	o, err = c2.Inspect(ctx, r.ID)
	if err != nil || o.State != "unknown" || !o.Receipts[0].Committed {
		t.Fatalf("lost unknown facts: %+v %v", o, err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "DEVICE_SECRET") || strings.Contains(string(raw), "approved") {
		t.Fatal("raw output/arguments leaked to registry")
	}
}
func TestStopBeforeStartAndLateCommit(t *testing.T) {
	b := &fakeBackend{}
	_, c, _, _ := fixture(t, "a", b)
	ctx := context.Background()
	if err := c.Stop(ctx, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, request(c, "stopped")); err == nil {
		t.Fatal("stopped start acknowledged")
	}
	if b.starts != 0 {
		t.Fatal("stopped identity executed")
	}
	r := request(c, "late")
	if err := c.Start(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	o, err := c.Inspect(ctx, r.ID)
	if err != nil || o.State != "running" {
		t.Fatalf("stop pretended exit: %+v %v", o, err)
	}
	b.set(background.Observation{ID: r.ID, State: "exited", Receipts: []background.Receipt{{Token: 1, Domain: "agent", Committed: true, Completed: true}}})
	o, err = c.Inspect(ctx, r.ID)
	if err != nil || o.State != "exited" || !o.Receipts[0].Committed || !o.Receipts[0].Completed {
		t.Fatalf("lost late commit: %+v %v", o, err)
	}
}
func TestBindingAndMalformedJournal(t *testing.T) {
	b := &fakeBackend{}
	d, _, p, cfg := fixture(t, "a", b)
	if _, err := Open(p, cfg); err == nil {
		t.Fatal("multiple device writers")
	}
	d.Close()
	wrong := cfg
	wrong.Binding.Workspace = "other"
	if x, err := Open(p, wrong); err == nil {
		x.Close()
		t.Fatal("changed binding accepted")
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"torn":`)
	f.Close()
	before, _ := os.ReadFile(p)
	if x, err := Open(p, cfg); err == nil {
		x.Close()
		t.Fatal("torn registry accepted")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("registry repaired")
	}
}
func TestErrorAndOutputSanitization(t *testing.T) {
	b := &fakeBackend{err: errors.New("DEVICE_SECRET")}
	_, c, _, _ := fixture(t, "a", b)
	err := c.Start(context.Background(), request(c, "operation"))
	if err == nil || strings.Contains(err.Error(), "DEVICE_SECRET") {
		t.Fatalf("leaked backend error: %v", err)
	}
}
