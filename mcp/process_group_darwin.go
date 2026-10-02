package mcp

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func processGroupKillError(pgid int, err error) error {
	if !errors.Is(err, syscall.EPERM) {
		return err
	}
	// XNU killpg1 excludes zombies before counting signalable group members.
	// A group containing only zombies therefore returns EPERM, even though it
	// has no remaining process to terminate. Inspect the exact group rather
	// than treating every permission error as successful cleanup.
	members, inspectErr := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if inspectErr != nil {
		return errors.Join(err, fmt.Errorf("mcp: inspect process group %d: %w", pgid, inspectErr))
	}
	return darwinGroupKillError(err, members)
}

// SZOMB is the stable Darwin extern_proc process-state value from sys/proc.h.
const darwinZombieProcess = 5

func darwinGroupKillError(err error, members []unix.KinfoProc) error {
	for _, member := range members {
		if member.Proc.P_stat != darwinZombieProcess {
			return err
		}
	}
	return nil
}
