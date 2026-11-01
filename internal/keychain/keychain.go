// Package keychain reads SMTP passwords from the macOS Keychain via the
// `security` command-line tool.
package keychain

import (
	"os/exec"
	"strings"
)

const service = "pingback"

// Source implements app.PasswordSource. The zero value runs the real
// `security` command.
type Source struct {
	// Exec runs a command and returns its stdout; nil means os/exec.
	Exec func(name string, args ...string) ([]byte, error)
}

// Password returns the generic password stored for the profile, if any.
// Entries are looked up by service "pingback" and account <profile>.
func (s Source) Password(profile string) (string, bool) {
	run := s.Exec
	if run == nil {
		run = func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).Output()
		}
	}
	out, err := run("security", "find-generic-password", "-s", service, "-a", profile, "-w")
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(out), "\r\n"), true
}
