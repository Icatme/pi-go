package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type connectionSlot struct {
	config     ServerConfig
	connection *Connection
	done       chan struct{}
	err        error
	cancel     context.CancelFunc
	generation uint64
}

type Manager struct {
	mu         sync.Mutex
	config     Config
	ctx        context.Context
	cancel     context.CancelFunc
	slots      map[string]*connectionSlot
	generation uint64
	closed     bool
	retired    map[*connectionSlot]struct{}
	closeDone  chan struct{}
	closeErr   error
}

// New validates configuration without launching processes, network requests,
// or OAuth. The host explicitly chooses when to Connect or Authenticate.
func New(config Config) (*Manager, error) {
	c, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{config: c, ctx: ctx, cancel: cancel, slots: map[string]*connectionSlot{}, retired: map[*connectionSlot]struct{}{}, closeDone: make(chan struct{})}
	for _, s := range c.Servers {
		m.slots[s.Name] = &connectionSlot{config: s}
	}
	return m, nil
}

func (m *Manager) Scope() Scope { m.mu.Lock(); defer m.mu.Unlock(); return m.config.Scope }
func (m *Manager) Servers() []ServerConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ServerConfig, 0, len(m.slots))
	for _, slot := range m.slots {
		out = append(out, cloneServerConfig(slot.config))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Connect joins one bounded connection attempt. Failure remains visible until
// an explicit Reconnect, rather than silently retrying an operation.
func (m *Manager) Connect(ctx context.Context, name string) (*Connection, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	slot := m.slots[name]
	if slot == nil || slot.config.Disabled {
		m.mu.Unlock()
		return nil, ErrHidden
	}
	if slot.done == nil {
		life, cancel := context.WithCancel(m.ctx)
		slot.cancel = cancel
		slot.done = make(chan struct{})
		m.generation++
		slot.generation = m.generation
		config := m.config
		server := slot.config
		generation := slot.generation
		go func() {
			connection, err := connect(life, server, config.Scope, generation, config)
			m.mu.Lock()
			current := m.slots[name] == slot && !m.closed
			slot.connection = connection
			if current {
				slot.err = err
			} else {
				slot.err = ErrStale
			}
			m.mu.Unlock()
			if !current && connection != nil {
				_ = connection.Close()
			}
			close(slot.done)
		}()
	}
	done := slot.done
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if m.slots[name] != slot {
		return nil, ErrStale
	}
	if slot.err != nil {
		return nil, slot.err
	}
	if slot.connection == nil {
		return nil, ErrNotReady
	}
	if err := slot.connection.available(); err != nil {
		return nil, err
	}
	return slot.connection, nil
}

// Ready does no I/O. Indirect server setup need not delay the first model call.
func (m *Manager) Ready(name string) (*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	s := m.slots[name]
	if s == nil || s.config.Disabled {
		return nil, ErrHidden
	}
	if s.connection != nil {
		if err := s.connection.available(); err != nil {
			return nil, err
		}
		return s.connection, nil
	}
	if s.err != nil {
		return nil, s.err
	}
	return nil, ErrNotReady
}

func (m *Manager) Reconnect(ctx context.Context, name string) (*Connection, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	old := m.slots[name]
	if old == nil {
		m.mu.Unlock()
		return nil, ErrHidden
	}
	m.slots[name] = &connectionSlot{config: old.config}
	m.retired[old] = struct{}{}
	if old.cancel != nil {
		old.cancel()
	}
	m.mu.Unlock()
	if err := m.retire(old); err != nil {
		return nil, err
	}
	return m.Connect(ctx, name)
}

// SetScope retires real SDK sessions and their wire bindings, not just an outer
// cache key. It cannot be called with a model-supplied identity.
func (m *Manager) SetScope(scope Scope) error {
	if !validIdentity(scope.Identity) || scope.AuthEpoch == 0 {
		return errors.New("mcp: valid trusted scope required")
	}
	m.mu.Lock()
	old, err := m.replaceScopeLocked(scope)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range old {
		errs = append(errs, m.retire(s))
	}
	return errors.Join(errs...)
}

func (m *Manager) replaceScopeLocked(scope Scope) (map[string]*connectionSlot, error) {
	if m.closed {
		return nil, ErrClosed
	}
	if m.config.Scope == scope {
		return nil, nil
	}
	old := m.slots
	m.slots = map[string]*connectionSlot{}
	m.config.Scope = scope
	for name, s := range old {
		m.retired[s] = struct{}{}
		m.slots[name] = &connectionSlot{config: s.config}
		if s.cancel != nil {
			s.cancel()
		}
	}
	return old, nil
}

func (m *Manager) retire(slot *connectionSlot) error {
	if slot.done != nil {
		<-slot.done
	}
	var err error
	if slot.connection != nil {
		err = slot.connection.Close()
	}
	m.mu.Lock()
	delete(m.retired, slot)
	m.mu.Unlock()
	return err
}

// SetExposure immediately replaces host policy. Already frozen scripts keep
// their definitions, but old connections cannot dispatch any further calls.
func (m *Manager) SetExposure(name string, exposure Exposure, rules []ToolRule) error {
	if !validExposure(exposure) || len(rules) > 256 {
		return errors.New("mcp: invalid exposure policy")
	}
	for _, r := range rules {
		if r.Pattern == "" || len(r.Pattern) > 256 || !validExposure(r.Exposure) {
			return errors.New("mcp: invalid tool rule")
		}
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	old := m.slots[name]
	if old == nil {
		m.mu.Unlock()
		return ErrHidden
	}
	config := old.config
	config.Exposure = exposure
	config.ToolRules = append([]ToolRule(nil), rules...)
	m.slots[name] = &connectionSlot{config: config}
	m.retired[old] = struct{}{}
	if old.cancel != nil {
		old.cancel()
	}
	m.mu.Unlock()
	return m.retire(old)
}

// Authenticate is host initiated. Successful credential changes retire all
// sessions of the identity epoch before another tool can be sent.
func (m *Manager) Authenticate(ctx context.Context, name string) error {
	return m.changeCredentials(ctx, name, false)
}

// ClearCredentials is a host-requested logout. It clears the bound credential
// with a versioned store operation and retires every session of the old epoch.
func (m *Manager) ClearCredentials(ctx context.Context, name string) error {
	return m.changeCredentials(ctx, name, true)
}

func (m *Manager) changeCredentials(ctx context.Context, name string, clear bool) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	slot := m.slots[name]
	if slot == nil || slot.config.OAuth == nil {
		m.mu.Unlock()
		return errors.New("mcp: OAuth not configured")
	}
	config := slot.config
	scope := m.config.Scope
	m.mu.Unlock()
	key := config.AuthKey
	key.Server = name
	key.URL = config.URL
	key.Identity = scope.Identity
	o, err := NewOAuth(ctx, key, *config.OAuth)
	if err != nil {
		return err
	}
	defer o.Close()
	var retired map[string]*connectionSlot
	o.commit = func(commitCtx context.Context, version uint64, credential *OAuthCredential) (uint64, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed || m.config.Scope != scope || m.slots[name] != slot {
			return 0, ErrStale
		}
		if err := commitCtx.Err(); err != nil {
			return 0, err
		}
		if scope.AuthEpoch == ^uint64(0) {
			return 0, errors.New("mcp: identity epoch exhausted")
		}
		// Existing executors do not acquire m.mu before physical handoff. Stop
		// them before CAS, including the interval after a store commits but has
		// not returned. Cancellation never waits or invokes host callbacks here.
		retired = make(map[string]*connectionSlot, len(m.slots))
		for currentName, current := range m.slots {
			retired[currentName] = current
			m.retired[current] = struct{}{}
			if current.cancel != nil {
				current.cancel()
			}
		}
		nextVersion, storeErr := o.store.CompareAndSwap(commitCtx, o.key, version, credential)
		if storeErr != nil && nextVersion == 0 {
			return 0, storeErr
		}
		// A nonzero version also reports publication when cleanup subsequently
		// fails. Retire that epoch while retaining the original storage cause.
		nextScope := scope
		nextScope.AuthEpoch++
		var replaceErr error
		retired, replaceErr = m.replaceScopeLocked(nextScope)
		return nextVersion, errors.Join(storeErr, replaceErr)
	}
	if clear {
		err = o.Clear(ctx)
	} else {
		err = o.Authenticate(ctx)
	}
	var errs []error
	errs = append(errs, err)
	for _, s := range retired {
		errs = append(errs, m.retire(s))
	}
	return errors.Join(errs...)
}

// OAuthState reads only the host credential store. It does not connect, launch
// a login flow or expose token material. A concurrent identity change rejects it.
func (m *Manager) OAuthState(ctx context.Context, name string) (OAuthState, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return OAuthState{}, ErrClosed
	}
	slot, scope := m.slots[name], m.config.Scope
	if slot == nil || slot.config.OAuth == nil {
		m.mu.Unlock()
		return OAuthState{}, errors.New("mcp: OAuth not configured")
	}
	config := slot.config
	m.mu.Unlock()
	key := config.AuthKey
	key.Server = name
	key.URL = config.URL
	key.Identity = scope.Identity
	o, err := NewOAuth(ctx, key, *config.OAuth)
	if err != nil {
		return OAuthState{}, err
	}
	defer o.Close()
	state := o.State()
	m.mu.Lock()
	current := !m.closed && m.config.Scope == scope && m.slots[name] == slot
	m.mu.Unlock()
	if !current {
		return OAuthState{}, ErrStale
	}
	return state, nil
}

func (m *Manager) Catalogs(ctx context.Context, includeIndirect bool) ([]Catalog, error) {
	if !includeIndirect {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	servers := m.Servers()
	out := make([]Catalog, 0, len(servers))
	for _, s := range servers {
		if s.Disabled || s.Exposure == Hidden && len(s.ToolRules) == 0 {
			continue
		}
		var c *Connection
		var err error
		if includeIndirect || s.NeedsDirect() {
			c, err = m.Connect(ctx, s.Name)
		} else {
			c, err = m.Ready(s.Name)
			if errors.Is(err, ErrNotReady) {
				continue
			}
		}
		if err != nil {
			return nil, fmt.Errorf("mcp: directory %s: %w", s.Name, err)
		}
		snap, err := c.Refresh(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, nil
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		select {
		case <-done:
			return m.closeErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.closed = true
	m.cancel()
	slots := make(map[*connectionSlot]struct{}, len(m.slots)+len(m.retired))
	for _, s := range m.slots {
		slots[s] = struct{}{}
	}
	for s := range m.retired {
		slots[s] = struct{}{}
	}
	m.mu.Unlock()
	go func() {
		var wg sync.WaitGroup
		errs := make(chan error, len(slots))
		for s := range slots {
			wg.Go(func() {
				if s.done != nil {
					<-s.done
				}
				if s.connection != nil {
					errs <- s.connection.Close()
				}
			})
		}
		wg.Wait()
		close(errs)
		var collected []error
		for err := range errs {
			collected = append(collected, err)
		}
		m.mu.Lock()
		m.closeErr = errors.Join(collected...)
		close(m.closeDone)
		m.mu.Unlock()
	}()
	select {
	case <-m.closeDone:
		return m.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
