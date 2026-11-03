//go:build !linux || android

package fstools

import "os"

func newPendingCreate(*os.Root, string) (pendingCreation, error) { return nil, ErrUnsupportedPlatform }
