package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestDatabaseIntegration checks that every connection the API opens has
// just-in-time compilation turned off.
func TestDatabaseIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make api-test-integration with the development stack running and migrated.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := openPool(ctx, loadConfig().databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var jit string
	if err := pool.QueryRow(ctx, "SHOW jit").Scan(&jit); err != nil {
		t.Fatal(err)
	}
	if jit != "off" {
		t.Fatalf("jit is %q, want off", jit)
	}
}
