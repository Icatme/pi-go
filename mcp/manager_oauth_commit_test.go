package mcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type oauthMutationGateStore struct {
	*MemoryCredentialStore
	loadArmed               atomic.Bool
	clearArmed              atomic.Bool
	entered                 chan struct{}
	release                 chan struct{}
	clearCalls              atomic.Int32
	failure                 error
	failureAfterPublication bool
}

func (s *oauthMutationGateStore) Load(ctx context.Context, key AuthKey) (CredentialSnapshot, error) {
	snapshot, err := s.MemoryCredentialStore.Load(ctx, key)
	if err == nil && s.loadArmed.CompareAndSwap(true, false) {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return CredentialSnapshot{}, ctx.Err()
		}
	}
	return snapshot, err
}

func (s *oauthMutationGateStore) CompareAndSwap(ctx context.Context, key AuthKey, version uint64, credential *OAuthCredential) (uint64, error) {
	if credential != nil {
		return s.MemoryCredentialStore.CompareAndSwap(ctx, key, version, credential)
	}
	s.clearCalls.Add(1)
	if s.failure != nil && !s.failureAfterPublication {
		return 0, s.failure
	}
	next, err := s.MemoryCredentialStore.CompareAndSwap(ctx, key, version, nil)
	if err == nil && s.failureAfterPublication {
		return next, s.failure
	}
	if err == nil && s.clearArmed.CompareAndSwap(true, false) {
		// The durable deletion has happened, but CompareAndSwap has not returned.
		// SetScope and Connect must not pass the manager's publication boundary.
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return next, ctx.Err()
		}
	}
	return next, err
}

func newOAuthMutationGateStore(t *testing.T) (*oauthMutationGateStore, func()) {
	t.Helper()
	s := &oauthMutationGateStore{MemoryCredentialStore: NewMemoryCredentialStore(), entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(s.release) }) }
	t.Cleanup(release)
	return s, release
}

func TestManagerOAuthScopeChangeRejectsClearBeforeCAS(t *testing.T) {
	f := newManagerOAuthFixture(t)
	store, release := newOAuthMutationGateStore(t)
	options := f.options()
	options.Store = store
	m := f.manager(t, options)
	old := authenticateAndConnect(t, m)
	before, err := store.Load(t.Context(), old.oauth.Key())
	if err != nil {
		t.Fatal(err)
	}
	store.loadArmed.Store(true)
	done := make(chan error, 1)
	go func() { done <- m.ClearCredentials(t.Context(), "fixture") }()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("clear did not reach preparation gate")
	}
	newScope := Scope{Identity: "account", AuthEpoch: old.Scope().AuthEpoch + 10}
	if err := m.SetScope(newScope); err != nil {
		t.Fatal(err)
	}
	current, err := m.Connect(t.Context(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStale) {
			t.Fatalf("stale clear was not rejected before CAS: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale clear did not finish")
	}
	after, err := store.Load(t.Context(), current.oauth.Key())
	if err != nil || after.Version != before.Version || after.Credential == nil || store.clearCalls.Load() != 0 || m.Scope() != newScope {
		t.Fatalf("stale preparation changed credentials/current epoch: version=%d CAS=%d err=%v", after.Version, store.clearCalls.Load(), err)
	}
	if _, record, err := current.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"}); err != nil || record.Attempts != 1 || f.toolCalls.Load() != 1 {
		t.Fatalf("rejected old clear disturbed authorized new epoch: record=%+v err=%v", record, err)
	}
}

func TestManagerOAuthClearCASSerializesScopeAndStopsOldToken(t *testing.T) {
	f := newManagerOAuthFixture(t)
	store, release := newOAuthMutationGateStore(t)
	options := f.options()
	options.Store = store
	m := f.manager(t, options)
	old := authenticateAndConnect(t, m)
	newScope := Scope{Identity: "account", AuthEpoch: old.Scope().AuthEpoch + 10}
	store.clearArmed.Store(true)
	clearDone := make(chan error, 1)
	go func() { clearDone <- m.ClearCredentials(t.Context(), "fixture") }()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("clear did not commit CAS")
	}
	deleted, err := store.Load(t.Context(), old.oauth.Key())
	if err != nil || deleted.Credential != nil {
		t.Fatalf("fixture did not durably delete before CAS returned: %v", err)
	}
	if _, record, err := old.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"}); err == nil || record.Attempts != 0 || f.toolPosts.Load() != 0 || f.toolCalls.Load() != 0 {
		t.Fatalf("committed clear left an existing token source usable: record=%+v err=%v", record, err)
	}
	scopeStarted, scopeDone := make(chan struct{}), make(chan error, 1)
	go func() { close(scopeStarted); scopeDone <- m.SetScope(newScope) }()
	<-scopeStarted
	select {
	case err := <-scopeDone:
		t.Fatalf("SetScope passed an unfinished credential/session commit: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case err := <-clearDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("clear did not finish")
	}
	select {
	case err := <-scopeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scope replacement did not finish")
	}
	if m.Scope() != newScope {
		t.Fatalf("scope replacement lost its ordering: %+v", m.Scope())
	}
	if current, err := m.Connect(t.Context(), "fixture"); !errors.Is(err, ErrAuthRequired) || current != nil || f.toolPosts.Load() != 0 {
		t.Fatalf("new epoch restored a durably cleared token: connection=%p err=%v posts=%d", current, err, f.toolPosts.Load())
	}
	awaitOAuthConnectionClose(t, old)
}

func TestManagerOAuthFailedCASClosesCanceledSessionAndRetainsCause(t *testing.T) {
	f := newManagerOAuthFixture(t)
	store, _ := newOAuthMutationGateStore(t)
	options := f.options()
	options.Store = store
	m := f.manager(t, options)
	c := authenticateAndConnect(t, m)
	if _, err := c.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	scope := m.Scope()
	before, err := store.Load(t.Context(), c.oauth.Key())
	if err != nil {
		t.Fatal(err)
	}
	private := errors.New("private credential storage detail")
	store.failure = private
	if err := m.ClearCredentials(t.Context(), "fixture"); !errors.Is(err, private) || strings.Contains(err.Error(), "private credential storage detail") {
		t.Fatalf("failed CAS lost its cause or exposed it: %v", err)
	}
	awaitOAuthConnectionClose(t, c)
	sessionDone := make(chan error, 1)
	go func() { sessionDone <- c.session.Wait() }()
	select {
	case <-sessionDone:
	case <-time.After(5 * time.Second):
		t.Fatal("failed CAS left SDK session active")
	}
	c.observer.mu.Lock()
	observerClosed := c.observer.closed
	c.observer.mu.Unlock()
	if !observerClosed {
		t.Fatal("failed CAS left wire observer active")
	}
	after, err := store.Load(t.Context(), c.oauth.Key())
	if err != nil || after.Version != before.Version || after.Credential == nil || m.Scope() != scope {
		t.Fatalf("failed CAS changed durable credential/epoch: version=%d err=%v", after.Version, err)
	}
	if _, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"}); err == nil || record.Attempts != 0 || f.toolPosts.Load() != 0 {
		t.Fatalf("failed logout restored an old session: record=%+v err=%v", record, err)
	}
}

func TestManagerOAuthOnChangeCanReenterAfterAtomicPublication(t *testing.T) {
	f := newManagerOAuthFixture(t)
	options := f.options()
	var m *Manager
	states := make(chan OAuthState, 2)
	options.OnChange = func(state OAuthState) {
		// These acquire the manager mutex and construct a fresh store-backed
		// OAuth owner. The publication guard and OAuth gate must both be free.
		scope := m.Scope()
		if scope.Identity != "account" || len(m.Servers()) != 1 {
			t.Error("callback did not observe the published manager configuration")
		}
		if _, err := m.Ready("fixture"); !errors.Is(err, ErrNotReady) {
			t.Errorf("callback observed an old session after publication: %v", err)
		}
		stored, err := m.OAuthState(t.Context(), "fixture")
		if err != nil || stored.Authenticated != state.Authenticated || stored.CredentialVersion != state.CredentialVersion {
			t.Errorf("callback did not observe the committed credential state: %+v %v", stored, err)
		}
		states <- state
	}
	m = f.manager(t, options)
	done := make(chan error, 1)
	go func() { done <- m.Authenticate(t.Context(), "fixture") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("login callback deadlocked on the publication guard")
	}
	if state := <-states; !state.Authenticated {
		t.Fatal("login callback did not observe authentication")
	}
	go func() { done <- m.ClearCredentials(t.Context(), "fixture") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("clear callback deadlocked on the publication guard")
	}
	if state := <-states; state.Authenticated {
		t.Fatal("clear callback observed old authentication")
	}
}

func TestManagerOAuthCommittedClearErrorStillPublishesAndRetires(t *testing.T) {
	f := newManagerOAuthFixture(t)
	store, _ := newOAuthMutationGateStore(t)
	options := f.options()
	options.Store = store
	var m *Manager
	var observed OAuthState
	options.OnChange = func(state OAuthState) {
		_ = m.Scope() // Also prove an error-path notification runs after unlock.
		observed = state
	}
	m = f.manager(t, options)
	c := authenticateAndConnect(t, m)
	scope := m.Scope()
	before := c.oauth.State()
	private := errors.New("private post-publication cleanup detail")
	store.failure, store.failureAfterPublication = private, true
	if err := m.ClearCredentials(t.Context(), "fixture"); !errors.Is(err, private) || strings.Contains(err.Error(), "private post-publication cleanup detail") {
		t.Fatalf("committed clear cleanup error lost cause or exposed it: %v", err)
	}
	if m.Scope().AuthEpoch != scope.AuthEpoch+1 || observed.Authenticated || observed.CredentialVersion != before.CredentialVersion+1 {
		t.Fatalf("committed deletion did not publish new epoch/state: scope=%+v state=%+v", m.Scope(), observed)
	}
	awaitOAuthConnectionClose(t, c)
	if _, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"}); err == nil || record.Attempts != 0 || f.toolPosts.Load() != 0 {
		t.Fatalf("committed clear cleanup failure restored old token: record=%+v err=%v", record, err)
	}
}
