package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

func TestIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make api-test-integration with the development stack running and migrated.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	cfg := loadConfig()
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		t.Fatal("could not configure the test database connection")
	}
	defer pool.Close()
	if err := ensureDemoSeed(ctx, pool); err != nil {
		t.Fatalf("load the demo seed: %v", err)
	}

	provider, err := discoverProvider(ctx, cfg.issuer)
	if err != nil {
		t.Fatal("could not discover the development sign-in server")
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	jobs, err := newJobClient(pool, logger, "")
	if err != nil {
		t.Fatal(err)
	}
	completed, unsubscribe := jobs.Subscribe(river.EventKindJobCompleted)
	defer unsubscribe()
	if err := jobs.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := jobs.Stop(stopCtx); err != nil {
			t.Error(err)
		}
	}()

	server := httptest.NewServer(newHandler(pool, newAuthenticator(provider, cfg.issuer, cfg.audience), logger, devOrigins, t.TempDir()))
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path[1:], func(t *testing.T) {
			res, err := client.Get(server.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("got HTTP %d, want 200", res.StatusCode)
			}
		})
	}

	const meURL = "/api/v1/me"
	t.Run("anonymous", func(t *testing.T) {
		res, err := client.Get(server.URL + meURL)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized || res.Header.Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("got HTTP %d with challenge %q, want 401 with a bearer challenge", res.StatusCode, res.Header.Get("WWW-Authenticate"))
		}
	})

	for _, tc := range []struct {
		username  string
		subSuffix string
		role      string
		unit      unit
		beatCount int64
	}{
		{"state.admin", "a1", "state_admin", unit{"DS", "Demo State", "state", "ds"}, 24},
		{"division.officer", "a2", "division_officer", unit{"D1", "Hill Division", "division", "ds.c1.d1"}, 6},
		{"beat.guard", "a3", "field_staff", unit{"B01", "Beat 01", "beat", "ds.c1.d1.r1.b01"}, 1},
		{username: "no.role", subSuffix: "a4"},
	} {
		t.Run(tc.username, func(t *testing.T) {
			token := demoToken(t, client, provider.Endpoint().TokenURL, tc.username)
			r, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+meURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer "+token)
			res, err := client.Do(r)
			if err != nil {
				t.Fatal("could not call the API")
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("got HTTP %d, want 200", res.StatusCode)
			}
			var me meResponse
			if err := json.NewDecoder(res.Body).Decode(&me); err != nil {
				t.Fatal(err)
			}
			if me.Subject != "d0000000-0000-4000-8000-0000000000"+tc.subSuffix || me.Username != tc.username {
				t.Fatalf("got subject %q and username %q for %s", me.Subject, me.Username, tc.username)
			}
			if tc.role == "" {
				if me.RoleAssignments == nil || len(me.RoleAssignments) != 0 {
					t.Fatalf("want an empty list of roles, got %+v", me.RoleAssignments)
				}
				var username string
				if err := pool.QueryRow(ctx, "SELECT username FROM users WHERE oidc_subject = $1", me.Subject).Scan(&username); err != nil {
					t.Fatal(err)
				}
				if username != tc.username {
					t.Fatalf("stored username is %q, want %q", username, tc.username)
				}
				return
			}
			want := roleAssignment{
				Workspace: workspace{Slug: "demo", Name: "Demo State"},
				Hierarchy: "territorial", Role: tc.role, Unit: tc.unit, BeatCount: tc.beatCount,
			}
			if len(me.RoleAssignments) != 1 || me.RoleAssignments[0] != want {
				t.Fatalf("got %+v, want [%+v]", me.RoleAssignments, want)
			}
			t.Logf("%s beat count = %d", want.Unit.Code, want.BeatCount)
		})
	}

	t.Run("periodic heartbeat", func(t *testing.T) {
		// Other periodic jobs, such as the feed health check, also complete.
		for {
			select {
			case event := <-completed:
				if event == nil {
					t.Fatal("the queue stopped before it completed a heartbeat")
				}
				if event.Job.Kind == (heartbeatArgs{}).Kind() {
					return
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for the queue to complete a heartbeat")
			}
		}
	})
}

func demoToken(t *testing.T, client *http.Client, tokenURL, username string) string {
	t.Helper()
	res, err := client.PostForm(tokenURL, url.Values{
		"grant_type": {"password"},
		"client_id":  {"open-fimra-dev-cli"},
		"username":   {username},
		"password":   {"demo"},
	})
	if err != nil {
		t.Fatal("could not reach the demo token endpoint")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("demo sign-in returned HTTP %d", res.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&token); err != nil || token.AccessToken == "" {
		t.Fatal("demo sign-in did not return an access token")
	}
	return token.AccessToken
}
