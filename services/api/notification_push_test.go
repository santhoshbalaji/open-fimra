package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// decryptWebPush is the phone's side of RFC 8291, for tests.
func decryptWebPush(body []byte, phone *ecdh.PrivateKey, authSecret []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("short message")
	}
	salt, idLen := body[:16], int(body[20])
	if binary.BigEndian.Uint32(body[16:20]) != webPushRecordSize || len(body) < 21+idLen {
		return nil, errors.New("bad header")
	}
	serverKey, ciphertext := body[21:21+idLen], body[21+idLen:]
	server, err := ecdh.P256().NewPublicKey(serverKey)
	if err != nil {
		return nil, err
	}
	shared, err := phone.ECDH(server)
	if err != nil {
		return nil, err
	}
	info := "WebPush: info\x00" + string(phone.PublicKey().Bytes()) + string(serverKey)
	ikm, _ := hkdf.Key(sha256.New, shared, authSecret, info, 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	key, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	end := bytes.LastIndexByte(plain, 2)
	if end < 0 {
		return nil, errors.New("no record delimiter")
	}
	return plain[:end], nil
}

// TestWebPushExample checks the example message in RFC 8291 appendix A.
func TestWebPushExample(t *testing.T) {
	server, err := ecdh.P256().NewPrivateKey(b64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	phone, err := ecdh.P256().NewPrivateKey(b64(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	if err != nil {
		t.Fatal(err)
	}
	auth := b64(t, "BTBZMqHH6r4Tts7J_aSIgg")
	want := b64(t, "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN")
	got, err := encryptWebPushWith([]byte("When I grow up, I want to be a watermelon"), phone.PublicKey().Bytes(), auth,
		server, b64(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("RFC 8291 example: %v\n got %x\nwant %x", err, got, want)
	}
	plain, err := decryptWebPush(want, phone, auth)
	if err != nil || string(plain) != "When I grow up, I want to be a watermelon" {
		t.Fatalf("decrypt example: %q %v", plain, err)
	}

	// Random keys and salt each time, and only the phone can read it.
	first, _ := encryptWebPush([]byte("hello"), phone.PublicKey().Bytes(), auth)
	second, _ := encryptWebPush([]byte("hello"), phone.PublicKey().Bytes(), auth)
	if bytes.Equal(first, second) {
		t.Fatal("two encryptions of one message must differ")
	}
	other, _ := ecdh.P256().GenerateKey(rand.Reader)
	if _, err := decryptWebPush(first, other, auth); err == nil {
		t.Fatal("another key must not decrypt the message")
	}
	if _, err := encryptWebPush(make([]byte, 4000), phone.PublicKey().Bytes(), auth); err == nil {
		t.Fatal("a message longer than one record must be refused")
	}
	if _, err := encryptWebPush([]byte("x"), []byte("not a key"), auth); err == nil {
		t.Fatal("a bad phone key must be refused")
	}
}

func TestPushPayloadHasNoDetail(t *testing.T) {
	alert := int64(77)
	body := marshalPushPayload(newPushPayload(pushMessage{kind: "notification", notificationID: 12, event: "alert_new",
		workspace: "demo", alertID: &alert}))
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"v": 1.0, "type": "notification", "notification": "12", "event": "alert_new",
		"workspace": "demo", "alert": "77", "title": "New fire alert", "body": pushBody}
	if len(fields) != len(want) {
		t.Fatalf("payload fields = %v", fields)
	}
	for key, value := range want {
		if fields[key] != value {
			t.Fatalf("payload %s = %v, want %v", key, fields[key], value)
		}
	}
	if got := string(marshalPushPayload(newPushPayload(pushMessage{kind: "device_check"}))); got != `{"v":1,"type":"device_check"}` {
		t.Fatalf("device check payload = %s", got)
	}
}

func TestPushServers(t *testing.T) {
	for _, bad := range []string{"ftp://push.example.org", "http://push.example.org", "https://push.example.org/path",
		"https://user@push.example.org", "push.example.org", "https://push.example.org?x=1"} {
		if _, err := pushServerOrigin(bad); err == nil {
			t.Errorf("push server %q must be refused", bad)
		}
	}
	t.Setenv("FIMRA_PUSH_SERVERS", " https://Push.example.org/ , http://127.0.0.1:18110")
	t.Setenv("FIMRA_PUSH_FCM_CREDENTIALS_FILE", "")
	settings, err := readPushSettings()
	if err != nil || settings.web == nil || settings.fcm != nil ||
		strings.Join(settings.servers, " ") != "https://push.example.org http://127.0.0.1:18110" {
		t.Fatalf("push settings = %+v, %v", settings, err)
	}
	if got := strings.Join(settings.providers(), ","); got != "unifiedpush" {
		t.Fatalf("providers = %s", got)
	}
	for endpoint, want := range map[string]bool{
		"https://push.example.org/upAbc123?up=1":                true,
		"https://PUSH.example.org/upAbc123":                     true,
		"http://127.0.0.1:18110/upAbc":                          true,
		"https://push.example.org/":                             false,
		"https://push.example.org.evil.test/upAbc":              false,
		"https://push.example.org:8443/upAbc":                   false,
		"https://user@push.example.org/upAbc":                   false,
		"http://push.example.org/upAbc":                         false,
		"https://other.example.org/upAbc":                       false,
		"http://169.254.169.254/latest/meta-data":               false,
		"https://push.example.org/upAbc#fragment":               false,
		"https://push.example.org/" + strings.Repeat("a", 4100): false,
	} {
		if got := settings.allowedEndpoint(endpoint); got != want {
			t.Errorf("allowed(%s) = %t, want %t", endpoint, got, want)
		}
	}
	t.Setenv("FIMRA_PUSH_SERVERS", "")
	if settings, err := readPushSettings(); err != nil || settings.enabled() {
		t.Fatalf("push must be off by default: %+v %v", settings, err)
	}
}

func TestCheckPush(t *testing.T) {
	phone, _ := ecdh.P256().GenerateKey(rand.Reader)
	key := base64.RawURLEncoding.EncodeToString(phone.PublicKey().Bytes())
	auth := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	on := api{push: pushSettings{servers: []string{"https://push.example.org"}, web: newWebPushSender()}}
	off := api{}
	good := &devicePush{Provider: "unifiedpush", Endpoint: "https://push.example.org/upAbc?up=1", PublicKey: key, Auth: auth}
	if cols, problem, ok := on.checkPush(good); !ok || problem != "" || cols.endpoint == nil || len(cols.publicKey) != 65 {
		t.Fatalf("good push: %+v %q %t", cols, problem, ok)
	}
	if cols, problem, ok := off.checkPush(good); !ok || cols.endpoint != nil || !strings.Contains(problem, "UnifiedPush") {
		t.Fatalf("push off: %+v %q %t", cols, problem, ok)
	}
	elsewhere := *good
	elsewhere.Endpoint = "https://ntfy.example.net/upAbc"
	if cols, problem, ok := on.checkPush(&elsewhere); !ok || cols.endpoint != nil || !strings.Contains(problem, "https://push.example.org") {
		t.Fatalf("other server: %+v %q %t", cols, problem, ok)
	}
	for _, bad := range []devicePush{
		{Provider: "unifiedpush", Endpoint: good.Endpoint, PublicKey: "short", Auth: auth},
		{Provider: "unifiedpush", Endpoint: good.Endpoint, PublicKey: key},
		{Provider: "unifiedpush", Endpoint: good.Endpoint, PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, 65)), Auth: auth},
		{Provider: "fcm", Endpoint: "token with spaces"},
		{Provider: "fcm", Endpoint: "token", PublicKey: key, Auth: auth},
		{Provider: "apns", Endpoint: "x"},
	} {
		if _, _, ok := on.checkPush(&bad); ok {
			t.Errorf("push %+v must be refused", bad)
		}
	}
	if cols, problem, ok := on.checkPush(&devicePush{Provider: "fcm", Endpoint: "fcm-token"}); !ok || cols.endpoint != nil ||
		!strings.Contains(problem, "Google") {
		t.Fatalf("FCM off: %+v %q %t", cols, problem, ok)
	}
	if cols, problem, ok := on.checkPush(nil); !ok || problem != "" || cols.endpoint != nil {
		t.Fatal("no push is fine")
	}
}

func TestWebPushSender(t *testing.T) {
	phone, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	codes := []int{201, 503, 410, 400, 429}
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") == "" || r.Header.Get("Urgency") != "high" {
			w.WriteHeader(400)
			return
		}
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://169.254.169.254/", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(codes[0])
		codes = codes[1:]
	}))
	defer server.Close()
	sender := newWebPushSender()
	target := pushTarget{provider: "unifiedpush", endpoint: server.URL + "/upAbc", publicKey: phone.PublicKey().Bytes(), auth: auth}
	for _, want := range []string{"delivered", "retry", "gone", "failed", "retry"} {
		if got, err := sender.send(context.Background(), target, []byte(`{"v":1}`)); got != want {
			t.Fatalf("status = %s (%v), want %s", got, err, want)
		}
	}
	plain, err := decryptWebPush(bodies[0], phone, auth)
	if err != nil || string(plain) != `{"v":1}` {
		t.Fatalf("the push server got %q: %v", plain, err)
	}
	target.endpoint = server.URL + "/redirect"
	if got, _ := sender.send(context.Background(), target, []byte(`{}`)); got != "failed" {
		t.Fatalf("a redirect must not be followed: %s", got)
	}
	server.Close()
	if got, err := sender.send(context.Background(), target, []byte(`{}`)); got != "retry" || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("an unreachable push server: %s %v", got, err)
	}
}

func TestFCMSender(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	tokens, sends := 0, 0
	var lastMessage map[string]any
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			parts := strings.Split(r.PostForm.Get("assertion"), ".")
			if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || len(parts) != 3 {
				w.WriteHeader(400)
				return
			}
			var claims map[string]any
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			_ = json.Unmarshal(raw, &claims)
			if claims["scope"] != fcmScope || claims["iss"] != "push@demo-project.example.invalid" {
				w.WriteHeader(400)
				return
			}
			tokens++
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fake-access", "expires_in": 3600})
		case "/v1/projects/demo-project/messages:send":
			if r.Header.Get("Authorization") != "Bearer fake-access" {
				w.WriteHeader(401)
				return
			}
			sends++
			var body struct{ Message map[string]any }
			_ = json.NewDecoder(r.Body).Decode(&body)
			lastMessage = body.Message
			switch body.Message["token"] {
			case "gone-token":
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `{"error":{"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`)
			case "busy-token":
				w.WriteHeader(503)
			default:
				_, _ = io.WriteString(w, `{"name":"projects/demo-project/messages/1"}`)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()

	file := filepath.Join(t.TempDir(), "service-account.json")
	credentials, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "demo-project",
		"client_email": "push@demo-project.example.invalid", "private_key": pemKey, "token_uri": "https://oauth2.example.invalid/token"})
	if err := os.WriteFile(file, credentials, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIMRA_PUSH_SERVERS", "")
	t.Setenv("FIMRA_PUSH_FCM_CREDENTIALS_FILE", file)
	settings, err := readPushSettings()
	if err != nil || settings.fcm == nil || settings.web != nil || strings.Join(settings.providers(), ",") != "fcm" {
		t.Fatalf("FCM settings: %+v %v", settings, err)
	}
	sender := settings.fcm.(*fcmSender)
	// Point the sender at the fake Google servers.
	sender.tokenURL, sender.apiURL = fake.URL+"/token", fake.URL
	for token, want := range map[string]string{"phone-token": "delivered", "gone-token": "gone", "busy-token": "retry"} {
		if got, err := sender.send(context.Background(), pushTarget{provider: "fcm", endpoint: token}, []byte(`{"v":1}`)); got != want {
			t.Fatalf("%s: %s %v, want %s", token, got, err, want)
		}
	}
	if tokens != 1 || sends != 3 {
		t.Fatalf("FCM sign-in %d times and %d sends, want 1 and 3", tokens, sends)
	}
	if data, _ := lastMessage["data"].(map[string]any); data["fimra"] != `{"v":1}` || lastMessage["notification"] != nil {
		t.Fatalf("FCM message must be data only: %v", lastMessage)
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	for _, content := range []string{"not json", `{"type":"service_account","project_id":"p","client_email":"e","private_key":"x","token_uri":"https://t"}`,
		`{"type":"service_account","project_id":"p","client_email":"e","private_key":"` + strings.ReplaceAll(pemKey, "\n", `\n`) + `","token_uri":"http://plain"}`} {
		_ = os.WriteFile(bad, []byte(content), 0o600)
		t.Setenv("FIMRA_PUSH_FCM_CREDENTIALS_FILE", bad)
		if _, err := readPushSettings(); err == nil {
			t.Errorf("credentials %q must be refused", content[:min(len(content), 40)])
		}
	}
}
