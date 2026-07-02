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

	"github.com/Bureau-Inc/bureau-commons-go/configlib"
	configlibconfig "github.com/Bureau-Inc/bureau-commons-go/configlib/config"
	"github.com/Bureau-Inc/bureau-commons-go/metricx"
	"github.com/Bureau-Inc/bureau-commons-go/mongoclient"
	"github.com/Bureau-Inc/bureau-commons-go/telemetry"
	telemetryconfig "github.com/Bureau-Inc/bureau-commons-go/telemetry/config"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"github.com/bureau/onboarding-service/internal/config"
	"github.com/bureau/onboarding-service/internal/controller"
	"github.com/bureau/onboarding-service/internal/repo"
)

// mongoConnectTimeout bounds the startup connect + index creation.
const mongoConnectTimeout = 15 * time.Second

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
	// Close tears down peripherals in reverse order of wiring (currently the
	// Mongo client and telemetry); each teardown is added as the corresponding
	// peripheral is wired in Wire.
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
	// The same registry is handed to mongoclient so its pool/op metrics surface too.
	registry := metricx.NewRegistry()

	// mongo (datastore) — connect from the mongoclient section of config.yml,
	// then create the collection indexes described in the LLD. Fails fast if
	// MongoDB is unreachable at boot (mongoclient pings on connect).
	mongoCtx, cancel := context.WithTimeout(context.Background(), mongoConnectTimeout)
	defer cancel()
	mongoCli, err := mongoclient.GetOrCreateBureauMongoClientFromYAML(mongoCtx, configPath, registry)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to mongo: %w", err)
	}
	if err := repo.EnsureIndexes(mongoCtx, mongoCli); err != nil {
		return nil, fmt.Errorf("failed to ensure mongo indexes: %w", err)
	}

	// configlib (Apollo) — load verticals + questions into a per-instance cache
	// that hot-reloads. With an empty MetaAddr this resolves from the seeded
	// defaults (no Apollo server); set APOLLO_META to enable live hot-reload.
	apolloClient, err := configlib.New(&configlibconfig.Options{
		Enabled:    cfg.Apollo.Enabled,
		AppID:      cfg.Apollo.AppID,
		Cluster:    cfg.Apollo.Cluster,
		Namespaces: []string{cfg.Apollo.Namespace},
		MetaAddr:   cfg.Apollo.MetaAddr,
		MustStart:  false,
		Defaults:   config.SeedDefaults(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init configlib: %w", err)
	}
	verticalCache, err := config.LoadVerticalCache(apolloClient)
	if err != nil {
		return nil, fmt.Errorf("failed to load vertical cache: %w", err)
	}

	// controllers — readiness requires Mongo reachable AND a non-empty cache.
	healthCtrl := controller.NewHealthController(
		controller.ReadinessCheck{Name: "mongo", Probe: mongoCli.Ping},
		controller.ReadinessCheck{Name: "verticals", Probe: func(context.Context) error {
			if verticalCache.Len() == 0 {
				return errors.New("vertical cache empty")
			}
			return nil
		}},
	)
	verticalsCtrl := controller.NewVerticalsController(verticalCache)

	// router: trace + count every request, then register routes.
	r := gin.Default()
	r.Use(otelgin.Middleware(cfg.Telemetry.ServiceName))
	r.Use(controller.MetricsMiddleware(registry))
	healthCtrl.RegisterRoutes(r)
	verticalsCtrl.RegisterRoutes(r)
	r.GET("/metrics", gin.WrapH(metricx.NewHandler(registry, &metricx.Options{})))

	return &Container{
		Router: r,
		Cfg:    cfg,
		Close: func() error {
			closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			apolloClient.Close()
			mongoErr := mongoCli.Close(closeCtx)
			telErr := telemetry.Shutdown()
			if mongoErr != nil {
				return mongoErr
			}
			return telErr
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
