package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// newTestApp returns an app reading env, with its stdout and stderr captured.
// The logger writes to stderr, as the real one does.
func newTestApp(env map[string]string) (a app, stdout, stderr *bytes.Buffer) {
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	return app{
		getenv: envFunc(env), stdout: stdout, stderr: stderr,
		logger: slog.New(slog.NewTextHandler(stderr, nil)),
	}, stdout, stderr
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		a, stdout, stderr := newTestApp(nil)
		if code := a.execute(context.Background(), args); code != exitOK {
			t.Fatalf("execute(%q) = %d, want %d", args, code, exitOK)
		}
		if stderr.Len() != 0 {
			t.Errorf("execute(%q) wrote to stderr: %q", args, stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "cloudflare-ddns [command]") {
			t.Errorf("execute(%q) stdout = %q, want the usage line", args, out)
		}
		for _, name := range []string{"run", "healthcheck", "version", "completion", "help"} {
			if !strings.Contains(out, "\n  "+name+" ") {
				t.Errorf("execute(%q) stdout does not list command %q:\n%s", args, name, out)
			}
		}
	}
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	a, stdout, stderr := newTestApp(nil)
	if code := a.execute(context.Background(), []string{"bogus"}); code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	for _, want := range []string{`unknown command "bogus"`, "Usage:", "cloudflare-ddns [command]"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want containing %q", stderr.String(), want)
		}
	}
}

func TestRunCommandAndNoCommandBothRunTheDaemon(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, args := range [][]string{nil, {"run"}} {
		a, _, stderr := newTestApp(fullEnv)
		if code := a.execute(ctx, args); code != exitOK {
			t.Errorf("execute(%q) = %d, stderr %q; want the daemon to start and stop with the context", args, code, stderr.String())
		}
	}
}
