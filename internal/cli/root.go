package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"sitelert/internal/alerting"
	"sitelert/internal/config"
	"sitelert/internal/metrics"
	"sitelert/internal/scheduler"
	"sitelert/internal/server"
)

// Execute runs the CLI.
func Execute() {
	opts := DefaultOptions()

	cmd := &cobra.Command{
		Use:   "sitelert",
		Short: "Uptime monitor daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(opts)
		},
	}

	flags := cmd.PersistentFlags()
	flags.StringVarP(&opts.ConfigPath, "config", "c", opts.ConfigPath, "Path to YAML config file")
	flags.StringVarP(&opts.Listen, "listen", "l", "", "Override bind address for /healthz and /metrics (e.g. 0.0.0.0:8080)")
	flags.StringVar(&opts.LogLevel, "log-level", opts.LogLevel, "Log level: debug|info|warn|error")
	flags.BoolVar(&opts.Watch, "watch", false, "Watch config file and reload on changes")

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func run(opts Options) error {
	rand.Seed(time.Now().UnixNano())

	log, err := NewLogger(opts.LogLevel)
	if err != nil {
		return err
	}

	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return err
	}

	app := &application{
		log:  log,
		opts: opts,
		cfg:  cfg,
	}

	return app.run()
}

type application struct {
	log  *slog.Logger
	opts Options
	cfg  *config.Config

	metrics     *metrics.Bundle
	alertEngine *alerting.Engine
	server      *server.Server
	scheduler   *scheduler.Scheduler
	watcher     *ConfigWatcher
}

func (a *application) run() error {
	// Initialize components
	a.metrics = metrics.NewBundle()
	a.metrics.Collector.EnsureServices(a.cfg.Services)
	a.metrics.Collector.ConfigReloadSuccess.Set(1)

	a.alertEngine = alerting.NewEngine(a.cfg.Alerting, a.log)

	bind := a.cfg.Global.ScrapeBind
	if a.opts.Listen != "" {
		bind = a.opts.Listen
	}
	a.server = server.New(bind, a.log, a.metrics.Registry)

	sched, err := scheduler.New(a.cfg, a.log, a.metrics.Collector, a.alertEngine)
	if err != nil {
		return err
	}
	a.scheduler = sched

	// Setup context and signals
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Separate SIGHUP channel for reloads
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	defer signal.Stop(hupCh)

	// Reload trigger channel
	reloadCh := make(chan struct{}, 1)
	triggerReload := func() {
		select {
		case reloadCh <- struct{}{}:
		default:
		}
	}

	// Start config watcher if enabled
	if a.opts.Watch {
		watcher, err := NewConfigWatcher(a.log, a.opts.ConfigPath, triggerReload)
		if err != nil {
			return err
		}
		a.watcher = watcher
		defer a.watcher.Close()
		a.log.Info("config watch enabled", "path", a.opts.ConfigPath)
	}

	// Start components
	errCh := make(chan error, 2)

	go func() {
		a.log.Info("starting server", "addr", bind)
		if err := a.server.ListenAndServe(); err != nil && !errors.Is(err, server.ErrServerClosed) {
			errCh <- fmt.Errorf("server failed: %w", err)
			return
		}
		errCh <- nil
	}()

	go func() {
		a.log.Info("starting scheduler",
			"workers", a.cfg.Global.WorkerCount,
			"jitter", a.cfg.Global.Jitter,
		)
		if err := a.scheduler.Start(ctx, a.cfg.Services); err != nil {
			errCh <- fmt.Errorf("scheduler failed: %w", err)
			return
		}
		errCh <- nil
	}()

	// Main event loop
	return a.eventLoop(ctx, hupCh, reloadCh, errCh)
}

func (a *application) eventLoop(ctx context.Context, hupCh <-chan os.Signal, reloadCh <-chan struct{}, errCh <-chan error) error {
	for {
		select {
		case <-ctx.Done():
			return a.shutdown()

		case sig := <-hupCh:
			a.log.Info("received signal", "signal", sig.String(), "action", "reload")
			a.applyReload()

		case <-reloadCh:
			a.log.Info("config file change detected", "action", "reload")
			a.applyReload()

		case err := <-errCh:
			if err != nil {
				return err
			}
			// Component exited cleanly; continue unless context cancelled
		}
	}
}

func (a *application) applyReload() {
	newCfg, err := config.Load(a.opts.ConfigPath)
	if err != nil {
		a.metrics.Collector.ConfigReloadSuccess.Set(0)
		a.log.Warn("config reload failed; keeping current config",
			"path", a.opts.ConfigPath,
			"error", err.Error(),
		)
		return
	}

	a.metrics.Collector.ConfigReloadSuccess.Set(1)
	a.metrics.Collector.EnsureServices(newCfg.Services)
	a.scheduler.UpdateServices(newCfg.Services)

	// Note: global settings changes (worker_count/jitter/bind) require restart
	a.log.Info("config reload applied",
		"path", a.opts.ConfigPath,
		"services", len(newCfg.Services),
	)
}

func (a *application) shutdown() error {
	a.log.Info("shutdown requested")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.server.Shutdown(shutdownCtx); err != nil {
		a.log.Warn("server shutdown error", "error", err.Error())
	}

	a.log.Info("shutdown complete")
	return nil
}
