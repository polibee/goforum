// Package placeholder provides M0 module manifests for domains implemented in later milestones.
// Each placeholder already declares its permissions, settings and dependencies so the admin
// shell, permission registry and settings registry exercise the real aggregation path.
package placeholder

import (
	"github.com/gin-gonic/gin"

	"goforum/internal/platform/adminui"
	"goforum/internal/platform/events"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/settings"
)

type Module struct {
	name  string
	deps  []string
	perms []permission.Definition
	sets  []settings.Definition
	nav   []adminui.NavItem
}

func New(name string, deps []string, perms []permission.Definition, sets []settings.Definition, nav []adminui.NavItem) *Module {
	return &Module{name: name, deps: deps, perms: perms, sets: sets, nav: nav}
}

func (m *Module) Name() string                         { return m.name }
func (m *Module) DependsOn() []string                  { return m.deps }
func (m *Module) Models() []any                        { return nil }
func (m *Module) Routes(_, _ *gin.RouterGroup)         {}
func (m *Module) Subscribe(events.Bus) error           { return nil }
func (m *Module) Admin() adminui.Contribution          { return adminui.Contribution{Nav: m.nav} }
func (m *Module) Permissions() []permission.Definition { return m.perms }
func (m *Module) Settings() []settings.Definition      { return m.sets }
