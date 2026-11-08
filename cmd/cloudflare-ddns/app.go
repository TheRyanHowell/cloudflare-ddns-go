package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/TheRyanHowell/cloudflare-ddns-go/cloudflare"
	"github.com/TheRyanHowell/cloudflare-ddns-go/ddns"
	"github.com/TheRyanHowell/cloudflare-ddns-go/internal/health"
	"github.com/TheRyanHowell/cloudflare-ddns-go/publicip"
	"golang.org/x/sync/errgroup"
)

const (
	// probeTimeout bounds the healthcheck subcommand's request.
	probeTimeout = 30 * time.Second

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

	lookup, err := publicip.New(cfg.providers, publicip.WithLogger(logger), publicip.WithInterval(cfg.interval))
	if err != nil {
		return fmt.Errorf("public IP lookup: %w", err)
	}
	state := health.New()
	s := ddns.New(newRecord(cfg), lookup,
		ddns.WithLogger(logger),
		ddns.WithHealthReporter(state),
		ddns.WithPermanentCheck(cloudflare.IsPermanent),
		ddns.WithInterval(cfg.interval),
	)

	srv := health.NewServer(addr, state.Handler(healthStaleAfter(cfg.interval, len(cfg.providers))))
	return runAll(ctx, srv, s)
}

func newRecord(cfg config) *cloudflare.Record {
	return cloudflare.NewRecord(cloudflare.RecordConfig{
		Token:    cfg.apiToken,
		ZoneID:   cfg.zoneID,
		RecordID: cfg.recordID,
		Name:     cfg.recordName,
		TTL:      cfg.ttl,
	})
}

// healthStaleAfter allows two and a half sync intervals plus one slow sync, in
// which every provider and the record update time out, so a missed sync or two
// does not flap the check but a stuck daemon is reported.
func healthStaleAfter(interval time.Duration, providers int) time.Duration {
	slowSync := time.Duration(providers)*publicip.RequestTimeout + cloudflare.RequestTimeout
	return interval*5/2 + slowSync
}

// runAll runs the health server and the syncer until either stops or ctx is
// cancelled, and returns the first error.
func runAll(ctx context.Context, srv *http.Server, s *ddns.Syncer) error {
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := health.Serve(ctx, srv); err != nil {
			return fmt.Errorf("health server: %w", err)
		}
		return nil
	})
	g.Go(func() error { return s.Run(ctx) })
	return g.Wait()
}

func runHealthcheck(ctx context.Context, addr string) error {
	url, err := health.URL(addr)
	if err != nil {
		return err
	}
	return health.Probe(ctx, health.NewProbeClient(probeTimeout), url)
}
