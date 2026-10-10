package alerts

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal SMTP server capturing one message. It speaks enough
// of the protocol (EHLO, AUTH PLAIN if credentials arrive, MAIL, RCPT, DATA,
// QUIT) for net/smtp's SendMail-style flow over plaintext.
type fakeSMTP struct {
	addr string

	mu       sync.Mutex
	ehlos    int
	authLine string
	from     string
	rcpts    []string
	data     string
	quit     bool
	failRCPT bool
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	f := &fakeSMTP{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f.addr = ln.Addr().String()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	conn.Write([]byte("220 test ESMTP\r\n"))
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		cmdUpper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmdUpper, "EHLO"):
			f.mu.Lock()
			f.ehlos++
			f.mu.Unlock()
			// Advertise AUTH PLAIN, no STARTTLS: test server is plain.
			conn.Write([]byte("250-test greets you\r\n250 AUTH PLAIN\r\n"))
		case strings.HasPrefix(cmdUpper, "HELO"):
			f.mu.Lock()
			f.ehlos++
			f.mu.Unlock()
			conn.Write([]byte("250 test greets you\r\n"))
		case strings.HasPrefix(cmdUpper, "AUTH "):
			f.mu.Lock()
			f.authLine = line
			f.mu.Unlock()
			conn.Write([]byte("235 ok\r\n"))
		case strings.HasPrefix(cmdUpper, "MAIL FROM:"):
			f.mu.Lock()
			f.from = line[strings.Index(line, ":")+1:]
			f.mu.Unlock()
			conn.Write([]byte("250 ok\r\n"))
		case strings.HasPrefix(cmdUpper, "RCPT TO:"):
			f.mu.Lock()
			if f.failRCPT {
				f.mu.Unlock()
				conn.Write([]byte("550 no such user\r\n"))
				continue
			}
			f.rcpts = append(f.rcpts, line[strings.Index(line, ":")+1:])
			f.mu.Unlock()
			conn.Write([]byte("250 ok\r\n"))
		case strings.HasPrefix(cmdUpper, "DATA"):
			conn.Write([]byte("354 go ahead\r\n"))
			var sb strings.Builder
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimSpace(dl) == "." {
					break
				}
				sb.WriteString(dl)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			conn.Write([]byte("250 accepted\r\n"))
		case strings.HasPrefix(cmdUpper, "QUIT"):
			f.mu.Lock()
			f.quit = true
			f.mu.Unlock()
			conn.Write([]byte("221 bye\r\n"))
			return
		case cmdUpper == "RSET" || cmdUpper == "NOOP":
			conn.Write([]byte("250 ok\r\n"))
		default:
			conn.Write([]byte("502 not implemented\r\n"))
		}
	}
}

func (f *fakeSMTP) snapshot() (from string, rcpts []string, data string, auth string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.from, append([]string(nil), f.rcpts...), f.data, f.authLine
}

func alertFixture() Alert {
	return Alert{
		Kind: KindTemperature, Level: LevelCritical,
		MinerID: "m1", MinerIP: "192.168.1.10", Model: "Gamma 601",
		Message: "temperature 90.0°C above 85°C threshold",
	}
}

func TestEmailMessageFormat(t *testing.T) {
	en := NewEmail(EmailConfig{From: "hash@home.local", To: []string{"greg@home.local"}})
	msg := en.message(alertFixture())

	for _, want := range []string{
		"From: hash@home.local",
		"To: greg@home.local",
		"Subject: [critical] Gamma 601 (192.168.1.10)",
		"Date: ",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		FormatAlert(alertFixture()),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}

func TestEmailMessageMultipleRecipients(t *testing.T) {
	en := NewEmail(EmailConfig{From: "hash@home.local", To: []string{"a@x", "b@y"}})
	msg := en.message(alertFixture())
	if !strings.Contains(msg, "To: a@x, b@y") {
		t.Errorf("message missing both recipients:\n%s", msg)
	}
}

func TestEmailDeliversOverPlaintextLocalhost(t *testing.T) {
	f := newFakeSMTP(t)
	en := NewEmail(EmailConfig{
		Host: hostOf(f.addr), Port: portOf(f.addr),
		From: "hash@home.local", To: []string{"greg@home.local"},
		Username: "hash", Password: "pw",
		Timeout: 2 * time.Second,
	})
	if err := en.Notify(context.Background(), alertFixture()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	from, rcpts, data, auth := f.snapshot()
	from = strings.Trim(from, "<>")
	for i, r := range rcpts {
		rcpts[i] = strings.Trim(r, "<>")
	}
	if from != "hash@home.local" {
		t.Errorf("MAIL FROM = %q", from)
	}
	if len(rcpts) != 1 || rcpts[0] != "greg@home.local" {
		t.Errorf("RCPT TO = %q", rcpts)
	}
	if !strings.Contains(data, FormatAlert(alertFixture())) {
		t.Errorf("DATA missing alert body:\n%s", data)
	}
	if !strings.Contains(auth, "AUTH PLAIN") {
		t.Errorf("no AUTH observed: %q", auth)
	}
}

func TestEmailDeliversToMultipleRecipients(t *testing.T) {
	f := newFakeSMTP(t)
	en := NewEmail(EmailConfig{
		Host: hostOf(f.addr), Port: portOf(f.addr),
		From: "hash@home.local", To: []string{"a@x", "b@y", "c@z"},
		Timeout: 2 * time.Second,
	})
	if err := en.Notify(context.Background(), alertFixture()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	_, rcpts, _, _ := f.snapshot()
	if len(rcpts) != 3 {
		t.Fatalf("RCPT TO = %q, want 3", rcpts)
	}
}

func TestEmailReturnsErrorOnRejectedRecipient(t *testing.T) {
	f := newFakeSMTP(t)
	f.mu.Lock()
	f.failRCPT = true
	f.mu.Unlock()
	en := NewEmail(EmailConfig{
		Host: hostOf(f.addr), Port: portOf(f.addr),
		From: "hash@home.local", To: []string{"nobody@x"},
		Timeout: 2 * time.Second,
	})
	if err := en.Notify(context.Background(), alertFixture()); err == nil {
		t.Fatal("Notify succeeded with 550 recipient, want error")
	}
}

func TestEmailFailsOnUnreachableHost(t *testing.T) {
	en := NewEmail(EmailConfig{
		Host: "127.0.0.1", Port: "1", // nothing listens here
		From: "hash@home.local", To: []string{"greg@home.local"},
		Timeout: 500 * time.Millisecond,
	})
	if err := en.Notify(context.Background(), alertFixture()); err == nil {
		t.Fatal("Notify succeeded on a closed port, want error")
	}
}

func hostOf(addr string) string {
	h, _, _ := net.SplitHostPort(addr)
	return h
}

func portOf(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	return p
}
