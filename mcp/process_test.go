package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Executed as an isolated helper binary, never as a normal test fixture. This
// exercises actual stdio framing and process-tree ownership without a shell.
func TestManagedProcessFixture(t *testing.T) {
	switch os.Getenv("PI_GO_MCP_HELPER") {
	case "server":
		server := wireTestServer()
		addWireTool(server, "echo", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "fixture"}}}, nil
		})
		session, err := server.Connect(context.Background(), &sdk.StdioTransport{}, nil)
		if err != nil {
			os.Exit(2)
		}
		_ = session.Wait()
		os.Exit(0)
	case "tree_parent", "tree_parent_exit":
		cmd := exec.Command(os.Args[0], "-test.run=^TestManagedProcessFixture$")
		cmd.Env = append(os.Environ(), "PI_GO_MCP_HELPER=tree_child")
		if os.Getenv("PI_GO_MCP_HELPER") == "tree_parent_exit" {
			cmd.Stderr = os.Stderr
		}
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, cmd.Process.Pid)
		if os.Getenv("PI_GO_MCP_HELPER") == "tree_parent_exit" {
			var stop [1]byte
			_, _ = os.Stdin.Read(stop[:])
			os.Exit(0)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "tree_child":
		for {
			time.Sleep(time.Hour)
		}
	}
}

func processFixtureConfig(mode string) ServerConfig {
	return ServerConfig{Name: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestManagedProcessFixture$"}, Env: []string{"PI_GO_MCP_HELPER=" + mode}, Trusted: true, Timeout: 5 * time.Second}
}

func TestManagerManagedStdio(t *testing.T) {
	m, err := New(Config{Scope: Scope{Identity: "alice", AuthEpoch: 1}, Servers: []ServerConfig{processFixtureConfig("server")}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c, err := m.Connect(ctx, "stdio")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Protocol != "2026-07-28" || len(snap.Tools) != 1 {
		t.Fatalf("stdio directory unavailable: %#v", snap)
	}
	result, err := c.CallTool(ctx, &sdk.CallToolParams{Name: "echo", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].(*sdk.TextContent).Text != "fixture" {
		t.Fatalf("stdio result incorrect: %#v", result)
	}
	pid := c.process.cmd.Process.Pid
	alive := captureFixtureProcess(t, pid)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if alive() {
		t.Fatal("closed stdio parent still alive")
	}
}

// Windows owns descendants with a Job Object. Unix owns its assigned process
// group: these trusted fixtures keep all descendants in that group.
func TestManagedProcessOwnsSupportedDescendants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := startManagedProcess(ctx, processFixtureConfig("tree_parent"), 128)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	line := make(chan string, 1)
	go func() { value, _ := bufio.NewReader(p.stdout).ReadString('\n'); line <- value }()
	var child int
	select {
	case value := <-line:
		child, err = strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			t.Fatalf("child pid unavailable: %q %v", value, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("managed child did not start")
	}
	childAlive := captureFixtureProcess(t, child)
	if !childAlive() {
		t.Fatal("fixture child exited before tree test")
	}
	cancel()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("context cancellation did not stop managed process")
	}
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	alive := childAlive()
	for alive && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		alive = childAlive()
	}
	if alive {
		t.Fatalf("managed descendant escaped cleanup: %d", child)
	}
}

func TestManagedProcessOwnerExitDoesNotWaitForInheritedStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p, err := startManagedProcess(ctx, processFixtureConfig("tree_parent_exit"), 128)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	line := make(chan string, 1)
	go func() { value, _ := bufio.NewReader(p.stdout).ReadString('\n'); line <- value }()
	var child int
	select {
	case value := <-line:
		child, err = strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			t.Fatalf("child PID: %q %v", value, err)
		}
	case <-ctx.Done():
		t.Fatal("managed child did not start")
	}
	childAlive := captureFixtureProcess(t, child)
	if _, err := p.stdin.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		t.Fatal("inherited stderr concealed the server's exit")
	}
	if p.waitErr != nil {
		t.Fatalf("owner exit: %v", p.waitErr)
	}
	if !childAlive() {
		t.Fatal("child exited before the inherited-pipe check")
	}
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for childAlive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if childAlive() {
		t.Fatal("supported descendant survived cleanup after owner exit")
	}
}

// Capture Linux's starttime before cancellation, so a recycled PID cannot be
// mistaken for the original fixture. A confirmed termination is permanent.
// Other platforms keep their native probe; none need Linux-specific syscalls.
func captureFixtureProcess(t *testing.T, pid int) func() bool {
	t.Helper()
	if runtime.GOOS != "linux" {
		return func() bool { return processPIDAlive(pid) }
	}
	initial, err := readLinuxFixtureProcess(pid)
	if err != nil {
		t.Fatalf("capture fixture process %d identity: %v", pid, err)
	}
	terminated := false
	return func() bool {
		t.Helper()
		if terminated {
			return false
		}
		current, readErr := readLinuxFixtureProcess(pid)
		alive, err := linuxFixtureProcessAlive(initial, current, readErr)
		if err != nil {
			t.Fatalf("observe fixture process %d identity: %v", pid, err)
		}
		terminated = !alive
		return alive
	}
}

type linuxFixtureProcess struct {
	state     byte
	startTime uint64
}

func readLinuxFixtureProcess(pid int) (linuxFixtureProcess, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return linuxFixtureProcess{}, err
	}
	return parseLinuxFixtureProcess(raw)
}

func parseLinuxFixtureProcess(raw []byte) (linuxFixtureProcess, error) {
	// comm (field 2) can contain spaces and parentheses. Fields after its final
	// ')' start with state (field 3); starttime is field 22, index 19 here.
	at := strings.LastIndexByte(string(raw), ')')
	if at < 0 {
		return linuxFixtureProcess{}, errors.New("invalid /proc process stat: missing comm")
	}
	fields := strings.Fields(string(raw[at+1:]))
	if len(fields) < 20 || len(fields[0]) != 1 {
		return linuxFixtureProcess{}, errors.New("invalid /proc process stat: missing state or starttime")
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return linuxFixtureProcess{}, fmt.Errorf("invalid /proc process starttime: %w", err)
	}
	return linuxFixtureProcess{state: fields[0][0], startTime: startTime}, nil
}

func linuxFixtureProcessAlive(initial, current linuxFixtureProcess, readErr error) (bool, error) {
	if errors.Is(readErr, os.ErrNotExist) || errors.Is(readErr, syscall.ESRCH) {
		return false, nil // The task was reaped after a previous observation.
	}
	if readErr != nil {
		return false, readErr // Permission/I/O/format errors cannot prove death.
	}
	if initial.startTime != current.startTime {
		return false, nil // This PID now identifies a different process.
	}
	switch current.state {
	case 'Z', 'X', 'x':
		return false, nil
	default:
		return true, nil
	}
}

func TestManagedProcessCancellationDuringRegistration(t *testing.T) {
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		stop := time.AfterFunc(100*time.Microsecond, cancel)
		p, err := startManagedProcess(ctx, processFixtureConfig("tree_child"), 128)
		stop.Stop()
		cancel()
		if err != nil {
			continue
		}
		if err := p.close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagedProcessPreCancelledStartsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, err := startManagedProcess(ctx, processFixtureConfig("tree_child"), 128)
	if p != nil || err != context.Canceled {
		if p != nil {
			_ = p.close()
		}
		t.Fatalf("pre-cancelled process started: %v", err)
	}
}

func TestManagedStderrKeepsBoundedTail(t *testing.T) {
	stderr := &boundedStderr{limit: 5}
	_, _ = stderr.Write([]byte("abc"))
	_, _ = stderr.Write([]byte("defg"))
	if string(stderr.data) != "cdefg" {
		t.Fatalf("stderr tail=%q", stderr.data)
	}
	_, _ = stderr.Write([]byte("0123456789"))
	if string(stderr.data) != "56789" {
		t.Fatalf("stderr oversized tail=%q", stderr.data)
	}
}

type processFailingClose struct {
	io.WriteCloser
	err error
}

func (p processFailingClose) Close() error { return errors.Join(p.WriteCloser.Close(), p.err) }

func TestManagedProcessCallbackPreservesCleanupError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := startManagedProcess(ctx, processFixtureConfig("tree_child"), 128)
	if err != nil {
		t.Fatal(err)
	}
	fixtureErr := errors.New("fixture owned pipe close failed")
	p.stdin = processFailingClose{WriteCloser: p.stdin, err: fixtureErr}
	cancel()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not clean up process")
	}
	for i := 0; i < 2; i++ {
		if err := p.close(); !errors.Is(err, fixtureErr) {
			t.Fatalf("callback cleanup error lost on explicit close: %v", err)
		}
	}
}
