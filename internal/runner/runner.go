package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Options struct {
	TailLines int
	// Output, if set, receives the command's stdout and stderr as they are produced.
	Output io.Writer
}

type Result struct {
	ExitCode  int
	Tail      string
	StartedAt time.Time
	Duration  time.Duration
	// Signal is the signal that terminated the command, or nil.
	Signal os.Signal
}

// tailBuffer keeps the last n complete lines written to it.
type tailBuffer struct {
	mu    sync.Mutex
	n     int
	buf   strings.Builder
	lines []string
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	s := t.buf.String()
	idx := strings.LastIndex(s, "\n")
	if idx < 0 {
		return len(p), nil
	}
	t.lines = append(t.lines, strings.SplitAfter(s[:idx+1], "\n")[:strings.Count(s[:idx+1], "\n")]...)
	if t.n > 0 && len(t.lines) > t.n {
		t.lines = t.lines[len(t.lines)-t.n:]
	}
	t.buf.Reset()
	t.buf.WriteString(s[idx+1:])
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "") + t.buf.String()
}

func Run(ctx context.Context, argv []string, opts Options) (Result, error) {
	tail := &tailBuffer{n: opts.TailLines}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var w io.Writer = tail
	if opts.Output != nil {
		w = io.MultiWriter(opts.Output, tail)
	}
	cmd.Stdout = w
	cmd.Stderr = w
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigs:
				cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)

	res := Result{Tail: tail.String(), StartedAt: start, Duration: time.Since(start)}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal = ws.Signal()
			res.ExitCode = 128 + int(ws.Signal())
			return res, nil
		}
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil {
		return Result{}, err
	}
	return res, nil
}
