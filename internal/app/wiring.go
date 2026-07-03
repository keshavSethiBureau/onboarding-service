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
	"path/filepath"
	"syscall"
	"time"

	"github.com/Bureau-Inc/bureau-commons-go/configlib"
	configlibconfig "github.com/Bureau-Inc/bureau-commons-go/configlib/config"
	"github.com/Bureau-Inc/bureau-commons-go/metricx"
	"github.com/Bureau-Inc/bureau-commons-go/mongoclient"
	"github.com/Bureau-Inc/bureau-commons-go/telemetry"
	telemetryconfig "github.com/Bureau-Inc/bureau-commons-go/telemetry/config"
	temporalclient "github.com/Bureau-Inc/bureau-commons-go/temporalclient/client"
	temporalconfig "github.com/Bureau-Inc/bureau-commons-go/temporalclient/config"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"onboarding-service/internal/auth"
	"onboarding-service/internal/config"
	"onboarding-service/internal/controller"
	"onboarding-service/internal/platform/auth0"
	"onboarding-service/internal/platform/provisioning"
	"onboarding-service/internal/repo"
	mongorepo "onboarding-service/internal/repo/mongo"
	"onboarding-service/internal/service/impl"
	"onboarding-service/internal/workflow"
)

// mongoConnectTimeout bounds the startup connect + index creation.
const mongoConnectTimeout = 15 * time.Second

// defaultEnv selects configs/local.yml when APP_ENV is unset.
const defaultEnv = "local"

// resolveConfigPath returns the config file to load. CONFIG_FILE wins if set;
// otherwise it is configs/<APP_ENV>.yml (APP_ENV defaults to "local").
func resolveConfigPath() string {
	if cf := os.Getenv("CONFIG_FILE"); cf != "" {
		return cf
	}
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = defaultEnv
	}
	return filepath.Join("configs", env+".yml")
}

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
	// config (boot/infra) — configs/<APP_ENV>.yml (default local), or CONFIG_FILE.
	configPath := resolveConfigPath()
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

	// repositories (DAO layer)
	journeyRepo := mongorepo.NewOnboardingJourneyRepo(mongoCli)
	provisioningRepo := mongorepo.NewProvisioningRecordRepo(mongoCli)

	// external integration ports — always the real clients (no stub fallback).
	// They construct fine with empty config; only the actual Auth0/Svix/Lago/
	// Kong/AWS calls fail until real config is provided (org-creation + complete
	// flows). Non-external endpoints run regardless.
	orgCreator, err := auth0.NewHTTPOrgCreator(auth0.Auth0Settings{
		Domain:                       cfg.Auth.Management.Domain,
		ClientID:                     cfg.Auth.Management.ClientID,
		ClientSecret:                 cfg.Auth.Management.ClientSecret,
		Audience:                     cfg.Auth.Management.Audience,
		UsernamePasswordConnectionID: cfg.Auth.Management.UsernamePasswordConnectionID,
		SSOConnectionID:              cfg.Auth.Management.SSOConnectionID,
		OwnerRoleID:                  cfg.Auth.Management.OwnerRoleID,
	}, registry)
	if err != nil {
		return nil, fmt.Errorf("failed to init auth0 org creator: %w", err)
	}
	provisioner, err := provisioning.NewHTTPProvisioner(mongoCtx, provisioning.Settings{
		SvixBaseURL: cfg.Provisioning.Svix.BaseURL, SvixToken: cfg.Provisioning.Svix.Token,
		LagoBaseURL: cfg.Provisioning.Lago.BaseURL, LagoToken: cfg.Provisioning.Lago.Token,
		KongBaseURL:    cfg.Provisioning.Kong.BaseURL,
		AWSRegion:      cfg.Provisioning.AWS.Region,
		AWSUsagePlanID: cfg.Provisioning.AWS.UsagePlanID,
	}, registry)
	if err != nil {
		return nil, fmt.Errorf("failed to init provisioner: %w", err)
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

	// temporal (orchestration) — client + a worker polling the onboarding task
	// queue, registering the OnboardingWorkflow and its activities. mongoCtx
	// (cancelled when Wire returns) bounds only the initial dial; the worker
	// runs independently afterwards.
	tcfg, err := temporalconfig.FromYAML(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load temporal config: %w", err)
	}
	tfactory, err := temporalclient.NewFactory(tcfg)
	if err != nil {
		return nil, fmt.Errorf("failed to init temporal factory: %w", err)
	}
	activities := workflow.NewActivities(journeyRepo, provisioningRepo, orgCreator, provisioner)
	temporalClients, temporalWorker, err := tfactory.StartWorker(mongoCtx, workflow.TaskQueue, func(r worker.Registry) {
		workflow.Register(r, activities)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start temporal worker: %w", err)
	}
	starter := workflow.NewStarter(temporalClients.Client, workflow.TaskQueue)

	// controllers — readiness requires Mongo, a non-empty cache, AND Temporal.
	healthCtrl := controller.NewHealthController(
		controller.ReadinessCheck{Name: "mongo", Probe: mongoCli.Ping},
		controller.ReadinessCheck{Name: "verticals", Probe: func(context.Context) error {
			if verticalCache.Len() == 0 {
				return errors.New("vertical cache empty")
			}
			return nil
		}},
		controller.ReadinessCheck{Name: "temporal", Probe: func(ctx context.Context) error {
			_, err := temporalClients.Client.CheckHealth(ctx, &sdkclient.CheckHealthRequest{})
			return err
		}},
	)
	verticalsCtrl := controller.NewVerticalsController(verticalCache)

	// auth (Auth0 JWT) — identity from the token; dev mode via headers when disabled.
	authMW, err := auth.New(auth.Config{
		Enabled:  cfg.Auth.Enabled,
		Issuer:   cfg.Auth.Issuer,
		Audience: cfg.Auth.Audience,
		JWKSURL:  cfg.Auth.JWKSURL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init auth: %w", err)
	}
	onboardingCtrl := controller.NewOnboardingController(impl.NewOnboardingService(journeyRepo), starter)

	// internal endpoint (Auth Service only) — starts/signals the workflow.
	internalCtrl := controller.NewInternalOnboardingController(starter)
	internalGuard := auth.InternalTokenMiddleware(cfg.Internal.AuthToken)

	// router: trace + count every request, then register routes.
	r := gin.Default()
	r.Use(otelgin.Middleware(cfg.Telemetry.ServiceName))
	r.Use(controller.MetricsMiddleware(registry))
	healthCtrl.RegisterRoutes(r)
	verticalsCtrl.RegisterRoutes(r)
	onboardingCtrl.RegisterRoutes(r, authMW.Handler())
	internalCtrl.RegisterRoutes(r, internalGuard)
	r.GET("/metrics", gin.WrapH(metricx.NewHandler(registry, &metricx.Options{})))

	return &Container{
		Router: r,
		Cfg:    cfg,
		Close: func() error {
			closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			temporalWorker.Stop()
			temporalClients.Close()
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
