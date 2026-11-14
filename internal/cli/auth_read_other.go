//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package cli

import "os"

// Do not fall back to a blocking device/FIFO read on unsupported platforms.
func openCLIAuthRead(string) (*os.File, error) { return nil, os.ErrInvalid }
