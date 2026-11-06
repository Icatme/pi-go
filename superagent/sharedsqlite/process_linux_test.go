//go:build linux && (amd64 || arm64)

package sharedsqlite

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSuspendedProcessFencedAfterTakeover(t *testing.T) {
	p := filepath.Join(privateDir(t), "suspend.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessLeaseHelper$")
	cmd.Env = append(os.Environ(), "PIGO_SQLITE_HELPER=suspend", "PIGO_SQLITE_PATH="+p)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lines := make(chan string, 2)
	go func() {
		s := bufio.NewScanner(out)
		for s.Scan() {
			lines <- s.Text()
		}
		close(lines)
	}()
	select {
	case line := <-lines:
		if line != "ready" {
			t.Fatalf("not ready: %s", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("start timeout")
	}
	if err = cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	replacement, err := Open(context.Background(), p, Options{Domain: "shared", Owner: "replacement", LeaseTTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if replacement.Epoch() != 2 {
		t.Fatalf("epoch %d", replacement.Epoch())
	}
	if err = cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(in, "append")
	in.Close()
	select {
	case line := <-lines:
		if line != "fenced" {
			t.Fatalf("not fenced: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stale process timeout")
	}
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err = replacement.AppendEntry("main", transition("2")); err != nil {
		t.Fatal(err)
	}
}
