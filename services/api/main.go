package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	databaseURL string
	issuer      string
	audience    string
	httpAddr    string
	firmsMapKey string
	corsOrigins string
	uploadDir   string
	exportDir   string
	exportTTL   time.Duration
}

func loadConfig() config {
	return config{
		databaseURL: envOr("FIMRA_DATABASE_URL", "postgres://fimra:fimra-dev@127.0.0.1:15432/fimra?sslmode=disable"),
		issuer:      envOr("FIMRA_OIDC_ISSUER", "http://127.0.0.1:18180/realms/open-fimra"),
		audience:    envOr("FIMRA_OIDC_AUDIENCE", "open-fimra-api"),
		httpAddr:    envOr("FIMRA_HTTP_ADDR", "127.0.0.1:18090"),
		firmsMapKey: strings.TrimSpace(os.Getenv("FIMRA_FIRMS_MAP_KEY")),
		corsOrigins: envOr("FIMRA_CORS_ORIGINS", devOrigins),
		uploadDir:   envOr("FIMRA_UPLOAD_DIR", ".geo-uploads"),
		exportDir:   envOr("FIMRA_EXPORT_DIR", ".geo-exports"),
		exportTTL:   defaultExportTTL,
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := loadConfig()
	var err error
	if raw := os.Getenv("FIMRA_EXPORT_TTL"); raw != "" {
		cfg.exportTTL, err = time.ParseDuration(raw)
		if err != nil || cfg.exportTTL < time.Minute {
			logger.Error("FIMRA_EXPORT_TTL must be a duration of at least one minute, such as 24h", "value", raw, "error", err)
			os.Exit(1)
		}
	}
	if len(os.Args) > 1 {
		err = runCommand(ctx, cfg, os.Args[1:])
	} else {
		err = run(ctx, cfg, logger)
	}
	if err != nil {
		logger.Error("Open FIMRA stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	startup, cancelStartup := context.WithTimeout(ctx, 10*time.Second)
	defer cancelStartup()

	pool, err := pgxpool.New(startup, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("could not configure the database connection: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(startup); err != nil {
		return fmt.Errorf("could not connect to the database: %w", err)
	}

	provider, err := discoverProvider(startup, cfg.issuer)
	if err != nil {
		return fmt.Errorf("could not discover the OpenID Connect provider at %s: %w", cfg.issuer, err)
	}

	listener, err := new(net.ListenConfig).Listen(startup, "tcp", cfg.httpAddr)
	if err != nil {
		return fmt.Errorf("could not listen on %s: %w", cfg.httpAddr, err)
	}
	defer listener.Close()

	jobs, err := newJobClient(pool, logger, cfg.firmsMapKey)
	if err != nil {
		return fmt.Errorf("could not configure the job queue: %w", err)
	}
	if err := jobs.Start(ctx); err != nil {
		return fmt.Errorf("could not start the job queue: %w", err)
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := jobs.Stop(shutdown); err != nil {
			logger.Error("Could not stop the job queue before the deadline", "error", err)
		}
	}()

	go runExportCleanup(ctx, pool, cfg.exportDir, logger)

	if cfg.firmsMapKey == "" {
		logger.Info("FIRMS polling is disabled because FIMRA_FIRMS_MAP_KEY is empty")
	}

	server := &http.Server{
		Handler: newHandler(pool, newAuthenticator(provider, cfg.issuer, cfg.audience), logger, cfg.corsOrigins, cfg.uploadDir, jobs,
			withExports(cfg.exportDir, cfg.exportTTL)),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	logger.Info("API listening", "address", listener.Addr().String())

	select {
	case err := <-served:
		return fmt.Errorf("could not serve HTTP requests: %w", err)
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return fmt.Errorf("could not shut down HTTP before the deadline: %w", err)
		}
		return nil
	}
}
