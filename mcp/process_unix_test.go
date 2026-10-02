//go:build !windows

package mcp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func processPIDAlive(pid int) bool {
	if runtime.GOOS == "linux" {
		current, readErr := readLinuxFixtureProcess(pid)
		alive, err := linuxFixtureProcessAlive(current, current, readErr)
		// An unclassified read error must fail a cleanup assertion, never pass
		// as evidence of death. The identity probe reports that error directly.
		return alive || err != nil
	}
	return syscall.Kill(pid, 0) == nil
}

func TestLinuxFixtureProcessStateAndIdentity(t *testing.T) {
	initial := linuxFixtureProcess{state: 'S', startTime: 1234}
	readErr := errors.New("fixture stat read failure")
	for _, tc := range []struct {
		name    string
		current linuxFixtureProcess
		err     error
		alive   bool
	}{
		{name: "running", current: linuxFixtureProcess{state: 'R', startTime: 1234}, alive: true},
		{name: "sleeping", current: initial, alive: true},
		{name: "stopped", current: linuxFixtureProcess{state: 'T', startTime: 1234}, alive: true},
		{name: "zombie", current: linuxFixtureProcess{state: 'Z', startTime: 1234}},
		{name: "dead", current: linuxFixtureProcess{state: 'X', startTime: 1234}},
		{name: "dead_lowercase", current: linuxFixtureProcess{state: 'x', startTime: 1234}},
		{name: "reused_pid", current: linuxFixtureProcess{state: 'R', startTime: 5678}},
		{name: "reaped", err: &os.PathError{Op: "read", Path: "/proc/42/stat", Err: syscall.ENOENT}},
		{name: "task_gone", err: &os.PathError{Op: "read", Path: "/proc/42/stat", Err: syscall.ESRCH}},
		{name: "permission_error", err: &os.PathError{Op: "read", Path: "/proc/42/stat", Err: syscall.EACCES}},
		{name: "io_error", err: readErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alive, err := linuxFixtureProcessAlive(initial, tc.current, tc.err)
			if alive != tc.alive {
				t.Fatalf("alive=%t, want %t", alive, tc.alive)
			}
			if tc.err != nil && !errors.Is(tc.err, os.ErrNotExist) && !errors.Is(tc.err, syscall.ESRCH) {
				if !errors.Is(err, tc.err) {
					t.Fatalf("observation error lost: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected observation error: %v", err)
			}
		})
	}
}

func TestLinuxFixtureProcessStatParsing(t *testing.T) {
	for _, state := range []byte{'R', 'Z', 'X', 'x'} {
		raw := []byte(fmt.Sprintf("42 (fixture with (nested) comm) %c %s1234 0", state, strings.Repeat("0 ", 18)))
		got, err := parseLinuxFixtureProcess(raw)
		if err != nil || got.state != state || got.startTime != 1234 {
			t.Fatalf("state %c parse=%#v err=%v", state, got, err)
		}
	}
	for _, raw := range []string{
		"42 invalid",
		"42 (fixture) S 0",
		"42 (fixture) SS " + strings.Repeat("0 ", 18) + "1234",
		"42 (fixture) S " + strings.Repeat("0 ", 18) + "bad_starttime",
	} {
		if _, err := parseLinuxFixtureProcess([]byte(raw)); err == nil {
			t.Fatalf("invalid process stat accepted: %q", raw)
		}
	}
}

func TestLinuxFixtureProcessProbeLiveThenReaped(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux /proc process identity")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestManagedProcessFixture$")
	cmd.Env = append(os.Environ(), "PI_GO_MCP_HELPER=tree_child")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("fixture cleanup kill: %v", err)
			}
			_ = cmd.Wait()
		}
	})
	alive := captureFixtureProcess(t, cmd.Process.Pid)
	if !alive() || !processPIDAlive(cmd.Process.Pid) {
		t.Fatal("probe falsely classified a live fixture as terminated")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	waited = true
	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		t.Fatalf("fixture kill did not return an exit status: %v", err)
	}
	if alive() {
		t.Fatal("reaped fixture was classified as alive")
	}
}
