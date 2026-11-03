//go:build !linux || android

package fstools

import "os"

const supported = false

func openDirectory(_ *os.Root, _ string) (*os.File, error) {
	return nil, ErrUnsupportedPlatform
}
