//go:build linux && (amd64 || arm64)

package device_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/agent/background/helper"
	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/superagent/device"
	"github.com/Icatme/pi-go/superagent/sharedsqlite"
)

const durableCanary = "PRIVATE-DEVICE-RESULT-CANARY-92748"

var durableBinding = device.Binding{Domain: "durable-chain", Device: "fixture", Workspace: "native-workspace", PolicyRevision: "v1"}
var durableTools = []string{"work"}

// The authority server is a fixture-only bridge to the actual current M6
// journal. A claimed header/epoch alone never authorizes either process.
func durableAuthorized(ctx context.Context, url string, epoch uint64) error {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	r.Header.Set("X-Fixture-Epoch", strconv.FormatUint(epoch, 10))
	c := &http.Client{Timeout: 2 * time.Second}
	response, err := c.Do(r)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return errors.New("fixture current authority denied")
	}
	return nil
}

func durableExists(path string) bool { _, err := os.Stat(path); return err == nil }
func durableMark(path string) error  { return os.WriteFile(path, []byte("1"), 0600) }

// Only our test binary runs. The only "effect" is a private fixture marker.
// A deliberately cancellation-ignoring body models already-entered work; it is
// not an example of permission-compliant application code.
func TestDurableChainNativeProcess(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--durable-native" {
		return
	}
	authority, dir := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	mark := func(name string) { _ = durableMark(filepath.Join(dir, name)) }
	started, err := os.OpenFile(filepath.Join(dir, "physical-start"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		mark("duplicate-process")
		os.Exit(3)
	}
	started.Close()
	err = helper.Run(context.Background(), dir, helper.NativeConfig{
		Scope: durableBinding.Workspace, PolicyRevision: durableBinding.PolicyRevision, PollInterval: 5 * time.Millisecond,
		Authorize: func(ctx context.Context, _ background.Request, epoch uint64) error {
			// Record the actual callback argument, independently of ctx cancellation.
			log, err := os.OpenFile(filepath.Join(dir, "authorized-epochs"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(log, epoch)
			log.Close()
			if err != nil {
				return err
			}
			return durableAuthorized(ctx, authority, epoch)
		},
		Options: agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(ctx context.Context, tc agent.ToolExecutionContext) (agent.ToolResult, error) {
			f, e := os.OpenFile(filepath.Join(dir, "entered"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				mark("duplicate")
				return agent.ToolResult{}, e
			}
			fmt.Fprintln(f, os.Getpid())
			f.Close()
			mode, _ := tc.Args.(map[string]any)["mode"].(string)
			if mode == "crash" {
				executionreceipt.ReportLocalCommit(ctx)
				mark("committed")
				mark("crashed")
				os.Exit(17)
			}
			if mode == "late" {
				<-ctx.Done()
				mark("canceled")
				executionreceipt.ReportLocalCommit(ctx)
				mark("committed")
				for !durableExists(filepath.Join(dir, "release")) {
					time.Sleep(time.Millisecond)
				}
			} else {
				for !durableExists(filepath.Join(dir, "release")) {
					if ctx.Err() != nil && mode != "fence" {
						mark("canceled")
						return agent.ToolResult{}, ctx.Err()
					}
					time.Sleep(time.Millisecond)
				}
				if e := tc.CheckPermission(ctx); e != nil {
					mark("denied")
					return agent.ToolResult{}, e
				}
				executionreceipt.ReportLocalCommit(ctx)
				mark("committed")
			}
			return agent.ToolResult{Content: []agent.Part{agent.NewTextPart(durableCanary)}}, nil
		}}}},
	})
	mark("finished")
	if err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

type durableEpochKey struct{}
type durableFaultBackend struct {
	background.Backend
	fault string
}

func (b durableFaultBackend) Start(ctx context.Context, r background.Request) error {
	if b.fault == "before-start" {
		os.Exit(19)
	}
	err := b.Backend.Start(ctx, r)
	if b.fault == "after-start" {
		os.Exit(19)
	}
	return err
}
func (b durableFaultBackend) Stop(ctx context.Context, id string) error {
	if b.fault == "before-stop" {
		os.Exit(19)
	}
	err := b.Backend.Stop(ctx, id)
	if b.fault == "after-stop" {
		os.Exit(19)
	}
	return err
}

func TestDurableChainEndpointProcess(t *testing.T) {
	if len(os.Args) < 6 || os.Args[len(os.Args)-5] != "--durable-endpoint" {
		return
	}
	dir, authority, create, fault := os.Args[len(os.Args)-4], os.Args[len(os.Args)-3], os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	opts := helper.Options{Scope: durableBinding.Workspace, PolicyRevision: durableBinding.PolicyRevision, MaxConcurrent: 1,
		Helpers: []helper.Command{{Tool: "work", Executable: os.Args[0], Args: []string{"-test.run=^TestDurableChainNativeProcess$", "--", "--durable-native", authority}}},
		Authorize: func(ctx context.Context, _ helper.Authorization) (uint64, error) {
			epoch, _ := ctx.Value(durableEpochKey{}).(uint64)
			return epoch, durableAuthorized(ctx, authority, epoch)
		},
	}
	openHelper, openDevice := helper.Open, device.Open
	if create == "create" {
		openHelper, openDevice = helper.Create, device.Create
	}
	b, err := openHelper(filepath.Join(dir, "helper"), opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer b.Close()
	d, err := openDevice(filepath.Join(dir, "device.jsonl"), device.Config{
		Binding: durableBinding, Tools: durableTools, Backend: durableFaultBackend{b, fault},
		Authorize: func(r *http.Request) (device.Authority, error) {
			epoch, _ := r.Context().Value(durableEpochKey{}).(uint64)
			if err := durableAuthorized(r.Context(), authority, epoch); err != nil {
				return device.Authority{}, err
			}
			return device.Authority{Domain: durableBinding.Domain, Device: durableBinding.Device, Scope: device.Scope(durableBinding, durableTools), Epoch: epoch}, nil
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer d.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "http://"+listener.Addr().String())
	_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		epoch, _ := strconv.ParseUint(r.Header.Get("X-Fixture-Epoch"), 10, 64)
		r = r.WithContext(context.WithValue(r.Context(), durableEpochKey{}, epoch))
		if r.Header.Get("X-Fixture-Drop") == "before" {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		if r.Header.Get("X-Fixture-Drop") == "after" {
			d.ServeHTTP(httptest.NewRecorder(), r)
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		d.ServeHTTP(w, r)
	}))
	os.Exit(0)
}

type durableProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	url  string
}

func (p *durableProcess) kill(t *testing.T) {
	t.Helper()
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Error("endpoint did not exit")
	}
}

type durableChain struct {
	t         *testing.T
	ctx       context.Context
	dir       string
	authority *httptest.Server
	mu        sync.Mutex
	current   *sharedsqlite.Journal
	wire      bytes.Buffer
	commands  []struct{ Action, ID string }
	p         *durableProcess
}

func newDurableChain(t *testing.T, fault string) *durableChain {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	f := &durableChain{t: t, ctx: ctx, dir: t.TempDir()}
	if err := os.Chmod(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	f.authority = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.current == nil || f.current.Context().Err() != nil || r.Header.Get("X-Fixture-Epoch") != strconv.FormatInt(f.current.Epoch(), 10) {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(204)
	}))
	t.Cleanup(f.authority.Close)
	f.nextJournal()
	f.startEndpoint(true, fault)
	// Revoke, release and wait for native completion before removing fixture
	// files or the authority service, including when an assertion fails. An
	// orphan helper is not our waitable child; its completion marker is not an
	// OS process-exit receipt.
	t.Cleanup(func() {
		f.mu.Lock()
		current := f.current
		f.mu.Unlock()
		if current != nil {
			_ = current.Close()
		}
		for _, dir := range f.operationDirs() {
			_ = durableMark(filepath.Join(dir, "release"))
			deadline := time.Now().Add(5 * time.Second)
			for durableExists(filepath.Join(dir, "entered")) && !durableExists(filepath.Join(dir, "finished")) && !durableExists(filepath.Join(dir, "crashed")) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if durableExists(filepath.Join(dir, "entered")) && !durableExists(filepath.Join(dir, "finished")) && !durableExists(filepath.Join(dir, "crashed")) {
				t.Error("native fixture did not finish after release")
			}
		}
	})
	return f
}

func (f *durableChain) nextJournal() *sharedsqlite.Journal {
	f.t.Helper()
	f.mu.Lock()
	old := f.current
	f.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	j, err := sharedsqlite.Open(f.ctx, filepath.Join(f.dir, "coordinator.db"), sharedsqlite.Options{Domain: durableBinding.Domain, Owner: fmt.Sprintf("owner-%d", time.Now().UnixNano())})
	if err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	f.current = j
	f.mu.Unlock()
	f.t.Cleanup(func() { _ = j.Close() })
	return j
}
func (f *durableChain) startEndpoint(create bool, fault string) {
	f.t.Helper()
	mode := "open"
	if create {
		mode = "create"
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDurableChainEndpointProcess$", "--", "--durable-endpoint", f.dir, f.authority.URL, mode, fault)
	cmd.Env = []string{}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		f.t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	p := &durableProcess{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	f.t.Cleanup(func() { p.kill(f.t) })
	ready := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		if s.Scan() {
			ready <- s.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case p.url = <-ready:
	case <-f.ctx.Done():
		f.t.Fatal(f.ctx.Err())
	}
	if !strings.HasPrefix(p.url, "http://127.0.0.1:") {
		f.t.Fatalf("endpoint startup: %q", p.url)
	}
	f.p = p
}
func (f *durableChain) client(epoch int64, dropAction, drop string) *device.Client {
	f.t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	f.t.Cleanup(transport.CloseIdleConnections)
	c, err := device.NewClient(device.ClientOptions{URL: f.p.url, Binding: durableBinding, Tools: durableTools, Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			return nil, err
		}
		var cmd struct{ Action, ID string }
		if err = json.Unmarshal(raw, &cmd); err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.wire.Write(raw)
		f.commands = append(f.commands, cmd)
		f.mu.Unlock()
		clone := r.Clone(r.Context())
		clone.Body = io.NopCloser(bytes.NewReader(raw))
		clone.Header.Set("X-Fixture-Epoch", strconv.FormatInt(epoch, 10))
		if cmd.Action == dropAction {
			clone.Header.Set("X-Fixture-Drop", drop)
		}
		res, err := transport.RoundTrip(clone)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.wire.Write(body)
		f.mu.Unlock()
		res.Body = io.NopCloser(bytes.NewReader(body))
		return res, nil
	})})
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}
func (f *durableChain) wait(fn func() bool) {
	f.t.Helper()
	for !fn() {
		select {
		case <-f.ctx.Done():
			f.t.Fatal("fixture condition timed out", f.ctx.Err())
		case <-time.After(2 * time.Millisecond):
		}
	}
}
func (f *durableChain) operationDirs() []string {
	dirs, _ := filepath.Glob(filepath.Join(f.dir, "helper", "*", "request.json"))
	for i := range dirs {
		dirs[i] = filepath.Dir(dirs[i])
	}
	return dirs
}
func (f *durableChain) entered() string {
	f.t.Helper()
	var result string
	f.wait(func() bool {
		for _, dir := range f.operationDirs() {
			if durableExists(filepath.Join(dir, "entered")) {
				result = dir
				return true
			}
		}
		return false
	})
	return result
}
func (f *durableChain) physicalStarts() int {
	n := 0
	for _, dir := range f.operationDirs() {
		if durableExists(filepath.Join(dir, "physical-start")) {
			n++
		}
		if durableExists(filepath.Join(dir, "duplicate")) || durableExists(filepath.Join(dir, "duplicate-process")) {
			f.t.Fatal("duplicate physical native entry")
		}
	}
	return n
}
func durableRequest(c *device.Client, id, mode string) background.Request {
	raw, _ := json.Marshal(map[string]string{"mode": mode})
	return background.Request{ID: id, Scope: c.Scope(), Call: agent.ToolCall{ID: "original-call", Name: "work", Arguments: raw}}
}
