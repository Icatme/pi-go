package pigo

import (
	"fmt"
	"sync"
)

type SessionResourceCleanup func(sessionID string)

var (
	sessionResourceCleanups   []SessionResourceCleanup
	sessionResourceCleanupsMu sync.RWMutex
)

func RegisterSessionResourceCleanup(cleanup SessionResourceCleanup) func() {
	sessionResourceCleanupsMu.Lock()
	defer sessionResourceCleanupsMu.Unlock()

	sessionResourceCleanups = append(sessionResourceCleanups, cleanup)
	index := len(sessionResourceCleanups) - 1

	return func() {
		sessionResourceCleanupsMu.Lock()
		defer sessionResourceCleanupsMu.Unlock()

		if index < len(sessionResourceCleanups) {
			sessionResourceCleanups = append(sessionResourceCleanups[:index], sessionResourceCleanups[index+1:]...)
		}
	}
}

func CleanupSessionResources(sessionID string) error {
	sessionResourceCleanupsMu.RLock()
	cleanups := make([]SessionResourceCleanup, len(sessionResourceCleanups))
	copy(cleanups, sessionResourceCleanups)
	sessionResourceCleanupsMu.RUnlock()

	var errs []error
	for _, cleanup := range cleanups {
		func() {
			defer func() {
				if r := recover(); r != nil {
					errs = append(errs, fmt.Errorf("session resource cleanup panicked: %v", r))
				}
			}()
			cleanup(sessionID)
		}()
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to cleanup %d session resource(s): %v", len(errs), errs)
	}
	return nil
}
