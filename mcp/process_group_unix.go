//go:build !windows && !darwin

package mcp

func processGroupKillError(_ int, err error) error { return err }
