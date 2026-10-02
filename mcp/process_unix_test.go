//go:build !windows

package mcp

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func processPIDAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	// Linux may briefly retain an orphan's zombie entry before PID 1 reaps it.
	// Such a process has already terminated and holds no running code/resources.
	if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if at := strings.LastIndexByte(string(raw), ')'); at >= 0 && len(raw) > at+2 && raw[at+2] == 'Z' {
			return false
		}
	}
	return true
}
