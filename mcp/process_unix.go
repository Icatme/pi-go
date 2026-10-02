//go:build !windows

package mcp

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type processControl struct{}

func prepareProcess(cmd *exec.Cmd) (*processControl, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &processControl{}, nil
}
func (*processControl) attach(*os.Process) error { return nil }
func (*processControl) close() error             { return nil }
func (*processControl) kill(p *os.Process) error {
	// The trusted server contract forbids escaping this group. Portable Unix
	// process groups are lifecycle ownership, not arbitrary descendant containment.
	err := syscall.Kill(-p.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return processGroupKillError(p.Pid, err)
}
