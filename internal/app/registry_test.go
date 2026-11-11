package app

import (
	"testing"

	"github.com/gin-gonic/gin"

	"goforum/internal/platform/adminui"
	"goforum/internal/platform/events"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/settings"
)

type fakeModule struct {
	name string
	deps []string
}

func (f fakeModule) Name() string                         { return f.name }
func (f fakeModule) DependsOn() []string                  { return f.deps }
func (f fakeModule) Models() []any                        { return nil }
func (f fakeModule) Routes(_, _ *gin.RouterGroup)         {}
func (f fakeModule) Admin() adminui.Contribution          { return adminui.Contribution{} }
func (f fakeModule) Permissions() []permission.Definition { return nil }
func (f fakeModule) Settings() []settings.Definition      { return nil }
func (f fakeModule) Subscribe(events.Bus) error           { return nil }

func TestRegistryTopologicalOrder(t *testing.T) {
	a := fakeModule{name: "a"}
	b := fakeModule{name: "b", deps: []string{"a"}}
	c := fakeModule{name: "c", deps: []string{"b"}}

	reg, err := NewRegistry(c, b, a)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	got := make([]string, 0, 3)
	for _, m := range reg.Modules() {
		got = append(got, m.Name())
	}
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestRegistryCycle(t *testing.T) {
	a := fakeModule{name: "a", deps: []string{"b"}}
	b := fakeModule{name: "b", deps: []string{"a"}}
	if _, err := NewRegistry(a, b); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestRegistryMissingDependency(t *testing.T) {
	a := fakeModule{name: "a", deps: []string{"missing"}}
	if _, err := NewRegistry(a); err == nil {
		t.Fatal("expected missing dependency error")
	}
}

func TestRegistryDuplicate(t *testing.T) {
	a1 := fakeModule{name: "a"}
	a2 := fakeModule{name: "a"}
	if _, err := NewRegistry(a1, a2); err == nil {
		t.Fatal("expected duplicate module error")
	}
}
