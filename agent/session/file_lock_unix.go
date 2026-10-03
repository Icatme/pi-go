//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockSessionFile(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err != nil {
		code := ErrorStorage
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			code = ErrorBusy
		}
		return sessionError(code, "session file cannot acquire exclusive writer lock", err)
	}
	return nil
}
