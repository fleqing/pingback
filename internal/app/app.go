// Package app is the pb command-line entry point.
package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"pingback/internal/mailer"
	"pingback/internal/notify"
	"pingback/internal/runner"
)

type fileConfig struct {
	Profiles map[string]profile `toml:"profiles"`
}

type profile struct {
	Host     string   `toml:"host"`
	Port     int      `toml:"port"`
	Security string   `toml:"security"`
	Username string   `toml:"username"`
	From     string   `toml:"from"`
	To       []string `toml:"to"`
	// RetryDelay is the first SMTP retry backoff, e.g. "1s". Empty means the default.
	RetryDelay string `toml:"retry_delay"`
}

// PasswordSource looks up a stored SMTP password by profile name.
type PasswordSource interface {
	Password(profile string) (string, bool)
}

// Deps holds the external collaborators of the CLI.
type Deps struct {
	// Passwords is consulted when PINGBACK_PASSWORD is not set. May be nil.
	Passwords PasswordSource
}

// Run executes the pb CLI and returns its process exit code.
func Run(args, env []string, stdout, stderr io.Writer) int {
	return RunWith(Deps{}, args, env, stdout, stderr)
}

// RunWith is Run with explicit collaborators.
func RunWith(deps Deps, args, env []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "test" {
		return runTest(deps, args[1:], env, stdout, stderr)
	}

	fs := flag.NewFlagSet("pb", flag.ContinueOnError)
	fs.SetOutput(stderr)
	onFail := fs.Bool("on-fail", false, "only send a notification when the command fails")
	minDuration := fs.Duration("min-duration", 0, "skip the notification for commands that succeed faster than this")
	tailLines := fs.Int("tail", 50, "number of output lines to include in the notification")
	noOutput := fs.Bool("no-output", false, "do not include command output in the notification")
	useShell := fs.Bool("c", false, "treat the command as a string and run it through $SHELL -c")
	toFlag := fs.String("to", "", "comma-separated recipients, overriding the configured ones")
	profileName := fs.String("profile", "", "name of the mail profile to use")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	argv := fs.Args()
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "pb: no command given")
		return 2
	}

	display := strings.Join(argv, " ")
	if *useShell {
		shell := lookup(env, "SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		argv = []string{shell, "-c", display}
	}

	res, err := runner.Run(context.Background(), argv, runner.Options{TailLines: *tailLines, Output: stdout})
	if err != nil {
		fmt.Fprintf(stderr, "pb: %v\n", err)
		return 127
	}

	if err := notifyRun(deps, display, res, env, *profileName, splitList(*toFlag), notify.Rules{OnFailOnly: *onFail, MinDuration: *minDuration, NoOutput: *noOutput}); err != nil {
		fmt.Fprintf(stderr, "pb: warning: notification not sent: %v\n", err)
	}
	return res.ExitCode
}

func notifyRun(deps Deps, command string, res runner.Result, env []string, profileName string, recipients []string, rules notify.Rules) error {
	host, _ := os.Hostname()
	cwd, _ := os.Getwd()
	msg, send := notify.Build(notify.Run{
		Command:   command,
		Host:      host,
		Cwd:       cwd,
		ExitCode:  res.ExitCode,
		Duration:  res.Duration,
		StartedAt: res.StartedAt,
		Tail:      res.Tail,
	}, rules)
	if !send {
		return nil
	}
	_, err := deliver(deps, env, profileName, recipients, msg)
	return err
}

// deliver sends msg using the selected profile and returns the recipients used.
// Recipients default to the profile's "to", then to the sender itself.
func deliver(deps Deps, env []string, profileName string, recipients []string, msg notify.Message) ([]string, error) {
	prof, profName, err := loadProfile(lookup(env, "PINGBACK_CONFIG"), profileName)
	if err != nil {
		return nil, err
	}
	to := recipients
	if len(to) == 0 {
		to = prof.To
	}
	if len(to) == 0 {
		to = []string{prof.From}
	}
	var retryDelay time.Duration
	if prof.RetryDelay != "" {
		d, err := time.ParseDuration(prof.RetryDelay)
		if err != nil {
			return nil, fmt.Errorf("invalid retry_delay %q: %w", prof.RetryDelay, err)
		}
		retryDelay = d
	}
	err = mailer.Send(mailer.Config{
		Host:     prof.Host,
		Port:     prof.Port,
		Security: mailer.Security(prof.Security),
		Username: prof.Username,
		Password: resolvePassword(deps, env, profName),
		From:     prof.From,
		To:       to,

		RetryDelay: retryDelay,
	}, msg.Subject, msg.Body)
	return to, err
}

func loadProfile(path, name string) (profile, string, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return profile{}, "", err
		}
		path = filepath.Join(home, ".config", "pingback", "config.toml")
	}
	var cfg fileConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return profile{}, "", fmt.Errorf("read config: %w", err)
	}
	names := make([]string, 0, len(cfg.Profiles))
	for n := range cfg.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)

	if name == "" {
		switch len(names) {
		case 0:
			return profile{}, "", fmt.Errorf("config defines no profiles")
		case 1:
			name = names[0]
		default:
			return profile{}, "", fmt.Errorf("multiple profiles (%s); choose one with --profile", strings.Join(names, ", "))
		}
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		return profile{}, "", fmt.Errorf("no profile %q (available: %s)", name, strings.Join(names, ", "))
	}
	return p, name, nil
}

func lookup(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// splitList splits a comma-separated list, dropping blanks.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// resolvePassword prefers an explicit PINGBACK_PASSWORD over stored passwords.
func resolvePassword(deps Deps, env []string, profile string) string {
	if p := lookup(env, "PINGBACK_PASSWORD"); p != "" {
		return p
	}
	if deps.Passwords != nil {
		if p, ok := deps.Passwords.Password(profile); ok {
			return p
		}
	}
	return ""
}

// runTest implements `pb test`: send a test mail and report the outcome.
// Unlike a wrapped run, a delivery failure here is the result, so it is an error.
func runTest(deps Deps, args, env []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pb test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	toFlag := fs.String("to", "", "comma-separated recipients, overriding the configured ones")
	profileName := fs.String("profile", "", "name of the mail profile to use")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	host, _ := os.Hostname()
	msg := notify.Message{
		Subject: fmt.Sprintf("[pb test] pingback is configured correctly @%s", host),
		Body:    "This is a test message from pb test.\nIf you can read it, notifications will be delivered.\n",
	}
	to, err := deliver(deps, env, *profileName, splitList(*toFlag), msg)
	if err != nil {
		fmt.Fprintf(stderr, "pb: test mail failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Test mail sent to %s\n", strings.Join(to, ", "))
	return 0
}
