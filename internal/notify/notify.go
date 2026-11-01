package notify

import (
	"fmt"
	"regexp"
	"time"
)

// Run describes a finished Run, as input to building a Notification.
type Run struct {
	Command   string
	Host      string
	ExitCode  int
	Duration  time.Duration
	Cwd       string
	StartedAt time.Time
	Tail      string
}

type Rules struct {
	OnFailOnly  bool
	MinDuration time.Duration
	NoOutput    bool
}

type Message struct {
	Subject string
	Body    string
}

func Build(r Run, rules Rules) (Message, bool) {
	status := "✓"
	if r.ExitCode != 0 {
		status = fmt.Sprintf("✗ %d", r.ExitCode)
	}
	subject := fmt.Sprintf("[pb %s] %s (%s) @%s", status, truncate(r.Command, maxCommandLen), r.Duration.Round(time.Second), r.Host)
	failed := r.ExitCode != 0
	send := true
	if !failed && (rules.OnFailOnly || r.Duration < rules.MinDuration) {
		send = false
	}
	tail := redact(r.Tail)
	if rules.NoOutput {
		tail = ""
	}
	body := fmt.Sprintf("Command: %s\nExit code: %d\nHost: %s\nDirectory: %s\nStarted: %s\nDuration: %s\n\n%s",
		r.Command, r.ExitCode, r.Host, r.Cwd,
		r.StartedAt.Format("2006-01-02 15:04:05 MST"), r.Duration.Round(time.Second), tail)
	return Message{Subject: subject, Body: body}, send
}

const maxCommandLen = 60

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

var redactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(?i)((?:password|passwd|secret|token|api[_-]?key)\s*[=:]\s*)\S+`), "${1}[REDACTED]"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "[REDACTED]"},
}

func redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
