package mcp

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDarwinProcessGroupCleanupAfterZombieExit(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestManagedProcessFixture$")
	cmd.Env = append(os.Environ(), "PI_GO_MCP_HELPER=tree_child")
	control, err := prepareProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("fixture cleanup kill: %v", err)
		}
		_ = cmd.Wait()
	})
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// Keep the child unreaped so the process group deterministically contains
	// a zombie. Cmd.Wait is deliberately deferred until after the assertion.
	deadline := time.Now().Add(5 * time.Second)
	for {
		members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		if len(members) == 1 && members[0].Proc.P_stat == darwinZombieProcess {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child never became a zombie: %#v", members)
		}
		time.Sleep(time.Millisecond)
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("XNU zombie-only group signal: %v, want EPERM", err)
	}
	if err := control.kill(cmd.Process); err != nil {
		t.Fatalf("zombie-only group cleanup: %v", err)
	}
}

func TestDarwinProcessGroupCleanupRetainsPermissionErrors(t *testing.T) {
	// Inspect this test's live process group. An EPERM for a group with a live
	// member must remain an error even if that member is otherwise signalable.
	pgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := processGroupKillError(pgid, syscall.EPERM); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("live group permission error hidden: %v", err)
	}
	if err := processGroupKillError(pgid, syscall.EIO); !errors.Is(err, syscall.EIO) {
		t.Fatalf("non-permission error hidden: %v", err)
	}
}

func TestDarwinGroupKillErrorDistinguishesLiveMembers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []int8
		failed bool
	}{
		{name: "empty"},
		{name: "zombies", states: []int8{5, 5}},
		{name: "creating", states: []int8{1}, failed: true},
		{name: "running", states: []int8{2}, failed: true},
		{name: "sleeping", states: []int8{3}, failed: true},
		{name: "stopped", states: []int8{4}, failed: true},
		{name: "mixed", states: []int8{5, 3}, failed: true},
		{name: "unknown", states: []int8{0}, failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			members := make([]unix.KinfoProc, len(tc.states))
			for i, state := range tc.states {
				members[i].Proc.P_stat = state
			}
			err := darwinGroupKillError(syscall.EPERM, members)
			if errors.Is(err, syscall.EPERM) != tc.failed {
				t.Fatalf("cleanup error=%v, failed=%t", err, tc.failed)
			}
		})
	}
}
