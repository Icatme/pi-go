package pigo

import (
	"fmt"
	"sync"
)

type SessionResourceCleanup func(sessionID string)

// SessionResourceCleanupOptional is the variant that allows omitting sessionID.
// Use this when registering cleanups that do not need a session identifier.
type SessionResourceCleanupOptional func()

type sessionResourceCleanupEntry struct {
	id      uint64
	cleanup SessionResourceCleanup
}

var (
	sessionResourceCleanups      []sessionResourceCleanupEntry
	sessionResourceCleanupsMu    sync.RWMutex
	sessionResourceCleanupNextID uint64
)

func RegisterSessionResourceCleanup(cleanup SessionResourceCleanup) func() {
	return registerSessionResourceCleanupInternal(cleanup)
}

// RegisterSessionResourceCleanupOptional registers a cleanup that does not
// require a sessionID. It wraps the cleanup so it can be stored alongside
// session-aware cleanups.
func RegisterSessionResourceCleanupOptional(cleanup SessionResourceCleanupOptional) func() {
	return registerSessionResourceCleanupInternal(func(string) { cleanup() })
}

func registerSessionResourceCleanupInternal(cleanup SessionResourceCleanup) func() {
	sessionResourceCleanupsMu.Lock()
	defer sessionResourceCleanupsMu.Unlock()

	sessionResourceCleanupNextID++
	entry := sessionResourceCleanupEntry{
		id:      sessionResourceCleanupNextID,
		cleanup: cleanup,
	}
	sessionResourceCleanups = append(sessionResourceCleanups, entry)

	return func() {
		sessionResourceCleanupsMu.Lock()
		defer sessionResourceCleanupsMu.Unlock()

		for index, registered := range sessionResourceCleanups {
			if registered.id != entry.id {
				continue
			}
			sessionResourceCleanups = append(sessionResourceCleanups[:index], sessionResourceCleanups[index+1:]...)
			break
		}
	}
}

func CleanupSessionResources(sessionID string) error {
	sessionResourceCleanupsMu.RLock()
	cleanups := make([]sessionResourceCleanupEntry, len(sessionResourceCleanups))
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
			cleanup.cleanup(sessionID)
		}()
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to cleanup %d session resource(s): %v", len(errs), errs)
	}
	return nil
}
