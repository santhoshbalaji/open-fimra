package main

import (
	"context"
	"crypto"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

func TestNotificationsIntegration(t *testing.T) {
	ctx, pool := notificationTestDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	suffix, slug, prefix := loadSeedCopy(t, ctx, tx)
	workspaceID := prefix + "01"
	if err := execSeed(ctx, tx, "reference.sql"); err != nil {
		t.Fatal(err)
	}
	// Add one patch of synthetic forest inside B01 to test field staff routing.
	_, err = tx.Exec(ctx, `
WITH beat AS (SELECT ST_PointOnSurface(geom) AS point FROM units WHERE hierarchy_id=$1 AND code='B01')
UPDATE layer_features SET geom=ST_Multi(ST_Union(geom,ST_Buffer(beat.point,0.002)))
FROM beat WHERE layer_version_id=$2 AND code='F01'`, prefix+"10", prefix+"31")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	observedAt := now.Add(-time.Hour)
	rows, err := tx.Query(ctx, `
SELECT id, ST_X(geom), ST_Y(geom) FROM reference_detections UNION ALL
SELECT 'B01', ST_X(point), ST_Y(point) FROM
    (SELECT ST_PointOnSurface(geom) AS point FROM units WHERE hierarchy_id=$1 AND code='B01') beat`, prefix+"10")
	if err != nil {
		t.Fatal(err)
	}
	observations, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (firms.Observation, error) {
		o := firms.Observation{Satellite: "N", Instrument: "VIIRS", ObservedAt: observedAt}
		var id string
		err := row.Scan(&id, &o.Longitude, &o.Latitude)
		o.Raw = map[string]string{"reference_id": id}
		return o, err
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func(context.Context) ([]firms.Observation, error) { return observations, nil }
	run := ingestRun{Source: "notifications_" + suffix, Product: demoProduct, WorkspaceID: &workspaceID}
	if counts, err := runIngest(ctx, tx, run, read); err != nil || counts.Inserted != 12 {
		t.Fatalf("ingest reference detections: %+v, %v", counts, err)
	}
	want := map[string][]string{
		"B01":    {"beat.guard", "division.officer"},
		"inside": {"division.officer"}, "shared-boundary": {"division.officer", "state.admin"},
		"ring": {}, "near-edge": {"state.admin"}, "overlap": {"state.admin"},
		"no-unit": {"state.admin"}, "uncoded-forest": {},
	}
	recipients := func(event string) map[string][]string {
		t.Helper()
		rows, err := tx.Query(ctx, `
SELECT d.raw->>'reference_id',ARRAY(SELECT u.username FROM notifications n JOIN users u ON u.id=n.recipient_id
 WHERE n.alert_id=a.id AND n.event=$3 AND n.channel='in_app' ORDER BY u.username)
FROM alerts a JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
WHERE a.workspace_id=$1 AND d.source=$2`, workspaceID, run.Source, event)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := make(map[string][]string)
		for rows.Next() {
			var id string
			var names []string
			if err := rows.Scan(&id, &names); err != nil {
				t.Fatal(err)
			}
			got[id] = names
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	assertNew := func() {
		t.Helper()
		if got := recipients("alert_new"); !reflect.DeepEqual(got, want) {
			t.Fatalf("new alert recipients = %v, want %v", got, want)
		}
	}
	assertNew()
	if counts, err := runIngest(ctx, tx, run, read); err != nil || counts.Duplicates != 12 {
		t.Fatalf("replay: %+v, %v", counts, err)
	}
	assertNew()
	for range 2 {
		// Re-match this fixture without picking up other global demo detections.
		_, err := tx.Exec(ctx, `
INSERT INTO layer_rematches(layer_version_id,river_job_id,window_start,window_end) VALUES($1,0,$2,$2)
ON CONFLICT(layer_version_id) DO UPDATE SET cursor_time=NULL,cursor_id=NULL,finished_at=NULL`,
			prefix+"31", observedAt)
		if err != nil {
			t.Fatal(err)
		}
		var done bool
		if err := tx.QueryRow(ctx, rematchSQL, prefix+"31").Scan(&done); err != nil || !done {
			t.Fatalf("re-match did not finish: %v", err)
		}
		var written, created int
		if err := tx.QueryRow(ctx, "SELECT matches_written,alerts_created FROM layer_rematches WHERE layer_version_id=$1",
			prefix+"31").Scan(&written, &created); err != nil || written < 11 || created != 0 {
			t.Fatalf("re-match wrote %d matches and %d alerts: %v", written, created, err)
		}
		assertNew()
	}
	for id := range want {
		want[id] = []string{}
	}
	for _, id := range []string{"B01", "inside", "shared-boundary"} {
		want[id] = []string{"division.officer"}
	}
	for range 2 {
		if err := checkOverdueNotifications(ctx, tx, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got := recipients("alert_overdue"); !reflect.DeepEqual(got, want) {
			t.Fatalf("overdue recipients = %v, want %v", got, want)
		}
	}
	t.Log("12 observations produced 8 alerts with the expected recipients. Feed replay, two re-matches and a repeated overdue check added zero duplicate notifications.")

	checkNotificationEndpoints(t, ctx, tx, slug, prefix)
}

func checkNotificationEndpoints(t *testing.T, ctx context.Context, tx pgx.Tx, slug, prefix string) {
	t.Helper()
	request := notificationRequests(t, api{reader: tx, logger: slog.Default()}.handler, prefix, "")
	const inbox = "/api/v1/notifications"
	empty := request("a4", "GET", inbox, "")
	if empty.Code != 200 || !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Fatalf("no.role inbox: %d %s", empty.Code, empty.Body)
	}
	var page struct {
		UnreadCount int
		Items       []struct {
			ID, AlertID, Event, ForestClass, BeatName, DivisionName string
			ReadAt                                                  *time.Time
		}
	}
	res := request("a3", "GET", inbox, "")
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &page) != nil ||
		page.UnreadCount != 1 || len(page.Items) != 1 || page.Items[0].ReadAt != nil || page.Items[0].AlertID == "" {
		t.Fatalf("beat.guard inbox: %d %s", res.Code, res.Body)
	}
	item := page.Items[0]
	if item.ForestClass == "" || item.BeatName == "" || item.DivisionName == "" {
		t.Fatalf("missing routing context: %+v", item)
	}
	read := inbox + "/" + page.Items[0].ID + "/read"
	if res := request("a2", "POST", read, ""); res.Code != 404 {
		t.Fatalf("read another user's notification: %d", res.Code)
	}
	for range 2 {
		if res := request("a3", "POST", read, ""); res.Code != 204 {
			t.Fatalf("mark own notification read: %d %s", res.Code, res.Body)
		}
	}
	if res := request("a3", "GET", inbox, ""); res.Code != 200 ||
		!strings.Contains(res.Body.String(), `"unreadCount":0`) || strings.Contains(res.Body.String(), `"readAt":null`) {
		t.Fatalf("read notification: %d %s", res.Code, res.Body)
	}
	checkReadAllNotifications(t, ctx, tx, request, prefix, item.ID)

	rules := "/api/v1/workspaces/" + slug + "/notification-rules"
	if res := request("a2", "GET", rules, ""); res.Code != 403 {
		t.Fatalf("division officer rule access: %d", res.Code)
	}
	var defaults []notificationRule
	if res := request("a1", "GET", rules, ""); res.Code != 200 ||
		json.Unmarshal(res.Body.Bytes(), &defaults) != nil || len(defaults) != 17 {
		t.Fatalf("default rules: %d %s", res.Code, res.Body)
	}
	if res := request("a1", "POST", rules, `[{"event":"alert_new","recipients":"root","channel":"in_app","forestClass":"inside"}]`); res.Code != 200 ||
		!strings.Contains(res.Body.String(), `"forestClass":"inside"`) {
		t.Fatalf("replace rules: %d %s", res.Code, res.Body)
	}
	if res := request("a2", "POST", inbox+"/preferences", `{"emailMuted":true}`); res.Code != 200 {
		t.Fatalf("mute email: %d %s", res.Code, res.Body)
	}
	if res := request("a2", "GET", inbox+"/preferences", ""); res.Code != 200 || !strings.Contains(res.Body.String(), `"emailMuted":true`) {
		t.Fatalf("email preferences: %d %s", res.Code, res.Body)
	}
}

func checkReadAllNotifications(t *testing.T, ctx context.Context, tx pgx.Tx,
	request func(string, string, string, string) *httptest.ResponseRecorder, prefix, beatNoticeID string) {
	t.Helper()
	const inbox = "/api/v1/notifications"
	// Fill more than one page with synthetic notices for the division officer.
	// Keep one unread email and one notice whose recipient loses access.
	if _, err := tx.Exec(ctx, `
INSERT INTO notifications(workspace_id,event,event_id,alert_id,recipient_id,channel,status)
SELECT n.workspace_id,n.event,-i,n.alert_id,u.id,v.channel,'delivered'
FROM notifications n CROSS JOIN users u
CROSS JOIN (SELECT generate_series(1,52) AS i,'in_app' AS channel UNION ALL SELECT 1,'email') v
WHERE n.id=$1::text::bigint AND u.oidc_subject=$2`, beatNoticeID, prefix+"a2"); err != nil {
		t.Fatal(err)
	}
	type page struct {
		UnreadCount int
		HasMore     bool
		Items       []struct {
			ID     string
			ReadAt *time.Time
		}
	}
	list := func(subject, query string) page {
		t.Helper()
		res := request(subject, "GET", inbox+query, "")
		var result page
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &result) != nil {
			t.Fatalf("inbox %s: %d %s", query, res.Code, res.Body)
		}
		return result
	}
	first := list("a2", "?view=unread")
	if len(first.Items) != 50 || !first.HasMore || first.UnreadCount <= 50 {
		t.Fatalf("expected a full first page with more unread items: %+v", first)
	}
	if res := request("a2", "POST", inbox+"/"+first.Items[0].ID+"/read", ""); res.Code != 204 {
		t.Fatalf("mark first item as read: %d %s", res.Code, res.Body)
	}
	second := list("a2", "?view=unread&before="+first.Items[49].ID)
	if second.UnreadCount != first.UnreadCount-1 || len(second.Items) != first.UnreadCount-50 || second.HasMore {
		t.Fatalf("cursor skipped or repeated unread items after marking read: %+v", second)
	}
	if all := list("a2", "?view=all"); len(all.Items) != 50 || all.Items[0].ID != first.Items[0].ID || all.Items[0].ReadAt == nil {
		t.Fatalf("read item moved in newest-first view: %+v", all)
	}
	if legacy := list("a2", ""); len(legacy.Items) != 50 || legacy.Items[0].ID != first.Items[1].ID {
		t.Fatalf("legacy view should still list unread items first")
	}
	for _, query := range []string{"?view=bad", "?before=10", "?view=all&before=0", "?view=unread&offset=50"} {
		if res := request("a2", "GET", inbox+query, ""); res.Code != 400 {
			t.Fatalf("invalid inbox query %s: %d %s", query, res.Code, res.Body)
		}
	}

	if _, err := tx.Exec(ctx, "UPDATE notifications SET read_at=NULL WHERE id=$1::text::bigint", beatNoticeID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM role_assignments WHERE user_id=(SELECT id FROM users WHERE oidc_subject=$1)", prefix+"a3"); err != nil {
		t.Fatal(err)
	}
	if got := list("a3", ""); len(got.Items) != 0 || got.UnreadCount != 0 {
		t.Fatalf("revoked grant still returns notification: %+v", got)
	}
	adminUnread := list("a1", "").UnreadCount
	if adminUnread == 0 {
		t.Fatal("expected another user to have visible unread notices")
	}
	for _, subject := range []string{"a2", "a2", "a3", "a4"} {
		if res := request(subject, "POST", inbox+"/read-all", ""); res.Code != 204 {
			t.Fatalf("mark all as read for %s: %d %s", subject, res.Code, res.Body)
		}
	}
	if got := list("a2", "?view=unread"); got.UnreadCount != 0 || len(got.Items) != 0 || got.HasMore {
		t.Fatalf("bulk update did not clear all pages: %+v", got)
	}
	if got := list("a1", "").UnreadCount; got != adminUnread {
		t.Fatalf("bulk update changed another user's count from %d to %d", adminUnread, got)
	}
	var untouched int
	if err := tx.QueryRow(ctx, `
SELECT count(*) FROM notifications n JOIN users u ON u.id=n.recipient_id
WHERE read_at IS NULL AND (n.id=$1::text::bigint OR (u.oidc_subject=$2 AND channel='email' AND event_id=-1))`,
		beatNoticeID, prefix+"a2").Scan(&untouched); err != nil || untouched != 2 {
		t.Fatalf("bulk update must leave other users, revoked items and email untouched: count=%d, err=%v", untouched, err)
	}
}

func TestNotificationEmailIntegration(t *testing.T) {
	ctx, pool := notificationTestDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	suffix, slug, prefix := loadSeedCopy(t, ctx, tx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	errReply := make(chan struct{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.WriteString(conn, "451 Temporary synthetic outage\r\n")
			close(errReply)
		}
	}()

	t.Setenv("FIMRA_SMTP_ADDR", listener.Addr().String())
	t.Setenv("FIMRA_SMTP_FROM", "open-fimra@example.invalid")
	t.Setenv("FIMRA_SMTP_SECURITY", "local")
	t.Setenv("FIMRA_SMTP_USERNAME", "")
	t.Setenv("FIMRA_SMTP_PASSWORD", "")
	t.Setenv("FIMRA_WEB_URL", "http://127.0.0.1:4200")
	broken, err := readSMTPConfig()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIMRA_SMTP_ADDR", os.Getenv("FIMRA_TEST_MAILPIT_SMTP"))
	mailpit, err := readSMTPConfig()
	if err != nil || mailpit == nil {
		t.Fatalf("configure Mailpit SMTP: %v", err)
	}

	address := "division-" + suffix + "@example.invalid"
	requestMe := func(enabled bool) {
		t.Helper()
		request := notificationRequests(t, func(auth authenticator, origins string) http.Handler {
			auth.emailEnabled = enabled
			return newHandler(pool, auth, slog.Default(), origins, "", nil)
		}, prefix, address)
		if res := request("a2", "GET", "/api/v1/me", ""); res.Code != 200 {
			t.Fatalf("load signed-in user: %d %s", res.Code, res.Body)
		}
	}
	assertAddress := func(want bool) {
		t.Helper()
		var got *string
		err := pool.QueryRow(ctx, "SELECT notification_email FROM users WHERE oidc_subject=$1", prefix+"a2").Scan(&got)
		if err != nil || (got != nil) != want || (got != nil && *got != address) {
			t.Fatalf("email claim stored = %t, want %t: %v", got != nil, want, err)
		}
	}
	requestMe(false)
	assertAddress(false)
	requestMe(true)
	assertAddress(true)

	workspaceID := prefix + "01"
	run := ingestRun{Source: "mail_" + suffix, Product: demoProduct, WorkspaceID: &workspaceID}
	observedAt := time.Now().UTC().Truncate(time.Minute)
	read := func(context.Context) ([]firms.Observation, error) {
		return []firms.Observation{{Satellite: "N", Instrument: "VIIRS", ObservedAt: observedAt,
			Longitude: 86.297475, Latitude: 16.418669}}, nil
	}
	if counts, err := runIngest(ctx, pool, run, read); err != nil || counts.Inserted != 1 {
		t.Fatalf("ingest: %+v %v", counts, err)
	}

	runJob := func(email notificationChannel) {
		t.Helper()
		workers := river.NewWorkers()
		river.AddWorker(workers, &notificationWorker{db: pool, email: email})
		client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
			Queues: map[string]river.QueueConfig{notificationQueue: {MaxWorkers: 1}}, Workers: workers,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err != nil {
			t.Fatal(err)
		}
		events, unsubscribe := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed)
		defer unsubscribe()
		if err := client.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer func() {
			stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := client.Stop(stop); err != nil {
				t.Error(err)
			}
		}()
		insert, err := client.Insert(ctx, notificationArgs{}, &river.InsertOpts{Queue: notificationQueue})
		if err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case event := <-events:
				if event.Job.ID == insert.Job.ID {
					if event.Kind != river.EventKindJobCompleted {
						t.Fatalf("notification job failed: %v", event.Job.Errors)
					}
					return
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for the notification job")
			}
		}
	}
	runJob(broken)
	select {
	case <-errReply:
	default:
		t.Fatal("the failing SMTP listener was not reached")
	}
	var pending bool
	if err := pool.QueryRow(ctx, `
SELECT n.status='pending' AND n.attempts=1 AND n.next_attempt_at>now() AND a.status='retry'
       AND a.error='SMTP greeting: reply 451'
FROM notifications n JOIN notification_attempts a ON a.notification_id=n.id
WHERE n.workspace_id=$1 AND n.channel='email'`, workspaceID).Scan(&pending); err != nil || !pending {
		t.Fatalf("first attempt must record failure and back off: %v", err)
	}
	assertCapturedMail(t, ctx, address, slug, 0)
	if _, err := pool.Exec(ctx, "UPDATE notifications SET next_attempt_at=now() WHERE workspace_id=$1 AND channel='email'", workspaceID); err != nil {
		t.Fatal(err)
	}
	runJob(mailpit)
	assertCapturedMail(t, ctx, address, slug, 1)
	if counts, err := runIngest(ctx, pool, run, read); err != nil || counts.Duplicates != 1 {
		t.Fatalf("replay after delivery: %+v %v", counts, err)
	}
	runJob(mailpit)
	assertCapturedMail(t, ctx, address, slug, 1)
	var attempts []string
	err = pool.QueryRow(ctx, `
SELECT array_agg(a.status ORDER BY a.attempt) FROM notifications n JOIN notification_attempts a ON a.notification_id=n.id
WHERE n.workspace_id=$1 AND n.channel='email' AND n.status='delivered'`, workspaceID).Scan(&attempts)
	if err != nil || !reflect.DeepEqual(attempts, []string{"retry", "delivered"}) {
		t.Fatalf("email attempts: %v %v", attempts, err)
	}
	t.Log("SMTP 451 recorded one retry with backoff. Mailpit captured exactly one email after retry, feed replay and another River run.")
}

func assertCapturedMail(t *testing.T, ctx context.Context, address, workspace string, want int) {
	t.Helper()
	base := os.Getenv("FIMRA_TEST_MAILPIT_URL") + "/api/v1"
	client := &http.Client{Timeout: 5 * time.Second}
	get := func(path string, out any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("Mailpit %s: %d", path, res.StatusCode)
		}
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	var search struct {
		Messages []struct{ ID, Subject string }
	}
	get("/search?query="+url.QueryEscape("to:"+address), &search)
	if len(search.Messages) != want {
		t.Fatalf("Mailpit captured %d messages for the test recipient, want %d", len(search.Messages), want)
	}
	if want > 0 {
		var mail struct{ Text string }
		get("/message/"+search.Messages[0].ID, &mail)
		if search.Messages[0].Subject != "New fire alert" ||
			!strings.Contains(mail.Text, "workspace="+workspace) || !strings.Contains(mail.Text, "alert=") ||
			strings.Contains(mail.Text, "86.297475") || strings.Contains(mail.Text, "16.418669") {
			t.Fatal("Mailpit message must have a short subject and a link without coordinates")
		}
	}
}

func notificationRequests(t *testing.T, handler func(authenticator, string) http.Handler, prefix, email string) func(string, string, string, string) *httptest.ResponseRecorder {
	t.Helper()
	key, signer := testSigner(t)
	const issuer, audience = "https://issuer.invalid", "open-fimra-api"
	auth := authenticator{issuers: map[string]bool{issuer: true}, verifier: oidc.NewVerifier(issuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: audience})}
	h := handler(auth, devOrigins)
	return func(subject, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		claims := jwt.Claims{Issuer: issuer, Subject: prefix + subject, Audience: jwt.Audience{audience}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute))}
		token, err := jwt.Signed(signer).Claims(claims).Claims(map[string]any{
			"preferred_username": "demo-user", "email": email, "email_verified": true,
		}).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
}

func notificationTestDB(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make api-test-integration with the development stack running and migrated.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, loadConfig().databaseURL)
	if err != nil {
		t.Fatal(fmt.Errorf("open notification test database: %w", err))
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}
