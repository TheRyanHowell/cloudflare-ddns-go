package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	pollInterval = 5 * time.Minute
	// healthStaleAfter is two and a half poll intervals: a missed poll or two
	// does not flap the check, but a stuck daemon is reported within minutes.
	healthStaleAfter = pollInterval * 5 / 2
	// startupRetryDelay is the wait between attempts to read the record at startup.
	startupRetryDelay = 30 * time.Second
	requestTimeout    = 30 * time.Second

	healthcheckCommand = "healthcheck"
)

// run runs the daemon, or the health probe when args select it.
func run(ctx context.Context, logger *slog.Logger, args []string, getenv func(string) string) error {
	addr := healthAddr(getenv)
	if len(args) > 0 {
		if args[0] != healthcheckCommand {
			return fmt.Errorf("unknown command %q", args[0])
		}
		return runHealthcheck(ctx, addr)
	}

	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}

	state := newHealth()
	s := &syncer{
		logger:            logger,
		record:            &cloudflareRecord{client: &http.Client{Timeout: requestTimeout}, baseURL: cloudflareAPIBaseURL, cfg: cfg},
		lookup:            &publicIPLookup{client: newIPv4Client(requestTimeout), url: publicIPURL},
		health:            state,
		interval:          pollInterval,
		startupRetryDelay: startupRetryDelay,
	}

	srv := newHealthServer(addr, state.handler(healthStaleAfter))
	return runAll(ctx, srv, s)
}

// runAll runs the health server and the syncer until either stops or ctx is
// cancelled, and returns the first error.
func runAll(ctx context.Context, srv *http.Server, s *syncer) error {
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := serveHealth(ctx, srv); err != nil {
			return fmt.Errorf("health server: %w", err)
		}
		return nil
	})
	g.Go(func() error { return s.run(ctx) })
	return g.Wait()
}

func runHealthcheck(ctx context.Context, addr string) error {
	url, err := healthURL(addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return probeHealth(ctx, newProbeClient(requestTimeout), url)
}
