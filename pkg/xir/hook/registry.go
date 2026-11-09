package hook

import (
	"fmt"
	"sort"
	"sync"
)

// Registry maps a hook ID to the Factory that builds it.
//
// The zero value is ready to use. A Registry is safe for concurrent Lookup and
// IDs calls; Register is normally done once during startup. A Registry must
// not be copied after first use.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// Register adds a factory under id. It panics if id is empty, f is nil or id is
// already registered: duplicate registration is a programming error, not a
// runtime condition.
func (r *Registry) Register(id string, f Factory) {
	if id == "" {
		panic("hook: register with empty ID")
	}
	if f == nil {
		panic(fmt.Sprintf("hook: register %q with nil factory", id))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.factories == nil {
		r.factories = make(map[string]Factory)
	}
	if _, exists := r.factories[id]; exists {
		panic(fmt.Sprintf("hook: duplicate registration of %q", id))
	}
	r.factories[id] = f
}

// Lookup returns the factory registered under id.
func (r *Registry) Lookup(id string) (Factory, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[id]
	return f, ok
}

// IDs returns every registered ID in sorted order. The returned slice is a
// fresh copy.
func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.factories))
	for id := range r.factories {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
