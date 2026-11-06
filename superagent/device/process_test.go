package device

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/background"
)

// Every server is a real child process. Effects are fixture-only local writes.
// Fixture epoch headers are NOT a supported production authentication scheme.
type processBackend struct {
	mu         sync.Mutex
	path, mode string
	observed   map[string]background.Observation
}

func (b *processBackend) Start(_ context.Context, r background.Request) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, err := os.OpenFile(b.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, r.ID)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return err
	}
	if b.mode == "crash-after-effect" {
		os.Exit(0)
	}
	b.observed[r.ID] = background.Observation{ID: r.ID, State: "exited", Receipts: []background.Receipt{{Token: 1, Domain: "agent", CallID: r.Call.ID, ToolName: r.Call.Name, Committed: true, Completed: true}}}
	return nil
}
func (b *processBackend) Inspect(_ context.Context, id string) (background.Observation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if o, ok := b.observed[id]; ok {
		return o, nil
	}
	return background.Observation{ID: id, State: "unknown"}, nil
}
func (b *processBackend) Stop(context.Context, string) error { return nil }
func TestDeviceProcessHelper(t *testing.T) {
	dir := os.Getenv("PIGO_DEVICE_FIXTURE")
	if dir == "" {
		return
	}
	name := os.Getenv("PIGO_DEVICE_NAME")
	binding := Binding{Domain: "team", Device: name, Workspace: name + "-workspace", PolicyRevision: "v1"}
	tools := []string{"write"}
	backend := &processBackend{path: filepath.Join(dir, "effects"), mode: os.Getenv("PIGO_DEVICE_MODE"), observed: map[string]background.Observation{}}
	cfg := Config{Binding: binding, Tools: tools, Backend: backend, Authorize: func(r *http.Request) (Authority, error) {
		raw, err := os.ReadFile(filepath.Join(dir, "authority"))
		if err != nil {
			return Authority{}, err
		}
		epoch, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil || r.Header.Get("X-Fixture-Epoch") != strconv.FormatUint(epoch, 10) {
			return Authority{}, ErrDenied
		}
		return Authority{Domain: binding.Domain, Device: binding.Device, Scope: Scope(binding, tools), Epoch: epoch}, nil
	}}
	open := Open
	if os.Getenv("PIGO_DEVICE_CREATE") == "1" {
		open = Create
	}
	d, err := open(filepath.Join(dir, "registry.jsonl"), cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer d.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Fixture-Drop") {
		case "before":
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		case "after":
			d.ServeHTTP(httptest.NewRecorder(), r)
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		d.ServeHTTP(w, r)
	})
	fmt.Fprintln(os.Stdout, "http://"+listener.Addr().String())
	_ = http.Serve(listener, handler)
	os.Exit(0)
}

type processFixture struct {
	cmd            *exec.Cmd
	url, dir, name string
	done           chan error
}

func startProcess(t *testing.T, dir, name, mode string, create bool) *processFixture {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDeviceProcessHelper$")
	cmd.Env = append(os.Environ(), "PIGO_DEVICE_FIXTURE="+dir, "PIGO_DEVICE_NAME="+name, "PIGO_DEVICE_MODE="+mode)
	if create {
		cmd.Env = append(cmd.Env, "PIGO_DEVICE_CREATE=1")
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f := &processFixture{cmd: cmd, dir: dir, name: name, done: make(chan error, 1)}
	go func() { f.done <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		select {
		case <-f.done:
		case <-time.After(10 * time.Second):
			t.Error("process cleanup timed out")
		}
	})
	ready := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(out)
		if s.Scan() {
			ready <- s.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case f.url = <-ready:
		if !strings.HasPrefix(f.url, "http://127.0.0.1:") {
			t.Fatalf("helper not ready: %q", f.url)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("helper startup timeout")
	}
	return f
}
func (f *processFixture) client(t *testing.T, epoch, drop string) *Client {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	wrapped := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.Header.Set("X-Fixture-Epoch", epoch)
		r.Header.Set("X-Fixture-Drop", drop)
		return transport.RoundTrip(r)
	})
	c, err := NewClient(ClientOptions{URL: f.url, Transport: wrapped, Binding: Binding{Domain: "team", Device: f.name, Workspace: f.name + "-workspace", PolicyRevision: "v1"}, Tools: []string{"write"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func processDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "authority"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func effectCount(t *testing.T, dir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "effects"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}
func TestTwoProcessesDisconnectAndTakeover(t *testing.T) {
	a := startProcess(t, processDir(t), "a", "", true)
	b := startProcess(t, processDir(t), "b", "", true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ca := a.client(t, "1", "")
	cb := b.client(t, "1", "")
	if _, err := ca.Describe(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cb.Describe(ctx); err != nil {
		t.Fatal(err)
	}
	// Disconnect before admission. Reconnect queries unknown, never dispatches.
	before := request(ca, "before")
	if err := a.client(t, "1", "before").Start(ctx, before); err == nil {
		t.Fatal("missing disconnect")
	}
	o, err := ca.Inspect(ctx, before.ID)
	if err != nil || o.State != "unknown" || effectCount(t, a.dir) != 0 {
		t.Fatalf("before admission: %+v %v", o, err)
	}
	// Lost acknowledgment after a committed effect: exact-ID query recovers it.
	after := request(ca, "after")
	if err := a.client(t, "1", "after").Start(ctx, after); err == nil {
		t.Fatal("missing response loss")
	}
	o, err = ca.Inspect(ctx, after.ID)
	if err != nil || o.State != "exited" || !o.Receipts[0].Committed {
		t.Fatalf("lost committed fact: %+v %v", o, err)
	}
	if err = ca.Start(ctx, after); err != nil {
		t.Fatal(err)
	}
	if effectCount(t, a.dir) != 1 {
		t.Fatal("duplicate side effect")
	}
	if err = cb.Start(ctx, after); err == nil || effectCount(t, b.dir) != 0 {
		t.Fatal("cross-device dispatch")
	}
	if err = cb.Start(ctx, request(cb, "own")); err != nil || effectCount(t, b.dir) != 1 {
		t.Fatal("second device failed")
	}
	// Current authority rejects old owner before replacement reaches the device.
	if err = os.WriteFile(filepath.Join(a.dir, "authority"), []byte("2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = ca.Stop(ctx, after.ID); err != ErrDenied {
		t.Fatalf("old executor not fenced: %v", err)
	}
	if _, err = a.client(t, "2", "").Inspect(ctx, after.ID); err != nil {
		t.Fatal(err)
	}
}
func TestDeviceCrashAfterEffectRemainsUnknown(t *testing.T) {
	dir := processDir(t)
	f := startProcess(t, dir, "a", "crash-after-effect", true)
	c := f.client(t, "1", "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r := request(c, "crashed")
	if err := c.Start(ctx, r); err == nil {
		t.Fatal("crashed Start succeeded")
	}
	// Wait for the registry lock to release, without consuming the cleanup result.
	select {
	case err := <-f.done:
		f.done <- err
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	next := startProcess(t, dir, "a", "", false)
	c2 := next.client(t, "1", "")
	o, err := c2.Inspect(ctx, r.ID)
	if err != nil || o.State != "unknown" {
		t.Fatalf("crash invented exit: %+v %v", o, err)
	}
	if err = c2.Start(ctx, r); err == nil {
		t.Fatal("unknown Start acknowledged")
	}
	if effectCount(t, dir) != 1 {
		t.Fatal("crash replayed effect")
	}
}
