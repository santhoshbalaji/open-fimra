package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestRequireBearerToken(t *testing.T) {
	key, signer := testSigner(t)
	_, wrongSigner := testSigner(t)
	var jwksRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			issuer := "http://" + r.Host
			writeJSON(w, http.StatusOK, map[string]any{
				"issuer":                                issuer,
				"jwks_uri":                              issuer + "/keys",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/keys":
			jwksRequests.Add(1)
			writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
				Key: &key.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig",
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	const audience = "open-fimra-api"
	provider, err := discoverProvider(t.Context(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var got identity
	handler := newAuthenticator(provider, server.URL, audience).require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Context().Value(identityKey{}).(identity)
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func(authorization string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	now := time.Now()
	valid := jwt.Claims{
		Issuer: server.URL, Subject: "test-subject", Audience: jwt.Audience{"account", audience},
		Expiry: jwt.NewNumericDate(now.Add(time.Minute)),
	}
	validToken := signedToken(t, signer, valid)
	for range 2 {
		if w := request("bearer " + validToken); w.Code != http.StatusNoContent {
			t.Fatalf("valid token returned HTTP %d", w.Code)
		}
		if got != (identity{Subject: "test-subject", Username: "test.officer", Name: "Test Officer"}) {
			t.Fatalf("wrong identity: %+v", got)
		}
	}
	if calls := jwksRequests.Load(); calls != 1 {
		t.Fatalf("JWKS requests = %d, want 1 for two valid tokens", calls)
	}

	t.Run("clock skew", func(t *testing.T) {
		recentlyExpired := valid
		recentlyExpired.Expiry = jwt.NewNumericDate(now.Add(-5 * time.Second))
		slightlyEarly := valid
		slightlyEarly.NotBefore = jwt.NewNumericDate(now.Add(5 * time.Second))
		for _, claims := range []jwt.Claims{recentlyExpired, slightlyEarly} {
			if w := request("Bearer " + signedToken(t, signer, claims)); w.Code != http.StatusNoContent {
				t.Fatalf("token within the clock skew returned HTTP %d", w.Code)
			}
		}
	})

	t.Run("another address of the same provider", func(t *testing.T) {
		const public = "https://fimra.example.org/realms/open-fimra"
		both := newAuthenticator(provider, server.URL, audience, " "+public+" ", "")
		claims := valid
		claims.Issuer = public
		if _, ok := both.verify(t.Context(), signedToken(t, signer, claims)); !ok {
			t.Fatal("token from an accepted extra issuer was refused")
		}
		if _, ok := both.verify(t.Context(), validToken); !ok {
			t.Fatal("token from the main issuer was refused")
		}
		if w := request("Bearer " + signedToken(t, signer, claims)); w.Code != http.StatusUnauthorized {
			t.Fatalf("token from an issuer that was not accepted returned HTTP %d", w.Code)
		}
	})

	for _, tc := range []struct {
		name   string
		change func(*jwt.Claims)
	}{
		{"wrong audience", func(c *jwt.Claims) { c.Audience = jwt.Audience{"other-api"} }},
		{"wrong issuer", func(c *jwt.Claims) { c.Issuer += "/other" }},
		{"expired", func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(now.Add(-time.Minute)) }},
		{"missing expiry", func(c *jwt.Claims) { c.Expiry = nil }},
		{"too early", func(c *jwt.Claims) { c.NotBefore = jwt.NewNumericDate(now.Add(time.Minute)) }},
		{"missing subject", func(c *jwt.Claims) { c.Subject = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := valid
			tc.change(&claims)
			assertUnauthorized(t, request("Bearer "+signedToken(t, signer, claims)), `Bearer error="invalid_token"`)
		})
	}
	t.Run("bad signature", func(t *testing.T) {
		assertUnauthorized(t, request("Bearer "+signedToken(t, wrongSigner, valid)), `Bearer error="invalid_token"`)
	})
	t.Run("malformed token", func(t *testing.T) {
		assertUnauthorized(t, request("Bearer invalid"), `Bearer error="invalid_token"`)
	})
	t.Run("missing token", func(t *testing.T) {
		assertUnauthorized(t, request(""), "Bearer")
	})
}

func testSigner(t *testing.T) (*rsa.PrivateKey, jose.Signer) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		new(jose.SignerOptions).WithHeader(jose.HeaderKey("kid"), "test-key"))
	if err != nil {
		t.Fatal(err)
	}
	return key, signer
}

func signedToken(t *testing.T, signer jose.Signer, claims jwt.Claims) string {
	t.Helper()
	token, err := jwt.Signed(signer).Claims(claims).Claims(map[string]any{
		"preferred_username": "test.officer",
		"name":               "Test Officer",
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func assertUnauthorized(t *testing.T, w *httptest.ResponseRecorder, challenge string) {
	t.Helper()
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != challenge {
		t.Fatalf("got HTTP %d with challenge %q, want 401 with %q", w.Code, w.Header().Get("WWW-Authenticate"), challenge)
	}
	if contentType := w.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("got content type %q, want JSON", contentType)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Fatalf("got invalid JSON error response: %q", w.Body.String())
	}
}
