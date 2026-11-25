package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestWizardWalkthroughServer serves the real API for a browser walkthrough of
// the wizard on an empty database. FIRMS is the fake server, which accepts
// only testMapKey, and a poll runs every few seconds so the alert map fills
// once the key is saved. Run it with make wizard-walkthrough.
func TestWizardWalkthroughServer(t *testing.T) {
	if os.Getenv("FIMRA_WALKTHROUGH") != "1" {
		t.Skip("Run make wizard-walkthrough.")
	}
	seconds, _ := strconv.Atoi(envOr("FIMRA_WALKTHROUGH_SECONDS", "900"))
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(seconds)*time.Second)
	defer cancel()
	cfg := loadConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if open, err := installOpen(ctx, pool); err != nil || !open {
		t.Fatalf("the walkthrough needs an empty, migrated database (open %v, error %v)", open, err)
	}
	secrets, err := newSettingsCipher(randomSettingsKey(t), "")
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeFIRMS(t, testMapKey)
	keys := newFIRMSKeys(pool, secrets, "", fake.transport())
	setup, err := openSetup(ctx, pool, cfg.setupCode, logger)
	if err != nil || setup == nil {
		t.Fatalf("set FIMRA_SETUP_CODE: %v", err)
	}
	smtp, err := readSMTPConfig()
	if err != nil {
		t.Fatal(err)
	}
	var email notificationChannel
	if smtp != nil {
		email = smtp
	}
	jobs, err := newJobClient(pool, logger, nil, email, pushSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = jobs.Stop(context.Background()) }()
	go func() {
		for ctx.Err() == nil {
			if err := pollFIRMS(ctx, pool, keys.workspaceFetch, time.Now()); err != nil && ctx.Err() == nil {
				logger.Warn("Walkthrough poll failed", "error", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}()

	provider, err := discoverProvider(ctx, cfg.issuer)
	if err != nil {
		t.Fatal(err)
	}
	auth := newAuthenticator(provider, cfg.issuer, cfg.audience)
	auth.emailEnabled = email != nil
	listener, err := net.Listen("tcp", cfg.httpAddr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: newHandler(pool, auth, logger, cfg.corsOrigins, cfg.uploadDir, jobs,
		withSetup(setup, secrets, keys)), ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = server.Close() }()
	logger.Info("Walkthrough API listening", "address", listener.Addr().String())
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}
