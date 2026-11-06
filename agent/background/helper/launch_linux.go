package helper

import (
	"os/exec"
	"slices"
	"syscall"
)

func launch(c Command, dir string) (<-chan struct{}, error) {
	cmd := exec.Command(c.Executable, append(slices.Clone(c.Args), dir)...)
	cmd.Dir = dir
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		// Wait only reaps the child and withdraws liveness. Exit status never
		// becomes a completed receipt or evidence of absence of native effects.
		_ = cmd.Wait()
		close(done)
	}()
	return done, nil
}
