package pigo

import (
	"errors"
	"testing"
)

func TestRegisterSessionResourceCleanup(t *testing.T) {
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

func TestCleanupSessionResourcesRecoversFromPanic(t *testing.T) {
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
	failing := func(string) { panic(errors.New("cleanup failed")) }

	RegisterSessionResourceCleanup(failing)

	err := CleanupSessionResources("test-session")
	if err == nil {
		t.Fatal("expected error from failing cleanup")
	}
}
