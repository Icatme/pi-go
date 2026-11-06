//go:build !linux

package helper

func launch(Command, string) (<-chan struct{}, error) { return nil, ErrInvalid }
