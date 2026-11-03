//go:build linux && !android

package fstools

import (
	"errors"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type linuxPendingCreate struct {
	*os.File
	parent, proc *os.File
	name         string
}

func newPendingCreate(root *os.Root, filename string) (pendingCreation, error) {
	name := path.Base(filename)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return nil, errors.New("invalid file basename")
	}
	parent, err := openDirectory(root, path.Dir(filename))
	if err != nil {
		return nil, err
	}
	proc, err := openProcDirectory()
	if err != nil {
		parent.Close()
		return nil, err
	}
	fd, err := unix.Openat(int(parent.Fd()), ".", unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	runtime.KeepAlive(parent)
	if err != nil {
		parent.Close()
		proc.Close()
		return nil, err
	}
	return &linuxPendingCreate{File: os.NewFile(uintptr(fd), "fstools-anonymous-create"), parent: parent, proc: proc, name: name}, nil
}
func (p *linuxPendingCreate) Publish() error {
	// The source is a host-generated decimal descriptor name, not model input.
	// Do not add O_EXCL to O_TMPFILE: that would forbid this atomic publication.
	err := unix.Linkat(int(p.proc.Fd()), strconv.Itoa(int(p.File.Fd())), int(p.parent.Fd()), p.name, unix.AT_SYMLINK_FOLLOW)
	runtime.KeepAlive(p)
	return err
}
func (p *linuxPendingCreate) SyncDirectory() error { return p.parent.Sync() }
func (p *linuxPendingCreate) Close() error {
	return errors.Join(p.File.Close(), p.parent.Close(), p.proc.Close())
}
