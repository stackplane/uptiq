package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"sitelert/internal/alerting"
	"sitelert/internal/config"
	"sitelert/internal/metrics"
	"sitelert/internal/scheduler"
	"sitelert/internal/server"
)

type options struct {
	configPath string
	listen     string
	logLevel   string
	watch      bool
}

func Execute() {
	opts := options{}

	rootCmd := &cobra.Command{
		Use:   "sitelert",
		Short: "Uptime monitor daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			rand.Seed(time.Now().UnixNano())

			logger, err := newLogger(opts.logLevel)
			if err != nil {
				return err
			}

			// Load initial config
			cfg, err := config.LoadAndValidateConfig(opts.configPath)
			if err != nil {
				return err
			}

			bind := cfg.Global.ScrapeBind
			if opts.listen != "" {
				bind = opts.listen
			}

			// Metrics bundle
			bundle := metrics.NewBundle()
			bundle.Metrics.EnsureServices(cfg.Services)
			bundle.Metrics.ConfigReloadSuccess.Set(1)

			// Alert engine (preserves state by service_id)
			alertEngine := alerting.NewEngine(cfg.Alerting, logger)

			// Server
			srv := server.NewServer(bind, logger, bundle.Registry)

			// Scheduler (reloadable)
			sched, err := scheduler.New(cfg, logger, bundle.Metrics, alertEngine)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			// Separate SIGHUP channel (do NOT cancel context)
			hupCh := make(chan os.Signal, 1)
			signal.Notify(hupCh, syscall.SIGHUP)
			defer signal.Stop(hupCh)

			// Reload trigger channel (HUP + watcher)
			reloadCh := make(chan struct{}, 1)
			triggerReload := func() {
				select {
				case reloadCh <- struct{}{}:
				default:
				}
			}

			if opts.watch {
				watcher, err := startConfigWatcher(logger, opts.configPath, triggerReload)
				if err != nil {
					return err
				}
				defer watcher.Close()
				logger.Info("config watch enabled", "path", opts.configPath)
			}

			errCh := make(chan error, 2)

			// Server goroutine
			go func() {
				logger.Info("starting server", "addr", bind)
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, server.ErrServerClosed) {
					errCh <- fmt.Errorf("server failed: %w", err)
					return
				}
				errCh <- nil
			}()

			// Scheduler goroutine
			go func() {
				logger.Info("starting scheduler", "workers", cfg.Global.WorkerCount, "jitter", cfg.Global.Jitter)
				if err := sched.Start(ctx, cfg.Services); err != nil {
					errCh <- fmt.Errorf("scheduler failed: %w", err)
					return
				}
				errCh <- nil
			}()

			// Reload apply function
			applyReload := func() {
				newCfg, err := config.LoadAndValidateConfig(opts.configPath)
				if err != nil {
					// IMPORTANT: do not replace running config
					bundle.Metrics.ConfigReloadSuccess.Set(0)
					logger.Warn("config reload failed; keeping current config",
						"path", opts.configPath,
						"error", err.Error(),
					)
					return
				}

				// Successful reload
				bundle.Metrics.ConfigReloadSuccess.Set(1)

				// Ensure metric series for new services
				bundle.Metrics.EnsureServices(newCfg.Services)

				// Update scheduler schedules (no restart)
				sched.UpdateServices(newCfg.Services)

				// Note: global settings changes (worker_count/jitter/bind) currently require restart.
				logger.Info("config reload applied",
					"path", opts.configPath,
					"services", len(newCfg.Services),
				)
			}

			// Main loop: shutdown, fatal component error, or reload triggers
			for {
				select {
				case <-ctx.Done():
					logger.Info("shutdown requested", "reason", ctx.Err())
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					_ = srv.Shutdown(shutdownCtx)
					logger.Info("shutdown complete")
					return nil

				case sig := <-hupCh:
					logger.Info("received signal", "signal", sig.String(), "action", "reload")
					applyReload()

				case <-reloadCh:
					logger.Info("config file change detected", "action", "reload")
					applyReload()

				case err := <-errCh:
					if err != nil {
						return err
					}
					// If one component exits cleanly, continue unless ctx cancelled.
				}
			}
		},
	}

	rootCmd.PersistentFlags().StringVarP(&opts.configPath, "config", "c", "configs/example.yaml", "Path to YAML config file")
	rootCmd.PersistentFlags().StringVarP(&opts.listen, "listen", "l", "", "Override bind address for /healthz and /metrics (e.g. 0.0.0.0:8080)")
	rootCmd.PersistentFlags().StringVar(&opts.logLevel, "log-level", "info", "Log level: debug|info|warn|error")
	rootCmd.PersistentFlags().BoolVar(&opts.watch, "watch", false, "Watch config file and reload on changes (fsnotify)")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func startConfigWatcher(log *slog.Logger, configPath string, triggerReload func()) (*fsnotify.Watcher, error) {
	if log == nil {
		log = slog.Default()
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}

	abs, err := filepath.Abs(configPath)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("abs path: %w", err)
	}
	dir := filepath.Dir(abs)
	base := filepath.Base(abs)

	// Watch the directory so atomic-save (rename) patterns still trigger.
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("watch dir %q: %w", dir, err)
	}

	// Debounce reload triggers (many editors emit multiple events)
	const debounce = 250 * time.Millisecond
	var (
		timer   *time.Timer
		pending bool
	)
	reset := func() {
		if timer == nil {
			timer = time.NewTimer(debounce)
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(debounce)
	}

	go func() {
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()

		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != base {
					continue
				}

				// Common events: Write, Create, Rename, Chmod
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
					pending = true
					reset()
				}

			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Warn("fsnotify error", "error", err.Error())

			case <-func() <-chan time.Time {
				if timer == nil {
					// never fires
					ch := make(chan time.Time)
					return ch
				}
				return timer.C
			}():
				if pending {
					pending = false
					triggerReload()
				}
			}
		}
	}()

	return w, nil
}

func newLogger(level string) (*slog.Logger, error) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, fmt.Errorf("invalid log level: %q", level)
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler), nil
}
