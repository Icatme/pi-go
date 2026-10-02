package mcp

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestManagerManagedStdioExitInvalidatesCatalog(t *testing.T) {
	m, err := New(Config{Scope: Scope{Identity: "alice", AuthEpoch: 1}, Servers: []ServerConfig{processFixtureConfig("server")}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, m)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	c, err := m.Connect(ctx, "stdio")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := c.Refresh(ctx)
	if err != nil || len(catalog.Tools) != 1 || !c.IsCurrent(catalog) {
		t.Fatalf("initial catalog: tools=%d current=%t err=%v", len(catalog.Tools), c.IsCurrent(catalog), err)
	}
	// Unexpected owner exit, independent of Connection.Close or its context.
	if err := c.process.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.process.done:
	case <-ctx.Done():
		t.Fatal("owned server did not exit")
	}
	var exit *exec.ExitError
	if err := c.available(); !errors.Is(err, ErrClosed) || !errors.As(err, &exit) {
		t.Fatalf("unexpected server exit cause lost: %v", err)
	}
	assertDisconnectedCatalog(t, ctx, m, c, catalog)
	select {
	case <-c.closeDone:
	case <-ctx.Done():
		t.Fatal("unexpected exit did not complete managed teardown")
	}
	fresh, err := m.Reconnect(ctx, "stdio")
	if err != nil || fresh == c || fresh.Generation() <= c.Generation() {
		t.Fatalf("explicit reconnect: fresh=%p old=%p err=%v", fresh, c, err)
	}
	if _, err := fresh.Refresh(ctx); err != nil {
		t.Fatalf("fresh connection catalog: %v", err)
	}
}

func TestManagerSessionExitInvalidatesCatalog(t *testing.T) {
	m, c := newManagerFixture(t, wireTestServer(), true)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	catalog, err := c.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Terminate the actual SDK session without invoking the managed wrapper.
	if err := c.session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.sessionDone:
	case <-ctx.Done():
		t.Fatal("session exit was not observed")
	}
	assertDisconnectedCatalog(t, ctx, m, c, catalog)
	select {
	case <-c.closeDone:
	case <-ctx.Done():
		t.Fatal("session exit did not complete managed teardown")
	}
}

func assertDisconnectedCatalog(t *testing.T, ctx context.Context, m *Manager, c *Connection, catalog Catalog) {
	t.Helper()
	if ready, err := m.Ready(c.config.Name); ready != nil || !errors.Is(err, ErrClosed) {
		t.Errorf("Ready accepted a terminated session: %p %v", ready, err)
	}
	if reused, err := m.Connect(ctx, c.config.Name); reused != nil || !errors.Is(err, ErrClosed) {
		t.Errorf("Connect reused a terminated session: %p %v", reused, err)
	}
	if c.IsCurrent(catalog) {
		t.Error("terminated session validated a frozen catalog")
	}
	tracked, record := c.Track(ctx)
	if _, err := c.Refresh(tracked); !errors.Is(err, ErrClosed) {
		t.Errorf("terminated session returned its SDK catalog cache: %v", err)
	}
	if record().Attempts != 0 {
		t.Error("terminated session attempted catalog I/O")
	}
}
