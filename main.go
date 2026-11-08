// Command cloudflare-ddns-go keeps a Cloudflare A record pointed at this
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
	os.Exit(exitCode(os.Args[1:], os.Getenv, os.Stderr))
}

// exitCode runs the program until it finishes or is signalled, logging to
// stderr, and returns the process exit code.
func exitCode(args []string, getenv func(string) string, stderr io.Writer) int {
	logger := slog.New(slog.NewTextHandler(stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, logger, args, getenv)
	stop()

	if err != nil {
		logger.Error("exiting", "error", err)
		return 1
	}
	return 0
}
