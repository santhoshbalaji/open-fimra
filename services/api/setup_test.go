package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// A made-up key in the FIRMS format. It works only with the fake server.
const testMapKey = "0123456789abcdef0123456789abcdef"

func randomSettingsKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func TestSettingsCipher(t *testing.T) {
	first, second := randomSettingsKey(t), randomSettingsKey(t)
	const workspace = "d0000000-0000-4000-8000-000000000001"
	current, err := newSettingsCipher(first, "")
	if err != nil {
		t.Fatal(err)
	}

	sealed, err := current.seal(workspace, firmsSecret, testMapKey)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := current.seal(workspace, firmsSecret, testMapKey)
	if bytes.Contains(sealed.Ciphertext, []byte(testMapKey)) || bytes.Equal(sealed.Nonce, again.Nonce) ||
		bytes.Equal(sealed.Ciphertext, again.Ciphertext) || len(sealed.KeyID) != 16 {
		t.Fatalf("sealing must hide the value and use a fresh nonce each time: %+v", sealed)
	}

	t.Run("round trip", func(t *testing.T) {
		opened, err := current.open(workspace, firmsSecret, sealed)
		if err != nil || opened != testMapKey {
			t.Fatalf("opened %q, %v", opened, err)
		}
	})

	t.Run("wrong key fails", func(t *testing.T) {
		other, _ := newSettingsCipher(second, "")
		_, err := other.open(workspace, firmsSecret, sealed)
		if err == nil || !strings.Contains(err.Error(), "FIMRA_SETTINGS_OLD_KEYS") || strings.Contains(err.Error(), testMapKey) {
			t.Fatalf("an unknown key ID must fail with a hint and no secret: %v", err)
		}
		// Same key ID, different key material: authentication must fail.
		forged := sealed
		forged.KeyID = other.currentID
		if _, err := other.open(workspace, firmsSecret, forged); err == nil || strings.Contains(err.Error(), testMapKey) {
			t.Fatalf("the wrong key opened the secret: %v", err)
		}
	})

	t.Run("changed data fails", func(t *testing.T) {
		tampered := sealed
		tampered.Ciphertext = append([]byte(nil), sealed.Ciphertext...)
		tampered.Ciphertext[0] ^= 1
		if _, err := current.open(workspace, firmsSecret, tampered); err == nil {
			t.Fatal("a changed ciphertext opened")
		}
		if _, err := current.open("d0000000-0000-4000-8000-000000000002", firmsSecret, sealed); err == nil {
			t.Fatal("a ciphertext moved to another workspace opened")
		}
	})

	t.Run("rotation keeps old keys readable", func(t *testing.T) {
		rotated, err := newSettingsCipher(second, " "+first+" ,")
		if err != nil {
			t.Fatal(err)
		}
		if rotated.currentID == current.currentID {
			t.Fatal("different keys must have different IDs")
		}
		opened, err := rotated.open(workspace, firmsSecret, sealed)
		if err != nil || opened != testMapKey {
			t.Fatalf("an old key could not open its secret: %v", err)
		}
		fresh, _ := rotated.seal(workspace, firmsSecret, opened)
		if fresh.KeyID != rotated.currentID {
			t.Fatal("new secrets must use the current key")
		}
	})

	t.Run("configuration", func(t *testing.T) {
		if off, err := newSettingsCipher("", ""); off != nil || err != nil {
			t.Fatalf("an empty key turns settings off: %v", err)
		}
		if _, err := off().seal(workspace, firmsSecret, testMapKey); !errors.Is(err, errSecretsOff) {
			t.Fatalf("sealing without a key: %v", err)
		}
		for _, bad := range []struct{ current, old string }{
			{"not base64!", ""}, {base64.StdEncoding.EncodeToString([]byte("short")), ""}, {"", first}, {first, "bad"},
		} {
			if _, err := newSettingsCipher(bad.current, bad.old); err == nil {
				t.Errorf("accepted key %q with old keys %q", bad.current, bad.old)
			} else if strings.Contains(err.Error(), first) {
				t.Errorf("the error repeats a key: %v", err)
			}
		}
	})

	if got := secretHint(testMapKey); got != "cdef" {
		t.Fatalf("hint %q, want the last four characters", got)
	}
	if got := secretHint("short"); got != "" {
		t.Fatalf("a short secret must have no hint, got %q", got)
	}
}

func off() *settingsCipher { return nil }

func TestSetupGuard(t *testing.T) {
	code, err := generateSetupCode()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-HJKMNP-Z2-9]{4}(-[A-HJKMNP-Z2-9]{4}){3}$`).MatchString(code) {
		t.Fatalf("unexpected setup code format %q", code)
	}
	if other, _ := generateSetupCode(); other == code {
		t.Fatal("two setup codes were the same")
	}

	now := time.Date(2026, 11, 25, 6, 0, 0, 0, time.UTC)
	guard := newSetupGuard(code)
	guard.now = func() time.Time { return now }
	if ok, paused := guard.check(strings.ToLower(strings.ReplaceAll(code, "-", " "))); !ok || paused {
		t.Fatal("the code must match without dashes and in lower case")
	}
	for i := range setupAttempts {
		if ok, paused := guard.check("WRONG-CODE-0000-000" + string(rune('A'+i))); ok || paused {
			t.Fatalf("wrong code %d: ok %v, paused %v", i, ok, paused)
		}
	}
	if ok, paused := guard.check(code); ok || !paused {
		t.Fatal("too many wrong codes must pause even the right code")
	}
	now = now.Add(setupAttemptWindow)
	if ok, paused := guard.check(code); !ok || paused {
		t.Fatal("the pause must end after the window")
	}
}

func TestFIRMSConnectionTest(t *testing.T) {
	fake := newFakeFIRMS(t, testMapKey)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	keys := newFIRMSKeys(nil, nil, testMapKey, fake.transport())

	result := testFIRMSConnection(t.Context(), keys.client(testMapKey))
	if !result.OK || !strings.Contains(result.Message, "VIIRS_SNPP_NRT has data from 2012-01-20") {
		t.Fatalf("unexpected result with the right key: %+v", result)
	}
	const wrong = "fedcba9876543210fedcba9876543210"
	result = testFIRMSConnection(t.Context(), keys.client(wrong))
	logger.Warn("FIRMS connection test failed", "reason", result.Message)
	if result.OK || !strings.Contains(result.Message, "HTTP 403") {
		t.Fatalf("a rejected key must fail with the HTTP status: %+v", result)
	}

	// A transport error names the URL, which holds the key. It must be removed.
	broken := firms.NewArea(wrong, roundTripper(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("dial " + r.URL.String() + ": connection refused")
	}))
	result = testFIRMSConnection(context.Background(), broken)
	logger.Warn("FIRMS connection test failed", "reason", result.Message)
	if result.OK || !strings.Contains(result.Message, "[redacted]") {
		t.Fatalf("the transport error must be reported with the key removed: %+v", result)
	}
	for _, output := range []string{result.Message, logs.String()} {
		if strings.Contains(output, wrong) || strings.Contains(output, testMapKey) {
			t.Fatalf("a FIRMS key leaked: %s", output)
		}
	}
	if len(fake.seen()) != 2 {
		t.Fatalf("expected two availability requests, got %v", fake.seen())
	}

	// Clients for the same key share a rate limiter.
	keys.client(testMapKey)
	if len(keys.limiters) != 2 {
		t.Fatalf("expected one limiter per key, got %d", len(keys.limiters))
	}
}

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{
		"division.officer@example.invalid": "d…@example.invalid", "x@example.invalid": "x…@example.invalid", "bad": "…",
	} {
		if got := maskEmail(in); got != want {
			t.Errorf("maskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

// The wizard saves the FIRMS key with PUT and revokes invitations with DELETE.
func TestPreflightAllowsWizardMethods(t *testing.T) {
	handler := allowOrigins(devOrigins)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("a preflight request must not reach the handler")
	}))
	for method, want := range map[string]int{"PUT": 204, "DELETE": 204, "POST": 204, "PATCH": 403} {
		r := httptest.NewRequest(http.MethodOptions, "/api/v1/workspaces/demo/settings/firms", nil)
		r.Header.Set("Origin", "http://127.0.0.1:4200")
		r.Header.Set("Access-Control-Request-Method", method)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want || (want == 204 && w.Header().Get("Access-Control-Allow-Methods") != method) {
			t.Errorf("%s preflight got HTTP %d, methods %q", method, w.Code, w.Header().Get("Access-Control-Allow-Methods"))
		}
		if method == "PUT" && w.Header().Get("Access-Control-Allow-Headers") != "Authorization, Fimra-Device, Content-Type" {
			t.Errorf("PUT preflight must allow Content-Type, got %q", w.Header().Get("Access-Control-Allow-Headers"))
		}
	}
}
