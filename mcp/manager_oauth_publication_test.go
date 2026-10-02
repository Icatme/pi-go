package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManagerOAuthRefreshRetiresBeforeConcurrentTokenUse(t *testing.T) {
	f := newManagerOAuthFixture(t)
	f.refresh = func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed-access", "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": "read"})
	}
	options := f.options()
	options.Store = NewMemoryCredentialStore()
	m := f.manager(t, options)
	c := authenticateAndConnect(t, m)
	before := c.oauth.State()
	expireConnectionToken(t, c)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Pause the connection's existing retirement lock boundary without changing
	// its state. A successful refresh has already committed to the real store.
	c.authMu.Lock()
	locked := true
	defer func() {
		if locked {
			c.authMu.Unlock()
		}
	}()
	refreshDone := make(chan error, 1)
	go func() { _, err := c.oauth.token(ctx); refreshDone <- err }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := options.Store.Load(ctx, c.oauth.Key())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Version == before.CredentialVersion+2 && snapshot.Credential != nil && !snapshot.Credential.RefreshPending {
			break
		}
		select {
		case err := <-refreshDone:
			t.Fatalf("refresh completed before retirement lock: %v", err)
		case <-ctx.Done():
			t.Fatal("refresh did not commit its terminal result")
		case <-ticker.C:
		}
	}
	type callResult struct {
		record DispatchRecord
		err    error
	}
	callDone := make(chan callResult, 1)
	go func() {
		_, record, err := c.CallToolTracked(ctx, &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}})
		callDone <- callResult{record, err}
	}()
	select {
	case result := <-callDone:
		t.Fatalf("tool finished before terminal-state retirement: attempts=%d err=%v posts=%d calls=%d", result.record.Attempts, result.err, f.toolPosts.Load(), f.toolCalls.Load())
	case <-time.After(50 * time.Millisecond):
	}
	if f.toolPosts.Load() != 0 || f.toolCalls.Load() != 0 {
		t.Fatal("changed-scope credential escaped before the retirement barrier")
	}
	c.authMu.Unlock()
	locked = false
	select {
	case <-refreshDone:
	case <-ctx.Done():
		t.Fatal("refresh did not finish after releasing retirement lock")
	}
	select {
	case result := <-callDone:
		if result.err == nil || result.record.Attempts != 0 || f.toolPosts.Load() != 0 || f.toolCalls.Load() != 0 {
			t.Fatalf("retired session dispatched a tool: attempts=%d err=%v posts=%d calls=%d", result.record.Attempts, result.err, f.toolPosts.Load(), f.toolCalls.Load())
		}
	case <-ctx.Done():
		t.Fatal("concurrent tool did not observe retirement")
	}
	awaitOAuthConnectionClose(t, c)
	if !c.stale.Load() || c.ctx.Err() == nil {
		t.Fatal("terminal-state publication did not mark stale and cancel the session")
	}
}

func TestOAuthExternalOnChangeCanReenterSameTokenSource(t *testing.T) {
	f := newOAuthFixture(t)
	options := f.options()
	var o *OAuth
	changed := make(chan error, 2)
	options.OnChange = func(state OAuthState) {
		if got := o.State(); got.CredentialVersion != state.CredentialVersion {
			t.Error("callback did not observe the published snapshot")
		}
		source, err := o.TokenSource(t.Context())
		if err == nil {
			_, err = source.Token()
		}
		changed <- err
	}
	var err error
	o, err = NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	done := make(chan error, 1)
	go func() { done <- o.Authenticate(t.Context()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("external callback deadlocked on the OAuth gate or state mutex")
	}
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
}
