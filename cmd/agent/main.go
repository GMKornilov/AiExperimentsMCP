package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aichallenge_scheduled/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	configPath := flag.String("config", "config.yaml", "YAML configuration")
	envFile := flag.String("env-file", "", "optional environment file")
	flag.Parse()
	cfg, err := app.LoadConfig(*configPath, *envFile)
	if err != nil {
		logger.Error("load_config_failed", "error", err)
		os.Exit(1)
	}
	serverNames := make([]string, 0, len(cfg.MCPServers))
	for _, server := range cfg.MCPServers {
		serverNames = append(serverNames, server.Name)
	}
	logger.Info("agent_config", "config_path", *configPath, "provider", cfg.LLM.Provider, "model", cfg.LLM.Model, "mcp_servers", serverNames)
	if cfg.LLM.Provider == "demo" {
		logger.Warn("demo_provider_active", "message", "Demo mode uses echo-style responses and does not call an LLM")
	}
	svc, err := app.NewService(cfg, app.WithLogger(logger))
	if err != nil {
		logger.Error("service_start_failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	svc.Start(ctx)
	server := &http.Server{Addr: cfg.Listen, Handler: svc.Handler(), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	logger.Info("agent_listening", "listen", cfg.Listen)
	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-done:
		if err != nil && err != http.ErrServerClosed {
			serveErr = err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = server.Shutdown(shutdownCtx)
	cancel()
	stop()
	if err := svc.Close(); err != nil {
		logger.Error("database_close_failed", "error", err)
	}
	if serveErr != nil {
		logger.Error("server_failed", "error", serveErr)
		os.Exit(1)
	}
}
