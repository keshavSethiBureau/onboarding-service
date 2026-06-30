// Package app wires the application together (config -> peripherals -> repos ->
// services -> controllers -> router) and runs the HTTP server. It mirrors the
// dendrite-store Container/Wire/Run setup.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Bureau-Inc/bureau-commons-go/metricx"
	"github.com/Bureau-Inc/bureau-commons-go/telemetry"
	telemetryconfig "github.com/Bureau-Inc/bureau-commons-go/telemetry/config"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"github.com/bureau/onboarding-service/internal/config"
	"github.com/bureau/onboarding-service/internal/controller"
)

// defaultConfigPath is used when CONFIG_FILE is not set.
const defaultConfigPath = "config.yml"

// shutdownTimeout bounds how long Run waits for in-flight requests to drain
// before forcing the server closed.
const shutdownTimeout = 10 * time.Second

// Container holds the assembled application dependencies. As foundation
// peripherals are wired (telemetry, Mongo, Apollo cache, metricx, redis — see
// agent_docs/onboarding-lld.md §9), they are added here and torn down by Close.
type Container struct {
	Router *gin.Engine
	Cfg    *config.Config
	// Close tears down peripherals in reverse order of wiring. It is a no-op
	// for now (no telemetry/DB/redis yet); each teardown is added as the
	// corresponding peripheral is wired in Wire.
	Close func() error
}

// Wire constructs the application dependencies and returns a Container.
//
// Wiring order (extended as peripherals are added): config -> telemetry ->
// mongo -> redis -> repos -> services -> controllers -> router.
func Wire() (*Container, error) {
	// config (boot/infra) — path from CONFIG_FILE, default config.yml
	configPath := os.Getenv("CONFIG_FILE")
	if configPath == "" {
		configPath = defaultConfigPath
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// telemetry (OpenTelemetry) — global tracer provider + propagator. The OTLP
	// exporter batches in the background, so this succeeds even with no collector.
	if err := telemetry.Init(
		telemetryconfig.WithServiceName(cfg.Telemetry.ServiceName),
		telemetryconfig.WithEndpoint(cfg.Telemetry.OTLPEndpoint),
	); err != nil {
		return nil, fmt.Errorf("failed to initialize telemetry: %w", err)
	}

	// metricx (Prometheus) — registry for the /metrics exposition + HTTP metrics.
	registry := metricx.NewRegistry()

	// controllers
	healthCtrl := controller.NewHealthController()

	// router: trace + count every request, then register routes.
	r := gin.Default()
	r.Use(otelgin.Middleware(cfg.Telemetry.ServiceName))
	r.Use(controller.MetricsMiddleware(registry))
	healthCtrl.RegisterRoutes(r)
	r.GET("/metrics", gin.WrapH(metricx.NewHandler(registry, &metricx.Options{})))

	return &Container{
		Router: r,
		Cfg:    cfg,
		Close: func() error {
			return telemetry.Shutdown()
		},
	}, nil
}

// Run starts the HTTP server and handles graceful shutdown on SIGINT/SIGTERM:
// in-flight requests are drained (up to shutdownTimeout) before peripherals are
// torn down via Container.Close.
func Run(c *Container) error {
	// Port comes from boot config (configloader), which resolves ${PORT:8080}
	// — so the PORT env var still overrides, now funneled through config.
	addr := ":" + c.Cfg.Server.Port

	srv := &http.Server{Addr: addr, Handler: c.Router}

	serverErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()
	log.Printf("server starting on %s", addr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		return err
	case <-sigCh:
		log.Println("shutdown signal received, draining in-flight requests")
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown timed out: %v", err)
		}
		return c.Close()
	}
}
