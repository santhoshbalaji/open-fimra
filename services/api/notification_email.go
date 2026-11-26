package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type smtpChannel struct {
	addr, host, from, username, password, security string
	webURL                                         url.URL
}

func readSMTPConfig() (*smtpChannel, error) {
	if os.Getenv("FIMRA_SMTP_ADDR") == "" {
		return nil, nil
	}
	c := &smtpChannel{
		addr: os.Getenv("FIMRA_SMTP_ADDR"), from: notificationAddress(os.Getenv("FIMRA_SMTP_FROM")),
		username: os.Getenv("FIMRA_SMTP_USERNAME"), password: os.Getenv("FIMRA_SMTP_PASSWORD"),
		security: envOr("FIMRA_SMTP_SECURITY", "starttls"),
	}
	host, port, err := net.SplitHostPort(c.addr)
	if err != nil || host == "" || port == "" || c.from == "" {
		return nil, errors.New("set FIMRA_SMTP_ADDR to a host and port and FIMRA_SMTP_FROM to an email address")
	}
	c.host = host
	switch c.security {
	case "starttls", "tls":
	case "local":
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() || c.username != "" || c.password != "" {
			return nil, errors.New("local SMTP requires a loopback address and no credentials")
		}
	default:
		return nil, errors.New("FIMRA_SMTP_SECURITY must be starttls, tls or local")
	}
	if (c.username == "") != (c.password == "") {
		return nil, errors.New("set both FIMRA_SMTP_USERNAME and FIMRA_SMTP_PASSWORD for SMTP authentication")
	}
	web, err := url.Parse(os.Getenv("FIMRA_WEB_URL"))
	if err != nil || web.Host == "" || web.User != nil || web.Fragment != "" || web.RawQuery != "" ||
		(web.Scheme != "https" && web.Scheme != "http") {
		return nil, errors.New("set FIMRA_WEB_URL to the web app's full HTTP or HTTPS address")
	}
	c.webURL = *web
	return c, nil
}

func notificationAddress(raw string) string {
	if strings.ContainsAny(raw, "\r\n") || len(raw) > 254 {
		return ""
	}
	a, err := mail.ParseAddress(raw)
	if err != nil || a.Address != raw {
		return ""
	}
	// This mailer sends plain ASCII envelopes.
	for _, ch := range raw {
		if ch <= ' ' || ch >= 127 {
			return ""
		}
	}
	return a.Address
}

func notificationTitle(event string) string {
	switch event {
	case "alert_new":
		return "New fire alert"
	case "alert_overdue":
		return "Fire alert overdue"
	case "incident_overdue":
		return "Incident response overdue"
	case "incident_escalated":
		return "Incident response escalated"
	default:
		return "Feed needs attention"
	}
}

func (c *smtpChannel) deliver(ctx context.Context, m notificationMessage) (string, error) {
	if notificationAddress(*m.address) == "" {
		return "failed", errors.New("email address is invalid")
	}
	return c.send(ctx, *m.address, c.email(m))
}

func (c *smtpChannel) sendInvitation(ctx context.Context, m invitationMessage) (string, error) {
	if notificationAddress(m.address) == "" {
		return "failed", errors.New("email address is invalid")
	}
	return c.send(ctx, m.address, c.invitationEmail(m))
}

// send delivers one message. Its status says whether a retry is safe.
func (c *smtpChannel) send(ctx context.Context, to, message string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return smtpFailure("connect", err, false)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return smtpFailure("set connection deadline", err, false)
	}
	// Cancellation also interrupts network I/O after the connection is open.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	tlsConfig := &tls.Config{ServerName: c.host, MinVersion: tls.VersionTLS12}
	var transport net.Conn = conn
	if c.security == "tls" {
		transport = tls.Client(conn, tlsConfig)
	}
	client, err := smtp.NewClient(transport, c.host)
	if err != nil {
		return smtpFailure("greeting", err, false)
	}
	defer client.Close()
	if c.security == "starttls" {
		if err := client.StartTLS(tlsConfig); err != nil {
			return smtpFailure("start TLS", err, false)
		}
	}
	if c.username != "" {
		if err := client.Auth(smtp.PlainAuth("", c.username, c.password, c.host)); err != nil {
			return smtpFailure("authenticate", err, false)
		}
	}
	if err := client.Mail(c.from); err != nil {
		return smtpFailure("set sender", err, false)
	}
	if err := client.Rcpt(to); err != nil {
		return smtpFailure("set recipient", err, false)
	}
	writer, err := client.Data()
	if err != nil {
		return smtpFailure("start message", err, false)
	}
	if _, err := writer.Write([]byte(message)); err != nil {
		return smtpFailure("write message", err, false)
	}
	if err := writer.Close(); err != nil {
		return smtpFailure("confirm message", err, true)
	}
	return "delivered", nil
}

func (c *smtpChannel) email(m notificationMessage) string {
	link := c.webURL
	q := link.Query()
	q.Set("workspace", m.workspace)
	if m.alertID != nil {
		q.Set("alert", strconv.FormatInt(*m.alertID, 10))
	} else {
		q.Set("view", "feed-health")
	}
	link.RawQuery = q.Encode()
	title := notificationTitle(m.event)
	domain := c.from[strings.LastIndex(c.from, "@")+1:]
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n"+
		"Message-ID: <fimra-%s-%d@%s>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s.\r\n\r\nOpen FIMRA: %s\r\n",
		c.from, *m.address, title, time.Now().UTC().Format(time.RFC1123Z), m.workspaceID, m.id, domain, title, link.String())
}

// invitationEmail asks an officer to sign in. It holds no secret: the grant is
// claimed only by a sign-in whose verified email matches the invitation.
func (c *smtpChannel) invitationEmail(m invitationMessage) string {
	domain := c.from[strings.LastIndex(c.from, "@")+1:]
	header := strings.NewReplacer("\r", " ", "\n", " ")
	body := fmt.Sprintf("You are invited to the %s workspace in Open FIMRA as %s for %s.\r\n\r\n"+
		"Sign in at %s with this email address before %s. Your account must show this address as verified.\r\n\r\n"+
		"If you did not expect this invitation, you can ignore this email.\r\n",
		header.Replace(m.workspace), strings.ToLower(m.roleLabel), header.Replace(m.unitName), c.webURL.String(),
		m.expiresAt.UTC().Format("2 January 2006 15:04 MST"))
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: You are invited to Open FIMRA\r\nDate: %s\r\n"+
		"Message-ID: <fimra-invitation-%d@%s>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		c.from, m.address, time.Now().UTC().Format(time.RFC1123Z), m.id, domain, body)
}

// SMTP replies can echo addresses or credentials. Keep the step and reply
// code (or transport error type) without recording the server's reply text.
func smtpFailure(step string, err error, awaitingAcceptance bool) (string, error) {
	var reply *textproto.Error
	if errors.As(err, &reply) && reply.Code >= 400 && reply.Code < 600 {
		status := "retry"
		if reply.Code >= 500 {
			status = "failed"
		}
		return status, fmt.Errorf("SMTP %s: reply %d", step, reply.Code)
	}
	status := "retry"
	if awaitingAcceptance {
		status = "uncertain"
	}
	var network *net.OpError
	if errors.As(err, &network) {
		err = network.Err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return status, fmt.Errorf("SMTP %s: connection timed out", step)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return status, fmt.Errorf("SMTP %s: server closed the connection", step)
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return status, fmt.Errorf("SMTP %s: %w", step, errno)
	}
	return status, fmt.Errorf("SMTP %s: %T", step, err)
}
