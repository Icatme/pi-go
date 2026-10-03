//go:build windows

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockSessionFile(file *os.File) error {
	// A cooperative lock beyond EOF leaves ordinary read-only log inspection usable.
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{OffsetHigh: 0x7fffffff, Offset: 0xffffffff})
	if err != nil {
		code := ErrorStorage
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			code = ErrorBusy
		}
		return sessionError(code, "session file cannot acquire exclusive writer lock", err)
	}
	return nil
}
