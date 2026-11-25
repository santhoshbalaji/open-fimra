package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	databaseURL string
	issuer      string
	audience    string
	httpAddr    string
}

func loadConfig() config {
	return config{
		databaseURL: envOr("FIMRA_DATABASE_URL", "postgres://fimra:fimra-dev@127.0.0.1:15432/fimra?sslmode=disable"),
		issuer:      envOr("FIMRA_OIDC_ISSUER", "http://127.0.0.1:18180/realms/open-fimra"),
		audience:    envOr("FIMRA_OIDC_AUDIENCE", "open-fimra-api"),
		httpAddr:    envOr("FIMRA_HTTP_ADDR", "127.0.0.1:18090"),
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

	if err := run(ctx, loadConfig(), logger); err != nil {
		logger.Error("API stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	startup, cancelStartup := context.WithTimeout(ctx, 10*time.Second)
	defer cancelStartup()

	pool, err := pgxpool.New(startup, cfg.databaseURL)
	if err != nil {
		return errors.New("could not configure the database connection")
	}
	defer pool.Close()
	if err := pool.Ping(startup); err != nil {
		return errors.New("could not connect to the database")
	}

	provider, err := discoverProvider(startup, cfg.issuer)
	if err != nil {
		return errors.New("could not discover the OpenID Connect provider")
	}

	listener, err := new(net.ListenConfig).Listen(startup, "tcp", cfg.httpAddr)
	if err != nil {
		return errors.New("could not listen on the HTTP address")
	}
	defer listener.Close()

	jobs, err := newJobClient(pool, logger)
	if err != nil {
		return errors.New("could not configure the job queue")
	}
	if err := jobs.Start(ctx); err != nil {
		return errors.New("could not start the job queue")
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := jobs.Stop(shutdown); err != nil {
			logger.Error("Could not stop the job queue before the deadline")
		}
	}()

	server := &http.Server{
		Handler:           newHandler(pool, newAuthenticator(provider, cfg.issuer, cfg.audience), logger),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	logger.Info("API listening", "address", listener.Addr().String())

	select {
	case <-served:
		return errors.New("could not serve HTTP requests")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return errors.New("could not shut down HTTP before the deadline")
		}
		return nil
	}
}
