package main

import (
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// phone is a synthetic field phone: a device ID and Web Push keys.
type phone struct {
	id       string
	key      *ecdh.PrivateKey
	auth     []byte
	endpoint string
}

func newPhone(t *testing.T, endpoint string) phone {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p := phone{key: key, auth: make([]byte, 16), endpoint: endpoint}
	_, _ = rand.Read(p.auth)
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	id[6], id[8] = id[6]&0x0f|0x40, id[8]&0x3f|0x80
	p.id = fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:])
	return p
}

func (p phone) registration() string {
	body, _ := json.Marshal(deviceRegistration{Platform: "android", AppVersion: "0.2.0+2", Push: &devicePush{
		Provider: "unifiedpush", Endpoint: p.endpoint,
		PublicKey: base64.RawURLEncoding.EncodeToString(p.key.PublicKey().Bytes()),
		Auth:      base64.RawURLEncoding.EncodeToString(p.auth)}})
	return string(body)
}

// fakePushServer stands in for a second push server. It answers with the
// given status codes in turn, then 201.
type fakePushServer struct {
	*httptest.Server
	mu     sync.Mutex
	codes  map[string][]int
	bodies map[string][][]byte
}

func newFakePushServer() *fakePushServer {
	f := &fakePushServer{codes: map[string][]int{}, bodies: map[string][][]byte{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.bodies[r.URL.Path] = append(f.bodies[r.URL.Path], body)
		code := http.StatusCreated
		if queue := f.codes[r.URL.Path]; len(queue) > 0 {
			code, f.codes[r.URL.Path] = queue[0], queue[1:]
		}
		w.WriteHeader(code)
	}))
	return f
}

func (f *fakePushServer) received(path string) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.bodies[path]...)
}

// ntfyMessages reads the messages a phone's push app would receive from the
// local ntfy server.
func ntfyMessages(t *testing.T, ctx context.Context, base, topic string) [][]byte {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+topic+"/json?poll=1", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out [][]byte
	decoder := json.NewDecoder(res.Body)
	for {
		var m struct{ Event, Message, Encoding string }
		if err := decoder.Decode(&m); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		if m.Event != "message" || m.Encoding != "base64" {
			t.Fatalf("ntfy message %+v must be a binary UnifiedPush message", m)
		}
		body, err := base64.StdEncoding.DecodeString(m.Message)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, body)
	}
}

func readPush(t *testing.T, p phone, body []byte) pushPayload {
	t.Helper()
	plain, err := decryptWebPush(body, p.key, p.auth)
	if err != nil {
		t.Fatalf("the phone could not decrypt its push message: %v", err)
	}
	var payload pushPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func runNotificationJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, worker *notificationWorker) {
	t.Helper()
	workers := river.NewWorkers()
	river.AddWorker(workers, worker)
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

// TestDevicesIntegration registers synthetic phones, sends alert pushes
// through the local ntfy server and a fake second server, replays the feed,
// and wipes a phone.
func TestDevicesIntegration(t *testing.T) {
	ctx, pool := notificationTestDB(t)
	ntfy := os.Getenv("FIMRA_TEST_PUSH_URL")
	if ntfy == "" {
		t.Fatal("Set FIMRA_TEST_PUSH_URL to the local ntfy server; make api-test-integration does this.")
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	suffix, slug, prefix := loadSeedCopy(t, ctx, tx)
	// A patch of synthetic forest inside B01 makes its alerts go to the beat guard.
	var longitude, latitude float64
	if err := tx.QueryRow(ctx, `
WITH beat AS (SELECT ST_PointOnSurface(geom) AS point FROM units WHERE hierarchy_id=$1 AND code='B01'),
grown AS (UPDATE layer_features SET geom=ST_Multi(ST_Union(geom,ST_Buffer(beat.point,0.002)))
    FROM beat WHERE layer_version_id=$2 AND code='F01')
SELECT ST_X(point), ST_Y(point) FROM beat`, prefix+"10", prefix+"31").Scan(&longitude, &latitude); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Later runs classify their detections in this workspace too. Its phones
	// must not receive their pushes.
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `UPDATE devices d SET revoked_at=now(), revoked_reason='owner'
FROM users u WHERE u.id=d.user_id AND u.oidc_subject LIKE $1 AND d.revoked_at IS NULL`, prefix[:8]+"%")
		if err != nil {
			t.Error(err)
		}
	})
	fake := newFakePushServer()
	defer fake.Close()
	settings := pushSettings{servers: []string{ntfy, fake.URL}, web: newWebPushSender()}
	request := deviceRequests(t, func(auth authenticator, origins string) http.Handler {
		return newHandler(pool, auth, slog.Default(), origins, "", nil, withPush(settings))
	}, prefix)
	guard, officer, admin, nobody := "a3", "a2", "a1", "a4"
	const sid = "session-guard-phone"

	// Registration.
	topic := "upfimra" + suffix
	main := newPhone(t, ntfy+"/"+topic+"?up=1")
	second := newPhone(t, fake.URL+"/up-second")
	stale := newPhone(t, fake.URL+"/up-stale")
	fake.codes["/up-second"] = []int{503}
	fake.codes["/up-stale"] = []int{410}
	elsewhere := newPhone(t, "https://push.elsewhere.example.invalid/upAbc")
	expect := func(res *httptest.ResponseRecorder, code int, contains string) {
		t.Helper()
		if res.Code != code || !strings.Contains(res.Body.String(), contains) {
			t.Fatalf("got %d %s, want %d with %q", res.Code, res.Body, code, contains)
		}
	}
	path := func(p phone) string { return "/api/v1/devices/" + p.id }
	expect(request(guard, sid, main.id, "PUT", path(main), `{"platform":"tablet","appVersion":"1"}`), 400, "platform")
	expect(request(guard, sid, main.id, "PUT", path(main), `{"platform":"android","appVersion":"1","push":{"provider":"unifiedpush","endpoint":"`+main.endpoint+`","publicKey":"bad","auth":"bad"}}`), 400, "keys")
	expect(request(guard, sid, "", "PUT", "/api/v1/devices/not-a-uuid", main.registration()), 404, "not found")
	for range 2 {
		expect(request(guard, sid, main.id, "PUT", path(main), main.registration()), 200, `"push":true`)
	}
	expect(request(guard, "s2", second.id, "PUT", path(second), second.registration()), 200, `"push":true`)
	expect(request(guard, "s3", stale.id, "PUT", path(stale), stale.registration()), 200, `"push":true`)
	expect(request(guard, "s4", elsewhere.id, "PUT", path(elsewhere), elsewhere.registration()), 200, `"pushProblem"`)
	expect(request(officer, "", "", "PUT", path(main), main.registration()), 404, "not found")
	var registered int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='device.register' AND target_id=$1`, main.id).Scan(&registered); err != nil || registered != 1 {
		t.Fatalf("device.register audited %d times: %v", registered, err)
	}

	// Owners see their own devices; administrators see their part of the hierarchy.
	expect(request(guard, sid, main.id, "GET", "/api/v1/devices", ""), 200, `"current":true`)
	if res := request(officer, "", "", "GET", "/api/v1/devices", ""); strings.Contains(res.Body.String(), main.id) {
		t.Fatalf("another user's devices: %s", res.Body)
	}
	devices := "/api/v1/workspaces/" + slug + "/devices"
	for _, who := range []string{admin, officer} {
		expect(request(who, "", "", "GET", devices, ""), 200, main.id)
	}
	for _, who := range []string{guard, nobody} {
		expect(request(who, "", "", "GET", devices, ""), 403, "role is required")
		expect(request(who, "", "", "POST", devices+"/"+main.id+"/wipe", ""), 404, "not found")
	}

	// A new alert in B01 reaches each of the guard's phones once.
	workspaceID := prefix + "01"
	run := ingestRun{Source: "devices_" + suffix, Product: demoProduct, WorkspaceID: &workspaceID}
	// This test commits its data, so its detections use a time no other test
	// uses. Otherwise they would join other tests' feeds as duplicates.
	var offset int64
	if _, err := fmt.Sscanf(suffix, "%x", &offset); err != nil {
		t.Fatal(err)
	}
	observedAt := time.Date(2003, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(offset%500000) * time.Minute)
	read := func(context.Context) ([]firms.Observation, error) {
		return []firms.Observation{{Satellite: "N", Instrument: "VIIRS", ObservedAt: observedAt, Longitude: longitude, Latitude: latitude}}, nil
	}
	if counts, err := runIngest(ctx, pool, run, read); err != nil || counts.Inserted != 1 {
		t.Fatalf("ingest: %+v %v", counts, err)
	}
	var alertID int64
	var pushRecipients []string
	if err := pool.QueryRow(ctx, `
SELECT a.id, ARRAY(SELECT u.username FROM notifications n JOIN users u ON u.id=n.recipient_id
    WHERE n.alert_id=a.id AND n.channel='push' ORDER BY 1)
FROM alerts a JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
WHERE d.source=$1 AND a.workspace_id=$2`, run.Source, workspaceID).
		Scan(&alertID, &pushRecipients); err != nil || len(pushRecipients) != 1 {
		t.Fatalf("push recipients = %v: %v", pushRecipients, err)
	}
	worker := &notificationWorker{db: pool, push: settings}
	runNotificationJob(t, ctx, pool, worker)
	checkMain := func(want int) []pushPayload {
		t.Helper()
		messages := ntfyMessages(t, ctx, ntfy, topic)
		if len(messages) != want {
			t.Fatalf("ntfy has %d messages for the guard's phone, want %d", len(messages), want)
		}
		var out []pushPayload
		for _, m := range messages {
			out = append(out, readPush(t, main, m))
		}
		return out
	}
	got := checkMain(1)[0]
	if got.Type != "notification" || got.Alert != fmt.Sprint(alertID) || got.Workspace != slug || got.Title != "New fire alert" ||
		got.Event != "alert_new" || got.Notification == "" {
		t.Fatalf("push payload = %+v", got)
	}
	for _, body := range [][]byte{marshalPushPayload(got)} {
		if strings.Contains(string(body), fmt.Sprintf("%.4f", longitude)) || strings.Contains(string(body), "B01") {
			t.Fatalf("push payload must not hold the place: %s", body)
		}
	}

	// Replay the feed and run the job again: no second message.
	if counts, err := runIngest(ctx, pool, run, read); err != nil || counts.Duplicates != 1 {
		t.Fatalf("replay: %+v %v", counts, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE push_messages p SET next_attempt_at=now() FROM devices d
WHERE d.id=p.device_id AND p.status='pending' AND d.id=$1`, second.id); err != nil {
		t.Fatal(err)
	}
	runNotificationJob(t, ctx, pool, worker)
	checkMain(1)
	if bodies := fake.received("/up-second"); len(bodies) != 2 || readPush(t, second, bodies[1]).Alert != fmt.Sprint(alertID) {
		t.Fatalf("second phone got %d attempts, want a failure then the message", len(bodies))
	}
	var status string
	var stalePush bool
	if err := pool.QueryRow(ctx, `SELECT n.status, (SELECT device_can_push(d) FROM devices d WHERE d.id=$2)
FROM notifications n WHERE n.alert_id=$1 AND n.channel='push'`, alertID, stale.id).Scan(&status, &stalePush); err != nil ||
		status != "delivered" || stalePush {
		t.Fatalf("push notification %s, stale phone can still push %t: %v", status, stalePush, err)
	}
	t.Log("One alert reached each of the guard's phones once through ntfy and the fake server, after a retry, a feed replay and a second job. A gone address was dropped.")

	// Revoked devices are told so and get no more pushes.
	expect(request(guard, "s2", second.id, "DELETE", path(second)+"?reason=sign_out", ""), 204, "")
	expect(request(guard, "s2", second.id, "GET", "/api/v1/me", ""), 401, "device_revoked")
	expect(request(guard, "s2", second.id, "PUT", path(second), second.registration()), 401, "device_revoked")
	expect(request(guard, "s9", "", "DELETE", path(second), ""), 204, "")
	expect(request(officer, "", "", "DELETE", path(main), ""), 404, "not found")

	// An officer wipes the main phone. Its next call is told to wipe, with or
	// without its device ID, and other sessions carry on.
	expect(request(officer, "", "", "POST", devices+"/"+main.id+"/wipe", ""), 200, `"wipe_pending"`)
	expect(request(admin, "", "", "POST", devices+"/"+main.id+"/wipe", ""), 200, `"wipe_pending"`)
	runNotificationJob(t, ctx, pool, worker)
	if check := checkMain(2)[1]; check.Type != "device_check" || check.Alert != "" {
		t.Fatalf("wipe prompt = %+v", check)
	}
	expect(request(guard, sid, main.id, "GET", "/api/v1/me", ""), 401, "device_wiped")
	expect(request(guard, sid, "", "GET", "/api/v1/workspaces/"+slug+"/alerts", ""), 401, "device_wiped")
	expect(request(guard, sid, main.id, "PUT", path(main), main.registration()), 401, "device_wiped")
	expect(request(guard, "another-session", "", "GET", "/api/v1/me", ""), 200, "roleAssignments")
	expect(request(officer, "", "", "POST", "/api/v1/devices/"+main.id+"/wiped", ""), 404, "not found")
	for range 2 {
		expect(request(guard, sid, main.id, "POST", "/api/v1/devices/"+main.id+"/wiped", ""), 204, "")
	}
	expect(request(admin, "", "", "GET", devices, ""), 200, `"wiped"`)
	// An owner can wipe their own phone too.
	expect(request(guard, "s3", "", "POST", "/api/v1/devices/"+stale.id+"/wipe", ""), 200, `"wipe_pending"`)

	var audit []string
	if err := pool.QueryRow(ctx, `SELECT array_agg(action ORDER BY id) FROM audit_log
WHERE target_type='device' AND target_id=$1`, main.id).Scan(&audit); err != nil ||
		strings.Join(audit, ",") != "device.register,device.wipe_request,device.wipe_signal,device.wipe_confirm" {
		t.Fatalf("audit for the wiped phone = %v: %v", audit, err)
	}
	var wipeWorkspace *string
	if err := pool.QueryRow(ctx, `SELECT workspace_id::text FROM audit_log WHERE action='device.wipe_request' AND target_id=$1`,
		main.id).Scan(&wipeWorkspace); err != nil || wipeWorkspace == nil || *wipeWorkspace != workspaceID {
		t.Fatalf("wipe request audit workspace = %v: %v", wipeWorkspace, err)
	}

	// A wiped or revoked phone receives nothing for a new alert.
	run.Source += "_later"
	observedAt = observedAt.Add(-48 * time.Hour)
	if counts, err := runIngest(ctx, pool, run, read); err != nil || counts.Inserted != 1 {
		t.Fatalf("second ingest: %+v %v", counts, err)
	}
	runNotificationJob(t, ctx, pool, worker)
	checkMain(2)
	if bodies := fake.received("/up-second"); len(bodies) != 2 {
		t.Fatalf("a revoked phone received a push: %d", len(bodies))
	}
	t.Log("Revoked and wiped phones were refused with plain messages, audited, and received no further alert pushes.")
}

// deviceRequests signs requests as a seed user with a session ID and,
// when given, a device ID header.
func deviceRequests(t *testing.T, handler func(authenticator, string) http.Handler, prefix string) func(subject, sid, device, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	key, signer := testSigner(t)
	const issuer, audience = "https://issuer.invalid", "open-fimra-api"
	auth := authenticator{issuers: map[string]bool{issuer: true}, verifier: oidc.NewVerifier(issuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: audience})}
	h := handler(auth, devOrigins)
	return func(subject, sid, device, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		claims := jwt.Claims{Issuer: issuer, Subject: prefix + subject, Audience: jwt.Audience{audience},
			Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute))}
		extra := map[string]any{"preferred_username": "demo-" + subject}
		if sid != "" {
			extra["sid"] = prefix + sid
		}
		token, err := jwt.Signed(signer).Claims(claims).Claims(extra).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		if device != "" {
			r.Header.Set(deviceHeader, device)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
}
