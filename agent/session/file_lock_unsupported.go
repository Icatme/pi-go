//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package session

import "os"

func lockSessionFile(*os.File) error {
	return sessionError(ErrorStorage, "exclusive session writers are unsupported on this platform", nil)
}
