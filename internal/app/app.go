// Package app wires repositories, services, and controllers onto the HTTP
// engine. For now it only registers the health controller; repos/services are
// added here as they are built.
package app

import (
	"github.com/gin-gonic/gin"

	"github.com/bureau/onboarding-service/internal/controller"
)

// App holds the assembled HTTP engine and its dependencies.
type App struct {
	engine *gin.Engine
}

// New builds the application: it creates the Gin engine and registers routes.
func New() *App {
	engine := gin.Default()

	controller.NewHealthController().RegisterRoutes(engine)

	return &App{engine: engine}
}

// Run starts the HTTP server on the given address (e.g. ":8080").
func (a *App) Run(addr string) error {
	return a.engine.Run(addr)
}
