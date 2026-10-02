//go:build !windows

package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDetachedProcessFixture(t *testing.T) {
	switch os.Getenv("PI_GO_MCP_DETACHED_HELPER") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestDetachedProcessFixture$")
		child.Env = append(os.Environ(), "PI_GO_MCP_DETACHED_HELPER=child")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, child.Process.Pid)
		for {
			time.Sleep(time.Hour)
		}
	case "child":
		for {
			time.Sleep(time.Hour)
		}
	}
}

// A portable Unix process group is not a descendant-containment mechanism.
// Prove the documented setsid boundary with real processes, and independently
// reclaim the deliberately unsupported fixture so this test never leaks it.
func TestUnixProcessGroupExcludesDetachedDescendants(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux process identity to verify independent reclamation")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	config := ServerConfig{Name: "detached", Command: os.Args[0], Args: []string{"-test.run=^TestDetachedProcessFixture$"}, Env: []string{"PI_GO_MCP_DETACHED_HELPER=parent"}, Trusted: true}
	p, err := startManagedProcess(ctx, config, 128)
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
			t.Fatalf("detached PID: %q %v", value, err)
		}
	case <-ctx.Done():
		t.Fatal("detached fixture did not start")
	}
	childAlive := captureFixtureProcess(t, child)
	defer func() {
		if err := syscall.Kill(child, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("independent detached-fixture cleanup: %v", err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for childAlive() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if childAlive() {
			t.Error("detached fixture was not independently reclaimed")
		}
	}()
	parentGroup, err := syscall.Getpgid(p.cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	childGroup, err := syscall.Getpgid(child)
	if err != nil {
		t.Fatal(err)
	}
	if parentGroup == childGroup {
		t.Fatal("setsid fixture did not escape the assigned group")
	}
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
	if !childAlive() {
		t.Fatal("detached fixture does not demonstrate the unsupported boundary")
	}
}
