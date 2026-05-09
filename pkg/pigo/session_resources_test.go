package pigo

import (
	"errors"
	"testing"
)

func isolateSessionResourceCleanups(t *testing.T) {
	t.Helper()

	sessionResourceCleanupsMu.Lock()
	previousCleanups := append([]sessionResourceCleanupEntry(nil), sessionResourceCleanups...)
	previousNextID := sessionResourceCleanupNextID
	sessionResourceCleanups = nil
	sessionResourceCleanupNextID = 0
	sessionResourceCleanupsMu.Unlock()

	t.Cleanup(func() {
		sessionResourceCleanupsMu.Lock()
		defer sessionResourceCleanupsMu.Unlock()
		sessionResourceCleanups = previousCleanups
		sessionResourceCleanupNextID = previousNextID
	})
}

func TestRegisterSessionResourceCleanup(t *testing.T) {
	isolateSessionResourceCleanups(t)

	called := false
	cleanup := func(sessionID string) {
		called = true
	}

	unregister := RegisterSessionResourceCleanup(cleanup)
	if unregister == nil {
		t.Fatal("expected unregister function")
	}

	_ = CleanupSessionResources("test-session")
	if !called {
		t.Fatal("expected cleanup to be called")
	}
}

func TestUnregisterSessionResourceCleanup(t *testing.T) {
	isolateSessionResourceCleanups(t)

	called := false
	cleanup := func(sessionID string) {
		called = true
	}

	unregister := RegisterSessionResourceCleanup(cleanup)
	unregister()

	_ = CleanupSessionResources("test-session")
	if called {
		t.Fatal("expected cleanup NOT to be called after unregister")
	}
}

func TestCleanupSessionResourcesWithMultipleCleanups(t *testing.T) {
	isolateSessionResourceCleanups(t)

	var order []int
	c1 := func(string) { order = append(order, 1) }
	c2 := func(string) { order = append(order, 2) }

	RegisterSessionResourceCleanup(c1)
	RegisterSessionResourceCleanup(c2)

	_ = CleanupSessionResources("test-session")
	if len(order) != 2 {
		t.Fatalf("expected 2 cleanups, got %d", len(order))
	}
}

func TestUnregisterSessionResourceCleanupOutOfOrder(t *testing.T) {
	isolateSessionResourceCleanups(t)

	var order []int
	unregisterFirst := RegisterSessionResourceCleanup(func(string) { order = append(order, 1) })
	RegisterSessionResourceCleanup(func(string) { order = append(order, 2) })
	unregisterThird := RegisterSessionResourceCleanup(func(string) { order = append(order, 3) })

	unregisterFirst()
	unregisterThird()

	_ = CleanupSessionResources("test-session")
	if len(order) != 1 || order[0] != 2 {
		t.Fatalf("expected only middle cleanup to remain after out-of-order unregister, got %v", order)
	}
}

func TestCleanupSessionResourcesRecoversFromPanic(t *testing.T) {
	isolateSessionResourceCleanups(t)

	called := false
	panicky := func(string) { panic("intentional panic") }
	normal := func(string) { called = true }

	RegisterSessionResourceCleanup(panicky)
	RegisterSessionResourceCleanup(normal)

	err := CleanupSessionResources("test-session")
	if err == nil {
		t.Fatal("expected error from panicked cleanup")
	}
	if !called {
		t.Fatal("expected normal cleanup to still be called")
	}
}

func TestCleanupSessionResourcesPassesSessionID(t *testing.T) {
	isolateSessionResourceCleanups(t)

	var receivedID string
	cleanup := func(sessionID string) {
		receivedID = sessionID
	}

	RegisterSessionResourceCleanup(cleanup)
	_ = CleanupSessionResources("specific-session-id")

	if receivedID != "specific-session-id" {
		t.Fatalf("expected session ID 'specific-session-id', got %q", receivedID)
	}
}

func TestCleanupSessionResourcesReturnsErrorForFailedCleanup(t *testing.T) {
	isolateSessionResourceCleanups(t)

	failing := func(string) { panic(errors.New("cleanup failed")) }

	RegisterSessionResourceCleanup(failing)

	err := CleanupSessionResources("test-session")
	if err == nil {
		t.Fatal("expected error from failing cleanup")
	}
}

func TestRegisterSessionResourceCleanupOptional(t *testing.T) {
	isolateSessionResourceCleanups(t)

	called := false
	cleanup := func() {
		called = true
	}

	unregister := RegisterSessionResourceCleanupOptional(cleanup)
	if unregister == nil {
		t.Fatal("expected unregister function")
	}

	_ = CleanupSessionResources("test-session")
	if !called {
		t.Fatal("expected optional cleanup to be called")
	}
}

func TestRegisterSessionResourceCleanupOptionalIgnoresSessionID(t *testing.T) {
	isolateSessionResourceCleanups(t)

	var receivedID string
	cleanup := func() {
		receivedID = "was-called"
	}

	RegisterSessionResourceCleanupOptional(cleanup)
	_ = CleanupSessionResources("any-session-id")

	if receivedID != "was-called" {
		t.Fatal("expected optional cleanup to be called regardless of sessionID")
	}
}
