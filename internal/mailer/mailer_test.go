package mailer_test

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"pingback/internal/mailer"
)

type received struct {
	from string
	to   []string
	data string
}

// fakeSMTP is a minimal plaintext SMTP server that records delivered mails.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	mail []received
	// failData makes the next N DATA commands fail with a transient 451.
	failData int
	attempts int
	// authUser/authPass, when set, make the server require AUTH PLAIN.
	authUser, authPass string
	authAttempts       int
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *fakeSMTP) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeSMTP) received() []received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]received(nil), s.mail...)
}

func (s *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(line string) { io.WriteString(c, line+"\r\n") }
	say("220 fake ESMTP")
	var cur received
	authed := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			if s.authUser != "" {
				say("250-fake")
				say("250 AUTH PLAIN")
			} else {
				say("250 fake")
			}
		case strings.HasPrefix(cmd, "AUTH PLAIN "):
			s.mu.Lock()
			s.authAttempts++
			s.mu.Unlock()
			raw, _ := base64.StdEncoding.DecodeString(strings.Fields(line)[2])
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 && parts[1] == s.authUser && parts[2] == s.authPass {
				authed = true
				say("235 ok")
			} else {
				say("535 bad credentials")
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			if s.authUser != "" && !authed {
				say("530 authentication required")
				continue
			}
			cur = received{from: extractAddr(line)}
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			cur.to = append(cur.to, extractAddr(line))
			say("250 ok")
		case cmd == "DATA":
			s.mu.Lock()
			s.attempts++
			fail := s.failData > 0
			if fail {
				s.failData--
			}
			s.mu.Unlock()
			if fail {
				say("451 try again later")
				continue
			}
			say("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			cur.data = b.String()
			s.mu.Lock()
			s.mail = append(s.mail, cur)
			s.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func extractAddr(line string) string {
	i, j := strings.Index(line, "<"), strings.Index(line, ">")
	if i < 0 || j < i {
		return ""
	}
	return line[i+1 : j]
}

func TestSend_DeliversSubjectBodyAndRecipients(t *testing.T) {
	srv := newFakeSMTP(t)
	cfg := mailer.Config{
		Host:     "127.0.0.1",
		Security: mailer.SecurityNone,
		Port:     srv.port(),
		From:     "me@example.com",
		To:       []string{"a@example.com", "b@example.com"},
	}

	err := mailer.Send(cfg, "[pb ✓] make build (2m13s) @mbp", "Command: make build\n")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := srv.received()
	if len(got) != 1 {
		t.Fatalf("received %d mails, want 1", len(got))
	}
	if got[0].from != "me@example.com" {
		t.Errorf("from = %q", got[0].from)
	}
	if strings.Join(got[0].to, ",") != "a@example.com,b@example.com" {
		t.Errorf("to = %v", got[0].to)
	}
	msg, err := mail.ReadMessage(strings.NewReader(got[0].data))
	if err != nil {
		t.Fatalf("parse mail: %v", err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "[pb ✓] make build (2m13s) @mbp"; subject != want {
		t.Errorf("subject = %q, want %q", subject, want)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if want := "Command: make build\n"; strings.ReplaceAll(string(body), "\r\n", "\n") != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestSend_RetriesTransientFailureThenSucceeds(t *testing.T) {
	srv := newFakeSMTP(t)
	srv.failData = 2
	cfg := mailer.Config{
		Host:       "127.0.0.1",
		Security:   mailer.SecurityNone,
		Port:       srv.port(),
		From:       "me@example.com",
		To:         []string{"a@example.com"},
		RetryDelay: time.Millisecond,
	}

	if err := mailer.Send(cfg, "subj", "body\n"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := len(srv.received()); got != 1 {
		t.Fatalf("received %d mails, want 1", got)
	}
	if srv.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", srv.attempts)
	}
}

func TestSend_GivesUpAfterThreeRetries(t *testing.T) {
	srv := newFakeSMTP(t)
	srv.failData = 100
	cfg := mailer.Config{
		Host:       "127.0.0.1",
		Security:   mailer.SecurityNone,
		Port:       srv.port(),
		From:       "me@example.com",
		To:         []string{"a@example.com"},
		RetryDelay: time.Millisecond,
	}

	if err := mailer.Send(cfg, "subj", "body\n"); err == nil {
		t.Fatal("Send succeeded, want error")
	}
	if got := len(srv.received()); got != 0 {
		t.Fatalf("received %d mails, want 0", got)
	}
	if srv.attempts != 4 {
		t.Fatalf("attempts = %d, want 4 (1 initial + 3 retries)", srv.attempts)
	}
}

// newFakeTLSSMTP starts an implicit-TLS server requiring AUTH PLAIN and
// returns it with a pool trusting its self-signed certificate.
func newFakeTLSSMTP(t *testing.T, user, pass string) (*fakeSMTP, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)

	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := tls.NewListener(inner, &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	})
	s := &fakeSMTP{ln: ln, authUser: user, authPass: pass}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s, pool
}

func TestSend_ImplicitTLSWithPlainAuth(t *testing.T) {
	srv, pool := newFakeTLSSMTP(t, "me@example.com", "app-password")
	cfg := mailer.Config{
		Host:     "127.0.0.1",
		Port:     srv.port(),
		Security: mailer.SecurityTLS,
		Username: "me@example.com",
		Password: "app-password",
		RootCAs:  pool,
		From:     "me@example.com",
		To:       []string{"a@example.com"},
	}

	if err := mailer.Send(cfg, "subj", "body\n"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := len(srv.received()); got != 1 {
		t.Fatalf("received %d mails, want 1", got)
	}
}

func TestSend_DoesNotRetryPermanentAuthFailure(t *testing.T) {
	srv, pool := newFakeTLSSMTP(t, "me@example.com", "right-password")
	cfg := mailer.Config{
		Host:       "127.0.0.1",
		Port:       srv.port(),
		Security:   mailer.SecurityTLS,
		Username:   "me@example.com",
		Password:   "wrong-password",
		RootCAs:    pool,
		From:       "me@example.com",
		To:         []string{"a@example.com"},
		RetryDelay: time.Millisecond,
	}

	if err := mailer.Send(cfg, "subj", "body\n"); err == nil {
		t.Fatal("Send succeeded, want auth error")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.authAttempts != 1 {
		t.Fatalf("authAttempts = %d, want 1 (no retry on 535)", srv.authAttempts)
	}
}
