package app

import (
	"fmt"
	"sort"

	"goforum/internal/platform/adminui"
	"goforum/internal/platform/authn"
	"goforum/internal/platform/events"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/settings"
)

type Registry struct {
	modules []Module
	byName  map[string]Module
	models  []any
	perms   []permission.Definition
	sets    []settings.Definition
}

// NewRegistry validates names/dependencies and returns modules in dependency order.
func NewRegistry(mods ...Module) (*Registry, error) {
	byName := make(map[string]Module, len(mods))
	for _, m := range mods {
		name := m.Name()
		if name == "" {
			return nil, fmt.Errorf("module with empty name")
		}
		if _, exists := byName[name]; exists {
			return nil, fmt.Errorf("duplicate module %q", name)
		}
		byName[name] = m
	}
	for _, m := range mods {
		for _, dep := range m.DependsOn() {
			if _, ok := byName[dep]; !ok {
				return nil, fmt.Errorf("module %q depends on unregistered module %q", m.Name(), dep)
			}
		}
	}

	// Depth-first topological sort, preserving declaration order for determinism.
	const (
		unvisited = iota
		visiting
		done
	)
	state := make(map[string]int, len(mods))
	ordered := make([]Module, 0, len(mods))
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case visiting:
			return fmt.Errorf("module dependency cycle at %q", name)
		case done:
			return nil
		}
		state[name] = visiting
		m := byName[name]
		for _, dep := range m.DependsOn() {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = done
		ordered = append(ordered, m)
		return nil
	}
	for _, m := range mods {
		if err := visit(m.Name()); err != nil {
			return nil, err
		}
	}

	r := &Registry{modules: ordered, byName: byName}
	for _, m := range ordered {
		r.models = append(r.models, m.Models()...)
		r.perms = append(r.perms, m.Permissions()...)
		r.sets = append(r.sets, m.Settings()...)
	}
	return r, nil
}

func (r *Registry) Modules() []Module                    { return r.modules }
func (r *Registry) Models() []any                        { return r.models }
func (r *Registry) Permissions() []permission.Definition { return r.perms }
func (r *Registry) Settings() []settings.Definition      { return r.sets }

func (r *Registry) Subscribe(bus events.Bus) error {
	for _, m := range r.modules {
		if err := m.Subscribe(bus); err != nil {
			return fmt.Errorf("module %s subscribe: %w", m.Name(), err)
		}
	}
	return nil
}

func (r *Registry) ModuleInfos() []adminui.ModuleInfo {
	out := make([]adminui.ModuleInfo, 0, len(r.modules))
	for _, m := range r.modules {
		out = append(out, adminui.ModuleInfo{
			Name:            m.Name(),
			DependsOn:       m.DependsOn(),
			PermissionCount: len(m.Permissions()),
			SettingCount:    len(m.Settings()),
		})
	}
	return out
}

// NavFor aggregates and permission-filters navigation from every module.
func (r *Registry) NavFor(p *authn.Principal) []adminui.NavItem {
	var out []adminui.NavItem
	seen := make(map[string]bool)
	for _, m := range r.modules {
		for _, item := range m.Admin().Nav {
			if item.Permission != "" && !p.Can(item.Permission) {
				continue
			}
			key := item.Group + "|" + item.URL + "|" + item.Label
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// AllModels returns platform models plus module models.
func (r *Registry) AllModels(platform ...any) []any {
	out := append([]any{}, platform...)
	out = append(out, r.models...)
	return out
}
