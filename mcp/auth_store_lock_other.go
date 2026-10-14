//go:build !windows

package mcp

import (
	"errors"
	"os"
)

func authCreateLockDirectory(path string) (bool, error) {
	err := os.Mkdir(path, 0700)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	return err == nil, err
}
