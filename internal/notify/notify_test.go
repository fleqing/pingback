package notify_test

import (
	"strings"
	"testing"
	"time"

	"pingback/internal/notify"
)

func TestBuild_SuccessSubject(t *testing.T) {
	run := notify.Run{
		Command:  "make build",
		Host:     "mbp",
		ExitCode: 0,
		Duration: 2*time.Minute + 13*time.Second,
	}
	msg, send := notify.Build(run, notify.Rules{})
	if !send {
		t.Fatal("send = false, want true")
	}
	if want := "[pb ✓] make build (2m13s) @mbp"; msg.Subject != want {
		t.Fatalf("Subject = %q, want %q", msg.Subject, want)
	}
}

func TestBuild_FailureSubjectIncludesExitCode(t *testing.T) {
	run := notify.Run{
		Command:  "make build",
		Host:     "mbp",
		ExitCode: 2,
		Duration: 2*time.Minute + 13*time.Second,
	}
	msg, _ := notify.Build(run, notify.Rules{})
	if want := "[pb ✗ 2] make build (2m13s) @mbp"; msg.Subject != want {
		t.Fatalf("Subject = %q, want %q", msg.Subject, want)
	}
}

func TestBuild_LongCommandTruncatedTo60Chars(t *testing.T) {
	run := notify.Run{
		Command:  strings.Repeat("a", 100),
		Host:     "mbp",
		Duration: time.Second,
	}
	msg, _ := notify.Build(run, notify.Rules{})
	want := "[pb ✓] " + strings.Repeat("a", 59) + "… (1s) @mbp"
	if msg.Subject != want {
		t.Fatalf("Subject = %q, want %q", msg.Subject, want)
	}
}

func TestBuild_OnFailOnlySkipsSuccessButSendsFailure(t *testing.T) {
	rules := notify.Rules{OnFailOnly: true}

	_, send := notify.Build(notify.Run{Command: "ok", ExitCode: 0}, rules)
	if send {
		t.Fatal("success run: send = true, want false")
	}

	_, send = notify.Build(notify.Run{Command: "bad", ExitCode: 1}, rules)
	if !send {
		t.Fatal("failed run: send = false, want true")
	}
}

func TestBuild_MinDurationSkipsFastSuccessButNotFailureOrSlowRun(t *testing.T) {
	rules := notify.Rules{MinDuration: 10 * time.Second}

	_, send := notify.Build(notify.Run{ExitCode: 0, Duration: 2 * time.Second}, rules)
	if send {
		t.Fatal("fast success: send = true, want false")
	}

	_, send = notify.Build(notify.Run{ExitCode: 1, Duration: 2 * time.Second}, rules)
	if !send {
		t.Fatal("fast failure: send = false, want true")
	}

	_, send = notify.Build(notify.Run{ExitCode: 0, Duration: 30 * time.Second}, rules)
	if !send {
		t.Fatal("slow success: send = false, want true")
	}
}

func TestBuild_BodyContainsMetadataAndTail(t *testing.T) {
	run := notify.Run{
		Command:   "make build",
		Host:      "mbp",
		Cwd:       "/work/app",
		ExitCode:  2,
		StartedAt: time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC),
		Duration:  2*time.Minute + 13*time.Second,
		Tail:      "compiling...\nerror: boom\n",
	}
	msg, _ := notify.Build(run, notify.Rules{})
	for _, want := range []string{
		"Command: make build",
		"Exit code: 2",
		"Host: mbp",
		"Directory: /work/app",
		"Started: 2026-11-01 09:30:00 UTC",
		"Duration: 2m13s",
		"compiling...\nerror: boom\n",
	} {
		if !strings.Contains(msg.Body, want) {
			t.Errorf("Body missing %q\nBody:\n%s", want, msg.Body)
		}
	}
}

func TestBuild_RedactsSecretsInTail(t *testing.T) {
	run := notify.Run{
		Command: "deploy",
		Tail: "Authorization: Bearer abc123.def-456\n" +
			"connecting with password=hunter2 now\n" +
			"key AKIAIOSFODNN7EXAMPLE used\n" +
			"build finished ok\n",
	}
	msg, _ := notify.Build(run, notify.Rules{})
	for _, secret := range []string{"abc123.def-456", "hunter2", "AKIAIOSFODNN7EXAMPLE"} {
		if strings.Contains(msg.Body, secret) {
			t.Errorf("Body leaks %q\nBody:\n%s", secret, msg.Body)
		}
	}
	for _, keep := range []string{"Authorization: Bearer [REDACTED]", "password=[REDACTED] now", "build finished ok"} {
		if !strings.Contains(msg.Body, keep) {
			t.Errorf("Body missing %q\nBody:\n%s", keep, msg.Body)
		}
	}
}

func TestBuild_NoOutputOmitsTail(t *testing.T) {
	run := notify.Run{Command: "make", ExitCode: 1, Tail: "secret-looking output line\n"}
	msg, send := notify.Build(run, notify.Rules{NoOutput: true})
	if !send {
		t.Fatal("send = false, want true")
	}
	if strings.Contains(msg.Body, "secret-looking output line") {
		t.Fatalf("Body contains tail despite NoOutput:\n%s", msg.Body)
	}
	if !strings.Contains(msg.Body, "Command: make") {
		t.Fatalf("Body lost metadata:\n%s", msg.Body)
	}
}
