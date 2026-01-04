package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"sitelert/internal/config"
	"sitelert/internal/metrics"
	"sitelert/internal/scheduler"
	"sitelert/internal/server"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

type options struct {
	configPath string
	listen     string
	logLevel   string
}

func Execute() {
	opts := &options{}

	rootCmd := &cobra.Command{
		Use:   "sitelert",
		Short: "Uptime monitor daemon",
		RunE: func(cmd *cobra.Command, args []string) error {

			rand.Seed(time.Now().UnixNano())

			logger, err := newLogger(opts.logLevel)
			if err != nil {
				return err
			}

			cfg, err := config.LoadAndValidateConfig(opts.configPath)
			if err != nil {
				return err
			}

			bind := cfg.Global.ScrapeBind
			if opts.listen != "" {
				bind = opts.listen
			}

			bundle := metrics.NewBundle()
			bundle.Metrics.InitServices(cfg.Services)

			srv := server.NewServer(bind, logger, bundle.Registry)

			sched, err := scheduler.NewScheduler(*cfg, logger, bundle.Metrics)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			errCh := make(chan error, 1)

			go func() {
				logger.Info("starting server", "addr", bind)
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, server.ErrServerClosed) {
					errCh <- fmt.Errorf("server failed: %w", err)
					return
				}
				errCh <- nil
			}()

			go func() {
				logger.Info("starting scheduler", "workers", cfg.Global.WorkerCount, "jitter", cfg.Global.Jitter)
				if err := sched.Start(ctx, cfg.Services); err != nil {
					errCh <- fmt.Errorf("scheduler failed: %w", err)
					return
				}
				errCh <- nil
			}()

			select {
			case <-ctx.Done():
				logger.Info("shutdown requested", "reason", ctx.Err())
			case err := <-errCh:
				if err != nil {
					return err
				}
			}

			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("server shutdown failed: %w", err)
			}
			logger.Info("server shut down gracefully")
			return nil
		},
	}

	rootCmd.PersistentFlags().StringVarP(&opts.configPath, "config", "c", "./config.yml", "Path to configuration file")
	rootCmd.PersistentFlags().StringVarP(&opts.listen, "listen", "l", "", "Override bind address for /healthz and /metrics endpoints")
	rootCmd.PersistentFlags().StringVar(&opts.logLevel, "log-level", "info", "Log level (debug, info, warn, error)")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
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
