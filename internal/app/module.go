package app

import (
	"github.com/gin-gonic/gin"

	"goforum/internal/platform/adminui"
	"goforum/internal/platform/events"
	"goforum/internal/platform/permission"
	"goforum/internal/platform/settings"
)

// Module is the single contract every feature module implements.
type Module interface {
	Name() string
	DependsOn() []string
	Models() []any
	Routes(web, api *gin.RouterGroup)
	Admin() adminui.Contribution
	Permissions() []permission.Definition
	Settings() []settings.Definition
	Subscribe(bus events.Bus) error
}
