package main

import (
	"context"
	"fmt"
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

// runDaemon runs the sync loop and the health server until one fails or ctx is
// cancelled.
func (a app) runDaemon(ctx context.Context, getenv func(string) string) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}

	lookup, err := a.newLookup(cfg.providers, cfg.interval)
	if err != nil {
		return err
	}
	state := health.New()
	s := ddns.New(a.newRecord(cfg), lookup,
		ddns.WithLogger(a.logger),
		ddns.WithHealthReporter(state),
		ddns.WithPermanentCheck(cloudflare.IsPermanent),
		ddns.WithInterval(cfg.interval),
	)

	srv := health.NewServer(healthAddr(getenv), state.Handler(healthStaleAfter(cfg.interval, len(cfg.providers))))
	return runAll(ctx, srv, s)
}

// newLookup builds the public IP lookup over the named providers.
func (a app) newLookup(providers []string, interval time.Duration) (*publicip.Lookup, error) {
	lookup, err := publicip.New(providers,
		publicip.WithLogger(a.logger), publicip.WithInterval(interval), publicip.WithHTTPClient(a.httpClient))
	if err != nil {
		return nil, fmt.Errorf("public IP lookup: %w", err)
	}
	return lookup, nil
}

func (a app) newRecord(cfg config) *cloudflare.Record {
	return cloudflare.NewRecord(cloudflare.RecordConfig{
		Token:    cfg.apiToken,
		ZoneID:   cfg.zoneID,
		RecordID: cfg.recordID,
		Name:     cfg.recordName,
		TTL:      cfg.ttl,
	}, cloudflare.WithHTTPClient(a.httpClient))
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

func (a app) runHealthcheck(ctx context.Context, getenv func(string) string) error {
	url, err := health.URL(healthAddr(getenv))
	if err != nil {
		return err
	}
	return health.Probe(ctx, health.NewProbeClient(probeTimeout), url)
}
