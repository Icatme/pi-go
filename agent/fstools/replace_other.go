//go:build !linux || android

package fstools

import "os"

func newPendingReplace(*os.Root, string, func() error) (pendingCreation, error) {
	return nil, ErrUnsupportedPlatform
}
