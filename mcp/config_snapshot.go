package mcp

import "sort"

// ConfigSnapshot freezes the scope and complete server policy used to build a
// directory, including servers that have never connected. Its private slot
// identities detect replacement even when policy later returns to the same
// values; starting a connection in an unchanged slot does not invalidate it.
type ConfigSnapshot struct {
	manager *Manager
	scope   Scope
	servers []ServerConfig
	slots   map[string]*connectionSlot
}

func (snapshot ConfigSnapshot) Scope() Scope { return snapshot.scope }

// Servers returns detached configuration. Mutating it cannot change the
// snapshot's policy or its currentness checks.
func (snapshot ConfigSnapshot) Servers() []ServerConfig {
	out := make([]ServerConfig, len(snapshot.servers))
	for i, server := range snapshot.servers {
		out[i] = cloneServerConfig(server)
	}
	return out
}

// ConfigSnapshot performs no I/O and captures all configuration under one lock.
func (m *Manager) ConfigSnapshot() (ConfigSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ConfigSnapshot{}, ErrClosed
	}
	snapshot := ConfigSnapshot{
		manager: m, scope: m.config.Scope,
		servers: make([]ServerConfig, 0, len(m.slots)),
		slots:   make(map[string]*connectionSlot, len(m.slots)),
	}
	for name, slot := range m.slots {
		snapshot.servers = append(snapshot.servers, cloneServerConfig(slot.config))
		snapshot.slots[name] = slot
	}
	sort.Slice(snapshot.servers, func(i, j int) bool { return snapshot.servers[i].Name < snapshot.servers[j].Name })
	return snapshot, nil
}

// IsCurrentConfig rejects closed managers, another manager's snapshot and every
// scope/policy/session replacement, including changes to a cold server or ABA.
func (m *Manager) IsCurrentConfig(snapshot ConfigSnapshot) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || snapshot.manager != m || snapshot.scope != m.config.Scope || len(snapshot.slots) != len(m.slots) {
		return false
	}
	for name, slot := range snapshot.slots {
		if m.slots[name] != slot {
			return false
		}
	}
	return true
}
