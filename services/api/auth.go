package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4/jwt"
)

const tokenClockSkew = 30 * time.Second

type identity struct {
	Subject  string
	Username string
	Name     string
	Email    *string
	// VerifiedEmail is the lower-case email claim when the token marks it
	// verified. Invitations are claimed with it, even when email is off.
	VerifiedEmail string
}

type authenticator struct {
	issuer       string
	verifier     *oidc.IDTokenVerifier
	emailEnabled bool
}

func discoverProvider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	return oidc.NewProvider(oidc.ClientContext(ctx, &http.Client{Timeout: 5 * time.Second}), issuer)
}

func newAuthenticator(provider *oidc.Provider, issuer, audience string) authenticator {
	return authenticator{
		issuer: issuer,
		verifier: provider.Verifier(&oidc.Config{
			ClientID: audience,
			// Apply the same clock skew to both time claims below.
			SkipExpiryCheck: true,
		}),
	}
}

type identityKey struct{}

func (a authenticator) require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) == 0 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "A bearer token is required.")
			return
		}

		parts := strings.Fields(headers[0])
		if len(headers) == 1 && len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if caller, ok := a.verify(r.Context(), parts[1]); ok {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, caller)))
				return
			}
		}

		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeError(w, http.StatusUnauthorized, "The bearer token is invalid.")
	})
}

func (a authenticator) verify(ctx context.Context, rawToken string) (identity, bool) {
	token, err := a.verifier.Verify(ctx, rawToken)
	if err != nil || token.Issuer != a.issuer || token.Subject == "" {
		return identity{}, false
	}

	var claims struct {
		Username  string           `json:"preferred_username"`
		Name      string           `json:"name"`
		NotBefore *jwt.NumericDate `json:"nbf"`
	}
	if err := token.Claims(&claims); err != nil {
		return identity{}, false
	}

	now := time.Now()
	if !now.Before(token.Expiry.Add(tokenClockSkew)) ||
		(claims.NotBefore != nil && now.Add(tokenClockSkew).Before(claims.NotBefore.Time())) {
		return identity{}, false
	}

	if claims.Username == "" {
		claims.Username = token.Subject
	}
	if claims.Name == "" {
		claims.Name = claims.Username
	}
	caller := identity{Subject: token.Subject, Username: claims.Username, Name: claims.Name}
	var contact struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
	}
	if token.Claims(&contact) == nil && contact.Verified {
		if email := notificationAddress(contact.Email); email != "" {
			caller.VerifiedEmail = strings.ToLower(email)
			if a.emailEnabled {
				caller.Email = &email
			}
		}
	}
	return caller, true
}
