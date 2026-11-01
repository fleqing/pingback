package runner_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"pingback/internal/runner"
)

func TestRun_ForwardsSIGTERMToChild(t *testing.T) {
	go func() {
		time.Sleep(200 * time.Millisecond)
		syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	start := time.Now()
	res, err := runner.Run(context.Background(), []string{"sleep", "5"}, runner.Options{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("child was not terminated promptly")
	}
	if res.ExitCode != 143 {
		t.Fatalf("ExitCode = %d, want 143", res.ExitCode)
	}
	if res.Signal != syscall.SIGTERM {
		t.Fatalf("Signal = %v, want SIGTERM", res.Signal)
	}
}

func TestRun_RecordsStartTimeAndDuration(t *testing.T) {
	before := time.Now()
	res, err := runner.Run(context.Background(), []string{"sleep", "0.2"}, runner.Options{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.StartedAt.Before(before) || res.StartedAt.After(time.Now()) {
		t.Fatalf("StartedAt = %v, want within test window", res.StartedAt)
	}
	if res.Duration < 200*time.Millisecond {
		t.Fatalf("Duration = %v, want >= 200ms", res.Duration)
	}
}

func TestRun_KeepsLastNLinesOfOutput(t *testing.T) {
	res, err := runner.Run(context.Background(),
		[]string{"sh", "-c", "echo 1; echo 2; echo 3 >&2; echo 4; echo 5"},
		runner.Options{TailLines: 2})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if want := "4\n5\n"; res.Tail != want {
		t.Fatalf("Tail = %q, want %q", res.Tail, want)
	}
}

func TestRun_PassesThroughExitCode(t *testing.T) {
	res, err := runner.Run(context.Background(), []string{"sh", "-c", "exit 3"}, runner.Options{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
}

func TestRun_EchoesOutputWhileKeepingTail(t *testing.T) {
	var out bytes.Buffer
	res, err := runner.Run(context.Background(),
		[]string{"sh", "-c", "echo out; echo err >&2"},
		runner.Options{TailLines: 10, Output: &out})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(out.String(), "out\n") || !strings.Contains(out.String(), "err\n") {
		t.Fatalf("Output = %q, want both stdout and stderr lines", out.String())
	}
	if !strings.Contains(res.Tail, "out\n") || !strings.Contains(res.Tail, "err\n") {
		t.Fatalf("Tail = %q, want both lines", res.Tail)
	}
}
