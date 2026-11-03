//go:build linux && !android

package fstools

import (
	"context"
	"errors"
	"os"
	"strconv"

	"github.com/Icatme/pi-go/agent"
	"golang.org/x/sys/unix"
)

func openRegular(ctx context.Context, execution agent.ToolExecutionContext, root *os.Root, path string) (*os.File, error) {
	fail := func(err error) (*os.File, error) {
		return nil, failure(agent.ToolFailureResource, "fstools: cannot open supported regular file", err)
	}
	// O_PATH pins an inode without opening a FIFO/device for I/O. Go Root adds
	// O_NOFOLLOW, so final symlinks yield symlink descriptors and are rejected.
	pinned, err := root.OpenFile(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(err)
	}
	defer pinned.Close()
	info, err := pinned.Stat()
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() {
		return fail(errors.New("not a regular inode"))
	}
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(pinned.Fd()), &filesystem); err != nil {
		return fail(err)
	}
	// Defense in depth, not an exhaustive virtual-filesystem classifier. Host
	// trust in the workspace filesystem/mount namespace remains required.
	if filesystem.Type == unix.PROC_SUPER_MAGIC || filesystem.Type == unix.SYSFS_MAGIC {
		return fail(errors.New("virtual filesystem is not supported"))
	}
	if err := permission(ctx, execution); err != nil {
		return nil, err
	}
	// Pin the proc directory too: reopen by decimal fd relative to this handle,
	// never by traversing the model-supplied pathname a second time.
	proc, err := openProcDirectory()
	if err != nil {
		return fail(err)
	}
	defer proc.Close()
	procFD := int(proc.Fd())
	if err := permission(ctx, execution); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(procFD, strconv.Itoa(int(pinned.Fd())), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return fail(err)
	}
	file := os.NewFile(uintptr(fd), "fstools-read")
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return fail(err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		file.Close()
		return fail(errors.New("pinned inode changed"))
	}
	return file, nil
}

// openProcDirectory pins the trusted host's proc fd directory for decimal-fd
// operations. Procfs type alone does not defend against malicious bind mounts.
func openProcDirectory() (*os.File, error) {
	fd, err := unix.Open("/proc/self/fd", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "fstools-proc-fd")
	var fs unix.Statfs_t
	if err = unix.Fstatfs(fd, &fs); err != nil {
		file.Close()
		return nil, err
	}
	if fs.Type != unix.PROC_SUPER_MAGIC {
		file.Close()
		return nil, errors.New("procfs is required")
	}
	return file, nil
}
