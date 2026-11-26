package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDemoReplayIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make demo-replay-test.")
	}
	ctx, pool := notificationTestDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())

	suffix, slug, prefix := loadSeedCopy(t, ctx, tx)
	_, _, overlapping := loadSeedCopy(t, ctx, tx)
	stateCode := "XS-" + strings.ToUpper(suffix)
	cipher, err := newSettingsCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := newLocalAttachments(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	day, err := replayOperations(ctx, tx, slug, prefix, stateCode, store, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.DateOnly, day); err != nil {
		t.Fatalf("reporting day: %q: %v", day, err)
	}
	if day, err := replayOperations(ctx, tx, slug, prefix, stateCode, store, cipher); err != nil || day != "" {
		t.Fatalf("running the replay again must be a no-op, got %q: %v", day, err)
	}
	if err := checkOperationsReplay(ctx, tx, prefix+"01", day, stateCode); err != nil {
		t.Fatal(err)
	}
	var leaked int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM detection_matches WHERE workspace_id=$1", overlapping+"01").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("replayed observations reached the overlapping workspace: %d matches, %v", leaked, err)
	}

	var hash string
	if err := tx.QueryRow(ctx, "SELECT sha256 FROM attachments WHERE workspace_id=$1 AND file_name='demo-ridge.png'", prefix+"01").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	file, err := store.Open(ctx, attachmentKeyFor(hash))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	saved, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := http.DetectContentType(saved); got != "image/png" {
		t.Fatalf("stored evidence type: %s", got)
	}
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
}
