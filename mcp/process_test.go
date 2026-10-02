package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
	case "tree_parent":
		cmd := exec.Command(os.Args[0], "-test.run=^TestManagedProcessFixture$")
		cmd.Env = append(os.Environ(), "PI_GO_MCP_HELPER=tree_child")
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, cmd.Process.Pid)
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
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if processPIDAlive(pid) {
		t.Fatal("closed stdio parent still alive")
	}
}

func TestManagedProcessOwnsWholeTree(t *testing.T) {
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
	if !processPIDAlive(child) {
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
	for processPIDAlive(child) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processPIDAlive(child) {
		t.Fatalf("managed descendant escaped cleanup: %d", child)
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
