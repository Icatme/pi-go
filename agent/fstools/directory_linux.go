//go:build linux && !android

package fstools

import (
	"os"
	"syscall"
)

const supported = true

func openDirectory(root *os.Root, path string) (*os.File, error) {
	// Reject non-directories in the open syscall, not with a racy Stat/Open
	// sequence. In particular, a FIFO must never wait for a writer.
	return root.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
}
