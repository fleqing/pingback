// Package smtptest provides a minimal plaintext SMTP server for tests.
package smtptest

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
)

type Mail struct {
	From string
	To   []string
	Data string
}

type Server struct {
	ln   net.Listener
	mu   sync.Mutex
	mail []Mail

	user, pass string // when user is set, AUTH PLAIN is required
}

// New starts a server that accepts mail without authentication.
func New(t *testing.T) *Server { return start(t, "", "") }

// NewWithAuth starts a server that requires AUTH PLAIN with the given credentials.
func NewWithAuth(t *testing.T, user, pass string) *Server { return start(t, user, pass) }

func start(t *testing.T, user, pass string) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{ln: ln, user: user, pass: pass}
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

func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *Server) Received() []Mail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Mail(nil), s.mail...)
}

func (s *Server) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(line string) { io.WriteString(c, line+"\r\n") }
	say("220 fake ESMTP")
	var cur Mail
	authed := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			if s.user != "" {
				say("250-fake")
				say("250 AUTH PLAIN")
			} else {
				say("250 fake")
			}
		case strings.HasPrefix(cmd, "AUTH PLAIN "):
			raw, _ := base64.StdEncoding.DecodeString(strings.Fields(line)[2])
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 && parts[1] == s.user && parts[2] == s.pass {
				authed = true
				say("235 ok")
			} else {
				say("535 bad credentials")
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			if s.user != "" && !authed {
				say("530 authentication required")
				continue
			}
			cur = Mail{From: addr(line)}
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			cur.To = append(cur.To, addr(line))
			say("250 ok")
		case cmd == "DATA":
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
			cur.Data = b.String()
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

func addr(line string) string {
	i, j := strings.Index(line, "<"), strings.Index(line, ">")
	if i < 0 || j < i {
		return ""
	}
	return line[i+1 : j]
}
