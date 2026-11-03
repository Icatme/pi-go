//go:build !linux || android

package fstools

import "os"

func openChildDirectory(_ *os.File, _ string) (*os.File, error) { return nil, ErrUnsupportedPlatform }
