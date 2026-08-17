package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nightshift/control/internal/access"
	rcadapter "nightshift/control/internal/adapter/rc"
	"nightshift/control/internal/agent"
	"nightshift/control/internal/focus"
	"nightshift/control/internal/machine"
	platformconfig "nightshift/control/internal/platform/config"
	platformlogging "nightshift/control/internal/platform/logging"
	"nightshift/control/internal/session"
)

var runtimeConfig = platformconfig.Load()

// application is the control-plane composition root. It owns process lifecycle
// and wires capability handlers to infrastructure adapters.
type application struct {
	config  platformconfig.Config
	logger  *slog.Logger
	access  *access.Verifier
	session *session.Handler
	focus   *focus.Handler
	machine *machine.Handler
	server  *http.Server
	workers []backgroundWorker
}

type backgroundWorker struct {
	name  string
	start func()
}

func newApplication(cfg platformconfig.Config) (*application, error) {
	logger, err := platformlogging.New(os.Stdout, platformlogging.Config{
		Level:     cfg.LogLevel,
		Format:    cfg.LogFormat,
		AddSource: cfg.LogAddSource,
	})
	if err != nil {
		return nil, err
	}
	logger = logger.With("service", "nightshift-control")
	slog.SetDefault(logger)
	accessVerifier := access.New(access.Config{
		TeamDomain:   cfg.AccessTeamDomain,
		Audience:     cfg.AccessAudience,
		AllowedEmail: cfg.AllowedEmails,
		TrustHeader:  cfg.TrustAccessHeader,
		DevMode:      cfg.DevMode,
		DevEmail:     devEmail,
	})
	rcClient := rcadapter.NewClient(cfg.RCWrapper, func(_ context.Context, name string, args ...string) (string, error) {
		return execCommand(name, args...)
	})
	sessionHandler := session.NewHandler(session.NewService(
		agent.NewRegistry(cfg.Agents),
		rcClient,
		logger.With("component", "session"),
	))
	focusHandler := focus.NewHandler(focus.NewStore(cfg.Home, logger.With("component", "focus")))
	machineReader := machine.Reader(machine.Read)
	if cfg.DevMode {
		machineReader = devVitals
	}
	machineHandler := machine.NewHandler(machine.NewService(machineReader))
	return &application{
		config:  cfg,
		logger:  logger,
		access:  accessVerifier,
		session: sessionHandler,
		focus:   focusHandler,
		machine: machineHandler,
		server: &http.Server{
			Addr:              cfg.Listen,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       90 * time.Second,
			Handler: newRouter(logger, accessVerifier, routeHandlers{
				sessions: sessionHandler,
				focus:    focusHandler,
				machine:  machineHandler,
			}),
		},
		workers: []backgroundWorker{
			{name: "observability_ingester", start: startIngester},
			{name: "scheduler", start: startScheduler},
			{name: "flow_janitor", start: startFlowJanitor},
			{name: "report_reaper", start: startReportReaper},
			{name: "auth_checker", start: startAuthChecker},
			{name: "ticket_janitor", start: startJanitor},
		},
	}, nil
}

func (a *application) run(ctx context.Context) error {
	if err := a.access.Warm(ctx); err != nil {
		a.logger.Warn("access.jwks_initial_fetch_failed", "error", err)
	}
	if a.config.DevMode {
		installDevMode() // stub rc + seed data; goroutines below stay OFF
	} else {
		for _, worker := range a.workers {
			worker.start()
			a.logger.Info("worker.started", "worker", worker.name)
		}
	}

	a.logger.Info("control.started",
		"listen", a.config.Listen,
		"agents", a.config.Agents,
		"auth_mode", a.access.Mode(),
	)
	serverError := make(chan error, 1)
	go func() { serverError <- a.server.ListenAndServe() }()
	select {
	case err := <-serverError:
		return err
	case <-ctx.Done():
		a.logger.Info("control.shutdown_started", "reason", ctx.Err())
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.server.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shutdown http server: %w", err)
		}
		a.logger.Info("control.shutdown_completed")
		return nil
	}
}

// Run constructs and serves the control-plane application.
func Run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := newApplication(runtimeConfig)
	if err != nil {
		return fmt.Errorf("configure logging: %w", err)
	}
	if err := app.run(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		app.logger.Error("control.stopped", "error", err)
		return err
	}
	return nil
}
