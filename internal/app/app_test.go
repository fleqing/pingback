package app_test

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pingback/internal/app"
	"pingback/internal/smtptest"
)

func writeConfig(t *testing.T, port int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := fmt.Sprintf(`
[profiles.main]
host = "127.0.0.1"
port = %d
security = "none"
retry_delay = "1ms"
from = "me@example.com"
to = ["me@example.com"]
`, port)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun_FailedCommandSendsMailAndPassesThroughExitCode(t *testing.T) {
	srv := smtptest.New(t)
	env := []string{"PINGBACK_CONFIG=" + writeConfig(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	code := app.Run([]string{"--", "sh", "-c", "exit 3"}, env, &stdout, &stderr)

	if code != 3 {
		t.Fatalf("exit code = %d, want 3\nstderr: %s", code, stderr.String())
	}
	got := srv.Received()
	if len(got) != 1 {
		t.Fatalf("received %d mails, want 1\nstderr: %s", len(got), stderr.String())
	}
	msg, err := mail.ReadMessage(strings.NewReader(got[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if !strings.HasPrefix(subject, "[pb ✗ 3] sh -c exit 3") {
		t.Fatalf("subject = %q", subject)
	}
}

func TestRun_OnFailFlagSkipsMailForSuccessfulCommand(t *testing.T) {
	srv := smtptest.New(t)
	env := []string{"PINGBACK_CONFIG=" + writeConfig(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	code := app.Run([]string{"--on-fail", "--", "sh", "-c", "exit 0"}, env, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if got := len(srv.Received()); got != 0 {
		t.Fatalf("received %d mails for successful command, want 0", got)
	}

	code = app.Run([]string{"--on-fail", "--", "sh", "-c", "exit 1"}, env, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if got := len(srv.Received()); got != 1 {
		t.Fatalf("received %d mails for failed command, want 1", got)
	}
}

func TestRun_MinDurationFlagSkipsFastSuccessButNotFastFailure(t *testing.T) {
	srv := smtptest.New(t)
	env := []string{"PINGBACK_CONFIG=" + writeConfig(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	code := app.Run([]string{"--min-duration", "10s", "--", "sh", "-c", "exit 0"}, env, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if got := len(srv.Received()); got != 0 {
		t.Fatalf("received %d mails for fast success, want 0", got)
	}

	code = app.Run([]string{"--min-duration", "10s", "--", "sh", "-c", "exit 1"}, env, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if got := len(srv.Received()); got != 1 {
		t.Fatalf("received %d mails for fast failure, want 1", got)
	}
}

func bodyOf(t *testing.T, data string) string {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func TestRun_TailFlagLimitsOutputLinesInBody(t *testing.T) {
	srv := smtptest.New(t)
	env := []string{"PINGBACK_CONFIG=" + writeConfig(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	app.Run([]string{"--tail", "2", "--", "sh", "-c", "echo one; echo two; echo three"}, env, &stdout, &stderr)

	got := srv.Received()
	if len(got) != 1 {
		t.Fatalf("received %d mails, want 1\nstderr: %s", len(got), stderr.String())
	}
	body := bodyOf(t, got[0].Data)
	if strings.Contains(body, "\none\n") || !strings.HasSuffix(body, "\ntwo\nthree\n") {
		t.Fatalf("body should keep only last 2 lines:\n%s", body)
	}
}

func TestRun_NoOutputFlagOmitsOutputFromBody(t *testing.T) {
	srv := smtptest.New(t)
	env := []string{"PINGBACK_CONFIG=" + writeConfig(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	app.Run([]string{"--no-output", "--", "sh", "-c", "printf 'sensitive-%s\\n' line"}, env, &stdout, &stderr)

	got := srv.Received()
	if len(got) != 1 {
		t.Fatalf("received %d mails, want 1\nstderr: %s", len(got), stderr.String())
	}
	if body := bodyOf(t, got[0].Data); strings.Contains(body, "sensitive-line") {
		t.Fatalf("body contains output despite --no-output:\n%s", body)
	}
	if !strings.Contains(stdout.String(), "sensitive-line") {
		t.Fatalf("terminal output should still show the line, got %q", stdout.String())
	}
}

func TestRun_UnreachableSMTPWarnsButKeepsCommandExitCode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens here any more: connections are refused
	env := []string{"PINGBACK_CONFIG=" + writeConfig(t, port)}
	var stdout, stderr bytes.Buffer

	code := app.Run([]string{"--", "sh", "-c", "exit 5"}, env, &stdout, &stderr)

	if code != 5 {
		t.Fatalf("exit code = %d, want 5 (SMTP failure must not change it)", code)
	}
	if !strings.Contains(stderr.String(), "notification not sent") {
		t.Fatalf("stderr should warn about the unsent notification, got %q", stderr.String())
	}
}

func TestRun_CFlagRunsCommandStringThroughShell(t *testing.T) {
	srv := smtptest.New(t)
	env := []string{"PINGBACK_CONFIG=" + writeConfig(t, srv.Port()), "SHELL=/bin/sh"}
	var stdout, stderr bytes.Buffer

	code := app.Run([]string{"-c", "printf x | tr x y; exit 6"}, env, &stdout, &stderr)

	if code != 6 {
		t.Fatalf("exit code = %d, want 6\nstderr: %s", code, stderr.String())
	}
	if stdout.String() != "y" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "y")
	}
	got := srv.Received()
	if len(got) != 1 {
		t.Fatalf("received %d mails, want 1", len(got))
	}
	msg, _ := mail.ReadMessage(strings.NewReader(got[0].Data))
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if want := "[pb ✗ 6] printf x | tr x y; exit 6 "; !strings.HasPrefix(subject, want) {
		t.Fatalf("subject = %q, want prefix %q", subject, want)
	}
}

func TestRun_ToFlagOverridesConfiguredRecipients(t *testing.T) {
	srv := smtptest.New(t)
	env := []string{"PINGBACK_CONFIG=" + writeConfig(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	app.Run([]string{"--to", "a@example.com,b@example.com", "--", "sh", "-c", "exit 0"}, env, &stdout, &stderr)

	got := srv.Received()
	if len(got) != 1 {
		t.Fatalf("received %d mails, want 1\nstderr: %s", len(got), stderr.String())
	}
	if want := "a@example.com,b@example.com"; strings.Join(got[0].To, ",") != want {
		t.Fatalf("recipients = %v, want %s (config's me@example.com must be overridden)", got[0].To, want)
	}
}

// writeTwoProfiles configures "work" (live server) and "personal" (dead port).
func writeTwoProfiles(t *testing.T, livePort int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := fmt.Sprintf(`
[profiles.work]
host = "127.0.0.1"
port = %d
security = "none"
retry_delay = "1ms"
from = "work@example.com"

[profiles.personal]
host = "127.0.0.1"
port = 1
security = "none"
retry_delay = "1ms"
from = "me@example.com"
`, livePort)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun_ProfileFlagSelectsAmongMultipleProfiles(t *testing.T) {
	srv := smtptest.New(t)
	env := []string{"PINGBACK_CONFIG=" + writeTwoProfiles(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	code := app.Run([]string{"--profile", "work", "--", "sh", "-c", "exit 0"}, env, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	got := srv.Received()
	if len(got) != 1 || got[0].From != "work@example.com" {
		t.Fatalf("want one mail from work@example.com, got %+v\nstderr: %s", got, stderr.String())
	}
}

func TestRun_AmbiguousProfileWarnsWithNamesAndKeepsExitCode(t *testing.T) {
	srv := smtptest.New(t)
	env := []string{"PINGBACK_CONFIG=" + writeTwoProfiles(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	code := app.Run([]string{"--", "sh", "-c", "exit 7"}, env, &stdout, &stderr)

	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	for _, name := range []string{"work", "personal", "--profile"} {
		if !strings.Contains(stderr.String(), name) {
			t.Errorf("stderr should mention %q, got %q", name, stderr.String())
		}
	}
	if got := len(srv.Received()); got != 0 {
		t.Fatalf("received %d mails, want 0", got)
	}
}

func TestRun_NoRecipientsFallsBackToSender(t *testing.T) {
	srv := smtptest.New(t)
	// the "work" profile configures no "to"
	env := []string{"PINGBACK_CONFIG=" + writeTwoProfiles(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	app.Run([]string{"--profile", "work", "--", "sh", "-c", "exit 0"}, env, &stdout, &stderr)

	got := srv.Received()
	if len(got) != 1 {
		t.Fatalf("received %d mails, want 1\nstderr: %s", len(got), stderr.String())
	}
	if want := "work@example.com"; strings.Join(got[0].To, ",") != want {
		t.Fatalf("recipients = %v, want [%s] (sender itself)", got[0].To, want)
	}
}

func TestRun_UnknownProfileWarnsWithAvailableNames(t *testing.T) {
	srv := smtptest.New(t)
	env := []string{"PINGBACK_CONFIG=" + writeTwoProfiles(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	code := app.Run([]string{"--profile", "nope", "--", "sh", "-c", "exit 8"}, env, &stdout, &stderr)

	if code != 8 {
		t.Fatalf("exit code = %d, want 8", code)
	}
	for _, want := range []string{`"nope"`, "personal", "work"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr should mention %s, got %q", want, stderr.String())
		}
	}
	if got := len(srv.Received()); got != 0 {
		t.Fatalf("received %d mails, want 0", got)
	}
}

// fakePasswords is an in-memory PasswordSource.
type fakePasswords map[string]string

func (f fakePasswords) Password(profile string) (string, bool) {
	p, ok := f[profile]
	return p, ok
}

func writeAuthConfig(t *testing.T, port int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := fmt.Sprintf(`
[profiles.main]
host = "127.0.0.1"
port = %d
security = "none"
retry_delay = "1ms"
username = "me@example.com"
from = "me@example.com"
`, port)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun_PasswordComesFromPasswordSource(t *testing.T) {
	srv := smtptest.NewWithAuth(t, "me@example.com", "keychain-pass")
	env := []string{"PINGBACK_CONFIG=" + writeAuthConfig(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	code := app.RunWith(app.Deps{Passwords: fakePasswords{"main": "keychain-pass"}},
		[]string{"--", "sh", "-c", "exit 0"}, env, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := len(srv.Received()); got != 1 {
		t.Fatalf("received %d mails, want 1\nstderr: %s", got, stderr.String())
	}
}

func TestRun_EnvPasswordOverridesPasswordSource(t *testing.T) {
	srv := smtptest.NewWithAuth(t, "me@example.com", "env-pass")
	env := []string{
		"PINGBACK_CONFIG=" + writeAuthConfig(t, srv.Port()),
		"PINGBACK_PASSWORD=env-pass",
	}
	var stdout, stderr bytes.Buffer

	app.RunWith(app.Deps{Passwords: fakePasswords{"main": "stale-keychain-pass"}},
		[]string{"--", "sh", "-c", "exit 0"}, env, &stdout, &stderr)

	if got := len(srv.Received()); got != 1 {
		t.Fatalf("received %d mails, want 1 (env must win)\nstderr: %s", got, stderr.String())
	}
}

func TestRun_MissingPasswordWarnsAndKeepsExitCode(t *testing.T) {
	srv := smtptest.NewWithAuth(t, "me@example.com", "whatever")
	env := []string{"PINGBACK_CONFIG=" + writeAuthConfig(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	code := app.RunWith(app.Deps{Passwords: fakePasswords{}},
		[]string{"--", "sh", "-c", "exit 9"}, env, &stdout, &stderr)

	if code != 9 {
		t.Fatalf("exit code = %d, want 9", code)
	}
	if !strings.Contains(stderr.String(), "notification not sent") {
		t.Fatalf("stderr should warn, got %q", stderr.String())
	}
	if got := len(srv.Received()); got != 0 {
		t.Fatalf("received %d mails, want 0", got)
	}
}

func TestTest_SendsTestMailUsingSelectedProfile(t *testing.T) {
	srv := smtptest.New(t)
	env := []string{"PINGBACK_CONFIG=" + writeTwoProfiles(t, srv.Port())}
	var stdout, stderr bytes.Buffer

	code := app.Run([]string{"test", "--profile", "work", "--to", "dest@example.com"}, env, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	got := srv.Received()
	if len(got) != 1 {
		t.Fatalf("received %d mails, want 1", len(got))
	}
	if got[0].From != "work@example.com" || strings.Join(got[0].To, ",") != "dest@example.com" {
		t.Fatalf("from/to = %q -> %v", got[0].From, got[0].To)
	}
	msg, _ := mail.ReadMessage(strings.NewReader(got[0].Data))
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if !strings.HasPrefix(subject, "[pb test]") {
		t.Fatalf("subject = %q, want prefix [pb test]", subject)
	}
	if !strings.Contains(stdout.String(), "dest@example.com") {
		t.Fatalf("stdout should confirm the recipient, got %q", stdout.String())
	}
}

func TestTest_ReportsDeliveryFailureWithNonZeroExit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	env := []string{"PINGBACK_CONFIG=" + writeConfig(t, port)}
	var stdout, stderr bytes.Buffer

	code := app.Run([]string{"test"}, env, &stdout, &stderr)

	if code == 0 {
		t.Fatal("exit code = 0, want non-zero when the test mail cannot be delivered")
	}
	if !strings.Contains(stderr.String(), "test mail failed") {
		t.Fatalf("stderr should explain the failure, got %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "sent") {
		t.Fatalf("stdout must not claim success, got %q", stdout.String())
	}
}
