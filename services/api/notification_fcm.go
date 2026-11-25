package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// fcmSender sends through Google's Firebase Cloud Messaging HTTP v1 API.
// It is optional: an agency turns it on by pointing
// FIMRA_PUSH_FCM_CREDENTIALS_FILE at a service account key file. Nothing
// in the build or the tests needs a Google account.
type fcmSender struct {
	projectID, clientEmail, tokenURL string
	key                              *rsa.PrivateKey
	// apiURL is the messaging API, replaced in tests.
	apiURL string
	client *http.Client

	mu          sync.Mutex
	accessToken string
	expires     time.Time
}

const (
	fcmScope  = "https://www.googleapis.com/auth/firebase.messaging"
	fcmAPIURL = "https://fcm.googleapis.com"
)

func readFCMCredentials(path string) (*fcmSender, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read FIMRA_PUSH_FCM_CREDENTIALS_FILE: %w", err)
	}
	var file struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, errors.New("FIMRA_PUSH_FCM_CREDENTIALS_FILE must be a service account key file in JSON")
	}
	token, err := url.Parse(file.TokenURI)
	if file.Type != "service_account" || file.ProjectID == "" || file.ClientEmail == "" ||
		err != nil || token.Scheme != "https" || token.Host == "" {
		return nil, errors.New("the FCM credentials file must be a service account key with a project, an email and an HTTPS token address")
	}
	block, _ := pem.Decode([]byte(file.PrivateKey))
	if block == nil {
		return nil, errors.New("the FCM credentials file has no private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("the FCM credentials file has a private key that cannot be read")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the FCM credentials file must have an RSA private key")
	}
	return &fcmSender{projectID: file.ProjectID, clientEmail: file.ClientEmail, tokenURL: file.TokenURI, key: key,
		apiURL: fcmAPIURL, client: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (s *fcmSender) send(ctx context.Context, target pushTarget, payload []byte) (string, error) {
	token, err := s.token(ctx)
	if err != nil {
		return "retry", err
	}
	body, _ := json.Marshal(map[string]any{"message": map[string]any{
		"token": target.endpoint,
		// Data messages only: the app decides what to show.
		"data":    map[string]string{"fimra": string(payload)},
		"android": map[string]string{"priority": "HIGH", "ttl": "86400s"},
	}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.apiURL+"/v1/projects/"+url.PathEscape(s.projectID)+"/messages:send", bytes.NewReader(body))
	if err != nil {
		return "failed", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return "retry", errors.New("FCM could not be reached")
	}
	defer res.Body.Close()
	var reply struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&reply)
	code := reply.Error.Status
	for _, detail := range reply.Error.Details {
		if detail.ErrorCode != "" {
			code = detail.ErrorCode
		}
	}
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return "delivered", nil
	case code == "UNREGISTERED" || res.StatusCode == http.StatusNotFound:
		return "gone", fmt.Errorf("FCM: reply %d %s, the token no longer exists", res.StatusCode, code)
	case res.StatusCode == http.StatusUnauthorized:
		s.mu.Lock()
		s.accessToken = ""
		s.mu.Unlock()
		return "retry", errors.New("FCM: reply 401")
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500:
		return "retry", fmt.Errorf("FCM: reply %d %s", res.StatusCode, code)
	default:
		return "failed", fmt.Errorf("FCM: reply %d %s", res.StatusCode, code)
	}
}

// token returns an OAuth access token from a signed service account
// assertion (RFC 7523), kept until shortly before it expires.
func (s *fcmSender) token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accessToken != "" && time.Now().Before(s.expires.Add(-time.Minute)) {
		return s.accessToken, nil
	}
	now := time.Now()
	segment := func(v any) string {
		body, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(body)
	}
	unsigned := segment(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + segment(map[string]any{
		"iss": s.clientEmail, "scope": fcmScope, "aud": s.tokenURL, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign FCM assertion: %w", err)
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion": {unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := s.client.Do(req)
	if err != nil {
		return "", errors.New("FCM sign-in could not be reached")
	}
	defer res.Body.Close()
	var reply struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&reply) != nil ||
		reply.AccessToken == "" {
		return "", fmt.Errorf("FCM sign-in: reply %d", res.StatusCode)
	}
	s.accessToken, s.expires = reply.AccessToken, now.Add(time.Duration(reply.ExpiresIn)*time.Second)
	return s.accessToken, nil
}
