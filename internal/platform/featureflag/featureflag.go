package featureflag

import "sync"

type Flags struct {
	mu     sync.RWMutex
	values map[string]bool
}

func New() *Flags { return &Flags{values: make(map[string]bool)} }

func (f *Flags) Set(key string, enabled bool) {
	f.mu.Lock()
	f.values[key] = enabled
	f.mu.Unlock()
}

func (f *Flags) Enabled(key string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.values[key]
}

func (f *Flags) Snapshot() map[string]bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string]bool, len(f.values))
	for k, v := range f.values {
		out[k] = v
	}
	return out
}
