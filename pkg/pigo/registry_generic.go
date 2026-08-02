package pigo

import "sync"

// Registry stores static values or lazy factories behind a concurrency-safe key lookup.
type Registry[K comparable, V any] struct {
	mu          sync.RWMutex
	entries     map[K]*registryEntry[V]
	resolveHook func(K)
}

type registryEntry[V any] struct {
	value   *V
	factory func() V
}

// NewRegistry creates an empty concurrency-safe registry.
func NewRegistry[K comparable, V any]() *Registry[K, V] {
	return &Registry[K, V]{entries: map[K]*registryEntry[V]{}}
}

// Register adds a static value or lazy factory for a key.
func (r *Registry[K, V]) Register(key K, value *V, factory func() V) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[key]; exists {
		panic("pigo: duplicate registry entry")
	}
	r.entries[key] = &registryEntry[V]{value: value, factory: factory}
}

// Resolve returns a cached value for a key, materializing its lazy factory at most once.
func (r *Registry[K, V]) Resolve(key K) *V {
	r.mu.RLock()
	entry := r.entries[key]
	if entry == nil {
		r.mu.RUnlock()
		return nil
	}
	if entry.value != nil {
		value := entry.value
		r.mu.RUnlock()
		return value
	}
	factory := entry.factory
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	entry = r.entries[key]
	if entry == nil {
		return nil
	}
	if entry.value != nil {
		return entry.value
	}
	value := factory()
	entry.value = &value
	if r.resolveHook != nil {
		r.resolveHook(key)
	}
	return entry.value
}

// Keys lists the currently registered keys without forcing lazy factories to resolve.
func (r *Registry[K, V]) Keys() []K {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := make([]K, 0, len(r.entries))
	for key := range r.entries {
		keys = append(keys, key)
	}
	return keys
}

// Delete removes a key without resolving any lazy entry.
func (r *Registry[K, V]) Delete(key K) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, key)
}

// Replace swaps an existing entry for a resolved value without exposing a
// partially updated value to concurrent readers. It returns false when the key
// is not registered.
func (r *Registry[K, V]) Replace(key K, value *V) bool {
	if value == nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[key]; !exists {
		return false
	}
	r.entries[key] = &registryEntry[V]{value: value}
	return true
}

// SetResolveHook installs a callback invoked when a lazy entry resolves.
func (r *Registry[K, V]) SetResolveHook(hook func(K)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolveHook = hook
}

func (r *Registry[K, V]) snapshot(cloneValue func(*V) *V) map[K]*registryEntry[V] {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cloned := make(map[K]*registryEntry[V], len(r.entries))
	for key, entry := range r.entries {
		if entry == nil {
			cloned[key] = nil
			continue
		}
		entryCopy := *entry
		if entry.value != nil {
			entryCopy.value = cloneValue(entry.value)
		}
		cloned[key] = &entryCopy
	}
	return cloned
}

func (r *Registry[K, V]) restore(entries map[K]*registryEntry[V]) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = entries
}
