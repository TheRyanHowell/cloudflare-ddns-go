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
	// startupRetryDelay is the wait between attempts to read the record at startup.
	startupRetryDelay = 30 * time.Second
	// permanentFailureExitDelay spaces out restarts after a permanent failure,
	// such as a rejected token, so the container retries hourly.
	permanentFailureExitDelay = time.Hour
	requestTimeout            = 30 * time.Second

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
		lookup:            newPublicIPSource(logger, newIPv4Client(requestTimeout), cfg.providers, cfg.interval),
		health:            state,
		interval:          cfg.interval,
		startupRetryDelay: startupRetryDelay,
		exitDelay:         permanentFailureExitDelay,
	}

	srv := newHealthServer(addr, state.handler(healthStaleAfter(cfg.interval, len(cfg.providers))))
	return runAll(ctx, srv, s)
}

// healthStaleAfter allows two and a half sync intervals plus one slow sync, in
// which every provider and the record update time out, so a missed sync or two
// does not flap the check but a stuck daemon is reported.
func healthStaleAfter(interval time.Duration, providers int) time.Duration {
	slowSync := time.Duration(providers+1) * requestTimeout
	return interval*5/2 + slowSync
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
	return probeHealth(ctx, newProbeClient(requestTimeout), url)
}
