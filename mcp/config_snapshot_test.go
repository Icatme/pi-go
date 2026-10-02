package mcp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func newColdSnapshotManager(t *testing.T) (*Manager, *atomic.Int32) {
	t.Helper()
	requests := new(atomic.Int32)
	manager, err := New(Config{
		Scope: Scope{Identity: "alice", AuthEpoch: 1},
		HTTPClient: &http.Client{Transport: managerTransportFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return nil, errors.New("unexpected I/O")
		})},
		Servers: []ServerConfig{
			{Name: "b", URL: "http://127.0.0.1:1", Headers: http.Header{"X-Fixture": []string{"original"}}, ToolRules: []ToolRule{{Pattern: "secret", Exposure: Hidden}}},
			{Name: "a", Command: "trusted-fixture", Trusted: true, Args: []string{"arg"}, Env: []string{"MODE=fixture"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeManager(t, manager) })
	return manager, requests
}

func TestConfigSnapshotIsInertDetachedAndManagerBound(t *testing.T) {
	manager, requests := newColdSnapshotManager(t)
	snapshot, err := manager.ConfigSnapshot()
	if err != nil || !manager.IsCurrentConfig(snapshot) || snapshot.Scope() != manager.Scope() || requests.Load() != 0 {
		t.Fatalf("snapshot performed I/O or missed its scope: err=%v snapshot=%+v requests=%d", err, snapshot, requests.Load())
	}
	servers := snapshot.Servers()
	if len(servers) != 2 || servers[0].Name != "a" || servers[1].Name != "b" {
		t.Fatalf("snapshot directory is not sorted: %+v", servers)
	}
	servers[0].Args[0] = "changed"
	servers[0].Env[0] = "MODE=changed"
	servers[1].Headers["X-Fixture"][0] = "changed"
	servers[1].ToolRules[0].Exposure = Direct
	servers[1].Exposure = Hidden
	servers = snapshot.Servers()
	if servers[0].Args[0] != "arg" || servers[0].Env[0] != "MODE=fixture" || servers[1].Headers.Get("X-Fixture") != "original" || servers[1].ToolRules[0].Exposure != Hidden || servers[1].Exposure != Codemode {
		t.Fatalf("returned configuration mutated its snapshot: %+v", servers)
	}
	if !manager.IsCurrentConfig(snapshot) || manager.Servers()[1].Headers.Get("X-Fixture") != "original" || requests.Load() != 0 {
		t.Fatal("returned configuration mutated the manager or triggered I/O")
	}
	other, _ := newColdSnapshotManager(t)
	if other.IsCurrentConfig(snapshot) || manager.IsCurrentConfig(ConfigSnapshot{}) {
		t.Fatal("currentness accepted another manager or an empty snapshot")
	}
	closeManager(t, manager)
	if manager.IsCurrentConfig(snapshot) {
		t.Fatal("closed manager still approved its snapshot")
	}
	if _, err := manager.ConfigSnapshot(); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed manager returned a new snapshot: %v", err)
	}
}

func TestConfigSnapshotInvalidatesColdPolicyAndScopeABA(t *testing.T) {
	for _, change := range []string{"server_hidden", "tool_exposure", "policy_ABA", "scope_ABA"} {
		t.Run(change, func(t *testing.T) {
			manager, requests := newColdSnapshotManager(t)
			snapshot, err := manager.ConfigSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "server_hidden", "policy_ABA":
				if err := manager.SetExposure("b", Hidden, nil); err != nil {
					t.Fatal(err)
				}
				if change == "policy_ABA" {
					if err := manager.SetExposure("b", Codemode, snapshot.Servers()[1].ToolRules); err != nil {
						t.Fatal(err)
					}
				}
			case "tool_exposure":
				if err := manager.SetExposure("b", Codemode, []ToolRule{{Pattern: "secret", Exposure: Direct}}); err != nil {
					t.Fatal(err)
				}
			case "scope_ABA":
				if err := manager.SetScope(Scope{Identity: "bob", AuthEpoch: 2}); err != nil {
					t.Fatal(err)
				}
				if err := manager.SetScope(snapshot.Scope()); err != nil {
					t.Fatal(err)
				}
			}
			if manager.IsCurrentConfig(snapshot) || requests.Load() != 0 {
				t.Fatalf("cold configuration change escaped invalidation: change=%s requests=%d", change, requests.Load())
			}
			current, err := manager.ConfigSnapshot()
			if err != nil || !manager.IsCurrentConfig(current) {
				t.Fatalf("fresh configuration was unavailable after change: %v", err)
			}
		})
	}
}

func TestConfigSnapshotConnectionStartAndReconnect(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "snapshot", Version: "1"}, nil)
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(httpServer.Close)
	manager, err := New(Config{Scope: Scope{Identity: "alice", AuthEpoch: 1}, Servers: []ServerConfig{{Name: "fixture", URL: httpServer.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeManager(t, manager) })
	snapshot, err := manager.ConfigSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Connect(t.Context(), "fixture")
	if err != nil || !manager.IsCurrentConfig(snapshot) {
		t.Fatalf("initial setup incorrectly invalidated unchanged policy: %v", err)
	}
	next, err := manager.Reconnect(t.Context(), "fixture")
	if err != nil || first == next || manager.IsCurrentConfig(snapshot) {
		t.Fatalf("reconnect did not invalidate the actual configuration generation: %v", err)
	}
	current, err := manager.ConfigSnapshot()
	if err != nil || !manager.IsCurrentConfig(current) {
		t.Fatalf("reconnected configuration not current: %v", err)
	}
	if err := manager.SetScope(current.Scope()); err != nil || !manager.IsCurrentConfig(current) {
		t.Fatalf("unchanged scope unexpectedly invalidated configuration: %v", err)
	}
}
