package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Push notifications for the field app (PLAN.md section 5.4).
//
// The default path is UnifiedPush with a push server the agency runs itself,
// such as ntfy. The app gives the API an endpoint URL on that server and the
// API posts each message to it, encrypted for the phone as Web Push
// (RFC 8291), so the push server cannot read it. Google's Firebase Cloud
// Messaging is optional and off unless FIMRA_PUSH_FCM_CREDENTIALS_FILE is set.
//
// A message holds IDs and a short generic text only. The app fetches the
// alert over the signed-in API, so nothing about a fire passes through a
// push server.

// pushPayload is the JSON a phone receives.
type pushPayload struct {
	Version      int    `json:"v"`
	Type         string `json:"type"`
	Notification string `json:"notification,omitempty"`
	Event        string `json:"event,omitempty"`
	Workspace    string `json:"workspace,omitempty"`
	Alert        string `json:"alert,omitempty"`
	Title        string `json:"title,omitempty"`
	Body         string `json:"body,omitempty"`
}

const pushBody = "Open the field app to see it."

func newPushPayload(m pushMessage) pushPayload {
	if m.kind == "device_check" {
		return pushPayload{Version: 1, Type: "device_check"}
	}
	p := pushPayload{Version: 1, Type: "notification", Notification: strconv.FormatInt(m.notificationID, 10),
		Event: m.event, Workspace: m.workspace, Title: notificationTitle(m.event), Body: pushBody}
	if m.alertID != nil {
		p.Alert = strconv.FormatInt(*m.alertID, 10)
	}
	return p
}

// pushTarget is where one message goes.
type pushTarget struct {
	provider, endpoint string
	publicKey, auth    []byte
}

// pushSender delivers one message. The status is delivered, retry, failed
// or gone; gone means the push address no longer exists.
type pushSender interface {
	send(ctx context.Context, target pushTarget, payload []byte) (string, error)
}

// pushSettings holds the configured push paths. Both are off by default.
type pushSettings struct {
	// servers are the origins of the push servers UnifiedPush endpoints may use.
	servers []string
	web     pushSender
	fcm     pushSender
}

func (p pushSettings) enabled() bool { return p.web != nil || p.fcm != nil }

func (p pushSettings) sender(provider string) pushSender {
	switch provider {
	case "unifiedpush":
		return p.web
	case "fcm":
		return p.fcm
	}
	return nil
}

// providers lists the push services this server sends through, for the app.
func (p pushSettings) providers() []string {
	list := []string{}
	if p.web != nil {
		list = append(list, "unifiedpush")
	}
	if p.fcm != nil {
		list = append(list, "fcm")
	}
	return list
}

func readPushSettings() (pushSettings, error) {
	var settings pushSettings
	for raw := range strings.SplitSeq(os.Getenv("FIMRA_PUSH_SERVERS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		origin, err := pushServerOrigin(raw)
		if err != nil {
			return settings, err
		}
		settings.servers = append(settings.servers, origin)
	}
	if len(settings.servers) > 0 {
		settings.web = newWebPushSender()
	}
	if path := os.Getenv("FIMRA_PUSH_FCM_CREDENTIALS_FILE"); path != "" {
		fcm, err := readFCMCredentials(path)
		if err != nil {
			return settings, err
		}
		settings.fcm = fcm
	}
	return settings, nil
}

// pushServerOrigin checks one FIMRA_PUSH_SERVERS entry. Plain HTTP is
// allowed only on a loopback address, for a development push server.
func pushServerOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("FIMRA_PUSH_SERVERS must list push server origins such as https://push.example.org, not %q", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("push server %s must use HTTPS; plain HTTP is only for a loopback address", raw)
		}
	default:
		return "", fmt.Errorf("push server %s must use HTTPS", raw)
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// allowedEndpoint says whether a UnifiedPush endpoint is on a configured
// push server. The API only ever posts to these servers, so a registration
// cannot make it call other addresses.
func (p pushSettings) allowedEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || len(raw) > 4096 || u.Path == "" || u.Path == "/" {
		return false
	}
	origin := u.Scheme + "://" + strings.ToLower(u.Host)
	for _, server := range p.servers {
		if origin == server {
			return true
		}
	}
	return false
}

// webPushSender posts encrypted messages to UnifiedPush endpoints.
type webPushSender struct {
	client *http.Client
}

func newWebPushSender() *webPushSender {
	return &webPushSender{client: &http.Client{
		Timeout: 10 * time.Second,
		// A push server answers directly. Following a redirect could reach an
		// address that is not on the allowed list.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (s *webPushSender) send(ctx context.Context, target pushTarget, payload []byte) (string, error) {
	body, err := encryptWebPush(payload, target.publicKey, target.auth)
	if err != nil {
		return "failed", fmt.Errorf("encrypt push message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.endpoint, bytes.NewReader(body))
	if err != nil {
		return "failed", errors.New("push endpoint is invalid")
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	// A fire alert is urgent, and stale after a day.
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "high")
	res, err := s.client.Do(req)
	if err != nil {
		return "retry", pushNetworkError(err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	res.Body.Close()
	return pushHTTPStatus(res.StatusCode)
}

// pushHTTPStatus maps a push server's answer to a delivery status. Push
// servers can echo addresses, so only the status code is kept.
func pushHTTPStatus(code int) (string, error) {
	switch {
	case code >= 200 && code < 300:
		return "delivered", nil
	case code == http.StatusNotFound || code == http.StatusGone:
		return "gone", fmt.Errorf("push server: reply %d, the address no longer exists", code)
	case code == http.StatusTooManyRequests || code >= 500 || code == http.StatusRequestTimeout:
		return "retry", fmt.Errorf("push server: reply %d", code)
	default:
		return "failed", fmt.Errorf("push server: reply %d", code)
	}
}

func pushNetworkError(err error) error {
	var network *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded):
		return errors.New("push server: connection timed out")
	case errors.As(err, &network):
		return fmt.Errorf("push server: %s failed", network.Op)
	}
	return errors.New("push server could not be reached")
}

// webPushRecordSize is the record size in the aes128gcm header. One record
// holds the whole message.
const webPushRecordSize = 4096

// encryptWebPush encrypts a message for one phone as RFC 8291 describes,
// with the aes128gcm content coding of RFC 8188.
func encryptWebPush(plaintext, phoneKey, authSecret []byte) ([]byte, error) {
	server, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptWebPushWith(plaintext, phoneKey, authSecret, server, salt)
}

// encryptWebPushWith takes the server key and salt, so tests can check the
// example in RFC 8291 appendix A.
func encryptWebPushWith(plaintext, phoneKey, authSecret []byte, server *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(authSecret) != 16 || len(salt) != 16 {
		return nil, errors.New("auth secret and salt must be 16 bytes")
	}
	phone, err := ecdh.P256().NewPublicKey(phoneKey)
	if err != nil {
		return nil, errors.New("phone key is not a P-256 public key")
	}
	if len(plaintext)+1+16 > webPushRecordSize-86 {
		return nil, errors.New("push message is too long")
	}
	shared, err := server.ECDH(phone)
	if err != nil {
		return nil, err
	}
	serverKey := server.PublicKey().Bytes()
	info := "WebPush: info\x00" + string(phoneKey) + string(serverKey)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, info, 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 0, 86)
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, webPushRecordSize)
	header = append(header, byte(len(serverKey)))
	header = append(header, serverKey...)
	// 0x02 marks the last (and only) record, with no padding.
	record := append(append([]byte{}, plaintext...), 2)
	return gcm.Seal(header, nonce, record, nil), nil
}

func marshalPushPayload(p pushPayload) []byte {
	body, _ := json.Marshal(p)
	return body
}
