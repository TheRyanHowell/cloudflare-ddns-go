// Command cloudflare-ddns-go keeps a Cloudflare A record pointed at this
// host's public IP address.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, logger, os.Args[1:], os.Getenv)
	stop()

	if err != nil {
		logger.Error("exiting", "error", err)
		os.Exit(1)
	}
}
