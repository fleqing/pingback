package mailer

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type Security string

const (
	// SecurityStartTLS upgrades a plaintext connection (typically port 587).
	// It is the default.
	SecurityStartTLS Security = "starttls"
	// SecurityTLS uses implicit TLS from the first byte (typically port 465).
	SecurityTLS Security = "tls"
	// SecurityNone is plaintext; only for connecting to the local machine.
	SecurityNone Security = "none"
)

type Config struct {
	Host     string
	Port     int
	Security Security
	Username string
	Password string
	From     string
	To       []string
	// RootCAs overrides the system roots when verifying the server; intended for tests.
	RootCAs *x509.CertPool
	// RetryDelay is the first backoff delay; it doubles on each retry.
	// Zero means one second.
	RetryDelay time.Duration
}

const maxRetries = 3

func Send(cfg Config, subject, body string) error {
	msg := buildMessage(cfg, subject, body)
	delay := cfg.RetryDelay
	if delay == 0 {
		delay = time.Second
	}
	var err error
	for attempt := 0; ; attempt++ {
		if err = sendOnce(cfg, msg); err == nil {
			return nil
		}
		if attempt == maxRetries || isPermanent(err) {
			return err
		}
		time.Sleep(delay)
		delay *= 2
	}
}

// isPermanent reports whether the server rejected the mail with a 5xx reply,
// which retrying cannot fix (bad credentials, rejected recipient, ...).
// Connection errors and 4xx replies are transient.
func isPermanent(err error) bool {
	var perr *textproto.Error
	return errors.As(err, &perr) && perr.Code >= 500
}

func sendOnce(cfg Config, msg []byte) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tlsCfg := &tls.Config{ServerName: cfg.Host, RootCAs: cfg.RootCAs}

	var conn net.Conn
	var err error
	if cfg.Security == SecurityTLS {
		conn, err = tls.Dial("tcp", addr, tlsCfg)
	} else {
		conn, err = net.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if cfg.Security == "" || cfg.Security == SecurityStartTLS {
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	for _, rcpt := range cfg.To {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func buildMessage(cfg Config, subject, body string) []byte {
	var qp bytes.Buffer
	w := quotedprintable.NewWriter(&qp)
	w.Write([]byte(body))
	w.Close()

	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", cfg.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(cfg.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	b.Write(qp.Bytes())
	return b.Bytes()
}
