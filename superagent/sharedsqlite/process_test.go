//go:build (linux || darwin || windows) && (amd64 || arm64)

package sharedsqlite

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessLeaseHelper(t *testing.T) {
	mode := os.Getenv("PIGO_SQLITE_HELPER")
	if mode == "" {
		return
	}
	j, err := Open(context.Background(), os.Getenv("PIGO_SQLITE_PATH"), Options{Domain: "shared", Owner: "process-" + mode, LeaseTTL: time.Second})
	if mode == "contend" {
		if !errors.Is(err, ErrBusy) {
			fmt.Fprintln(os.Stderr, "expected busy:", err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if _, err = j.AppendEntry("main", transition("1")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "ready")
	if mode == "suspend" {
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			os.Exit(3)
		}
		_, err = j.AppendEntry("main", transition("2"))
		if !errors.Is(err, ErrLeaseLost) {
			fmt.Fprintln(os.Stderr, "stale process append:", err)
			os.Exit(4)
		}
		fmt.Fprintln(os.Stdout, "fenced")
		os.Exit(0)
	}
	// No defer/Close: parent kills this process to test real crash recovery.
	select {}
}
func TestSubprocessContentionAndCrashRecovery(t *testing.T) {
	p := filepath.Join(privateDir(t), "process.db")
	command := func(mode string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProcessLeaseHelper$")
		cmd.Env = append(os.Environ(), "PIGO_SQLITE_HELPER="+mode, "PIGO_SQLITE_PATH="+p)
		return cmd
	}
	holder := command("holder")
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	holder.Stderr = os.Stderr
	if err = holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case line := <-ready:
		if line != "ready" {
			t.Fatalf("holder not ready: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("holder timeout")
	}
	if output, err := command("contend").CombinedOutput(); err != nil {
		t.Fatalf("independent contender: %v %s", err, output)
	}
	if err = holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	time.Sleep(1100 * time.Millisecond)
	j, err := Open(context.Background(), p, Options{Domain: "shared", Owner: "recovery", LeaseTTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if j.Epoch() != 2 || len(j.Log()) != 1 {
		t.Fatalf("crash recovery epoch=%d entries=%d", j.Epoch(), len(j.Log()))
	}
	if _, err = j.AppendEntry("main", transition("2")); err != nil {
		t.Fatal(err)
	}
}
