package alerts

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// EmailConfig describes the SMTP relay alerts are sent through.
type EmailConfig struct {
	Host     string // SMTP hostname
	Port     string // "587" (STARTTLS), "465" (implicit TLS), "25", …
	Username string // optional; empty skips AUTH
	Password string
	From     string
	To       []string // one or more recipient addresses
	Timeout  time.Duration
}

// EmailNotifier sends each alert as a plain-text email.
type EmailNotifier struct {
	cfg EmailConfig
}

// NewEmail creates an email notifier for the given relay.
func NewEmail(cfg EmailConfig) *EmailNotifier {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &EmailNotifier{cfg: cfg}
}

// Notify delivers one alert as a single email. Relays upgrading to STARTTLS
// are always taken (unless unauthenticated-local), port 465 speaks implicit
// TLS; nothing else is configured — a home miner monitor has no use for
// client certificates.
func (n *EmailNotifier) Notify(ctx context.Context, a Alert) error {
	ctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
	defer cancel()

	addr := net.JoinHostPort(n.cfg.Host, n.cfg.Port)
	var conn net.Conn
	var err error
	if n.cfg.Port == "465" {
		conn, err = (&tls.Dialer{Config: &tls.Config{ServerName: n.cfg.Host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("email: dial %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(n.cfg.Timeout))

	c, err := smtp.NewClient(conn, n.cfg.Host)
	if err != nil {
		return fmt.Errorf("email: greeting from %s: %w", addr, err)
	}
	defer c.Close()

	if n.cfg.Port != "465" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			cfg := &tls.Config{ServerName: n.cfg.Host}
			if err := c.StartTLS(cfg); err != nil {
				return fmt.Errorf("email: STARTTLS: %w", err)
			}
		}
	}

	if n.cfg.Username != "" {
		auth := smtp.PlainAuth("", n.cfg.Username, n.cfg.Password, n.cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("email: auth: %w", err)
		}
	}

	if err := c.Mail(n.cfg.From); err != nil {
		return fmt.Errorf("email: MAIL FROM: %w", err)
	}
	for _, rcpt := range n.cfg.To {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("email: RCPT %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("email: DATA: %w", err)
	}
	if _, err := w.Write([]byte(n.message(a))); err != nil {
		return fmt.Errorf("email: write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email: end message: %w", err)
	}
	// QUIT errors are harmless (some relays hang up first); the message
	// already made it.
	_ = c.Quit()
	return nil
}

// message renders the email: RFC 5322 headers, then the alert line.
func (n *EmailNotifier) message(a Alert) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", n.cfg.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(n.cfg.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", subjectFor(a))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(FormatAlert(a))
	b.WriteString("\r\n")
	return b.String()
}

// subjectFor names who the alert is about at what severity; the body carries
// the detail. (FormatAlert keeps the fuller log line.)
func subjectFor(a Alert) string {
	switch {
	case a.Model != "" && a.MinerIP != "":
		return fmt.Sprintf("[%s] %s (%s)", a.Level, a.Model, a.MinerIP)
	case a.MinerIP != "":
		return fmt.Sprintf("[%s] %s", a.Level, a.MinerIP)
	default:
		return fmt.Sprintf("[%s] homeHash", a.Level)
	}
}
