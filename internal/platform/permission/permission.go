package permission

import (
	"fmt"
	"sort"
	"sync"
)

type Definition struct {
	Key   string
	Label string
	Group string
}

type Registry struct {
	mu   sync.RWMutex
	defs map[string]Definition
}

func NewRegistry() *Registry {
	return &Registry{defs: make(map[string]Definition)}
}

func (r *Registry) Register(defs ...Definition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range defs {
		if d.Key == "" {
			return fmt.Errorf("permission key is empty")
		}
		if _, exists := r.defs[d.Key]; exists {
			return fmt.Errorf("duplicate permission key %q", d.Key)
		}
		r.defs[d.Key] = d
	}
	return nil
}

func (r *Registry) Has(key string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.defs[key]
	return ok
}

func (r *Registry) All() []Definition {
	r.mu.RLock()
	out := make([]Definition, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group == out[j].Group {
			return out[i].Key < out[j].Key
		}
		return out[i].Group < out[j].Group
	})
	return out
}

func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.defs)
}
