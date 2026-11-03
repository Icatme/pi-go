//go:build linux && !android

package fstools

import (
	"errors"
	"os"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func openChildDirectory(parent *os.File, name string) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return nil, errors.New("invalid directory entry")
	}
	// One basename relative to the still-open parent descriptor. Unlike a fresh
	// Root.OpenFile call, this must never follow a replacement final symlink.
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	runtime.KeepAlive(parent)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "fstools-find-directory"), nil
}
