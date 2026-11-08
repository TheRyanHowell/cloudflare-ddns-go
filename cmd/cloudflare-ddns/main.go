// Command cloudflare-ddns keeps a Cloudflare A record pointed at this
// host's public IP address.
package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	os.Exit(exitCode(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// exitCode runs the command in args until it finishes or is signalled, logging
// to stderr, and returns the process exit code.
func exitCode(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	a := app{getenv: getenv, stdout: stdout, stderr: stderr, logger: slog.New(slog.NewTextHandler(stderr, nil))}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.execute(ctx, args)
}
