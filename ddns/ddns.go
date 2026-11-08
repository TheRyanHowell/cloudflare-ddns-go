// Package ddns keeps a DNS record in step with the public IP address.
package ddns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// errPermanentSync marks a record update that retrying cannot fix, such as a
// revoked token. The daemon exits so the failure is visible.
var errPermanentSync = errors.New("permanent sync failure")

const (
	defaultInterval          = 5 * time.Minute
	defaultStartupRetryDelay = 30 * time.Second
	defaultExitDelay         = time.Hour
)

// RecordStore reads and updates the DNS record.
type RecordStore interface {
	Content(ctx context.Context) (string, error)
	Update(ctx context.Context, ip string) error
}

// IPSource reports the current public IP.
type IPSource interface {
	Current(ctx context.Context) (string, error)
}

// HealthReporter is told when a sync succeeds and when one fails permanently.
type HealthReporter interface {
	MarkSuccess()
	MarkPermanentFailure(err error)
}

// Syncer keeps the DNS record in step with the public IP.
type Syncer struct {
	logger            *slog.Logger
	record            RecordStore
	lookup            IPSource
	health            HealthReporter
	isPermanent       func(error) bool
	interval          time.Duration
	startupRetryDelay time.Duration
	exitDelay         time.Duration
}

// Option configures New.
type Option func(*Syncer)

// Options given a nil value, a non-positive interval or a negative delay leave
// the default in place. A zero delay means no wait.

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option {
	return func(s *Syncer) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithHealthReporter sets where sync outcomes are reported.
func WithHealthReporter(h HealthReporter) Option {
	return func(s *Syncer) {
		if h != nil {
			s.health = h
		}
	}
}

// WithPermanentCheck sets how Run tells errors that retrying cannot fix from
// transient ones. By default no error is permanent.
func WithPermanentCheck(isPermanent func(error) bool) Option {
	return func(s *Syncer) {
		if isPermanent != nil {
			s.isPermanent = isPermanent
		}
	}
}

// WithInterval sets how often Run syncs.
func WithInterval(d time.Duration) Option { return func(s *Syncer) { setPositive(&s.interval, d) } }

// WithStartupRetryDelay sets the wait between attempts to read the record at startup.
func WithStartupRetryDelay(d time.Duration) Option {
	return func(s *Syncer) { setNonNegative(&s.startupRetryDelay, d) }
}

// WithExitDelay sets how long Run waits before returning a permanent failure.
func WithExitDelay(d time.Duration) Option {
	return func(s *Syncer) { setNonNegative(&s.exitDelay, d) }
}

func setPositive(dst *time.Duration, d time.Duration) {
	if d > 0 {
		*dst = d
	}
}

func setNonNegative(dst *time.Duration, d time.Duration) {
	if d >= 0 {
		*dst = d
	}
}

// New returns a Syncer that keeps record pointed at the address lookup reports.
func New(record RecordStore, lookup IPSource, opts ...Option) *Syncer {
	s := &Syncer{
		logger:            slog.Default(),
		record:            record,
		lookup:            lookup,
		health:            nopReporter{},
		isPermanent:       func(error) bool { return false },
		interval:          defaultInterval,
		startupRetryDelay: defaultStartupRetryDelay,
		exitDelay:         defaultExitDelay,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

type nopReporter struct{}

func (nopReporter) MarkSuccess()               {}
func (nopReporter) MarkPermanentFailure(error) {}

// Run syncs the record at startup and then every interval until ctx is
// cancelled. Transient failures are retried, at startup every
// startupRetryDelay and afterwards on the next interval. A permanent failure
// (rejected credentials, missing or unusable record) ends it after exitDelay,
// so a restarting container does not retry in a tight loop.
func (s *Syncer) Run(ctx context.Context) error {
	err := s.loop(ctx)
	if err == nil {
		return nil
	}
	s.health.MarkPermanentFailure(err)
	s.logger.Error("permanent failure, exiting after a delay", "error", err, "exit_in", s.exitDelay)
	timer := time.NewTimer(s.exitDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-timer.C:
		return err
	}
}

func (s *Syncer) loop(ctx context.Context) error {
	known, err := s.loadRecord(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	s.logger.Info("loaded current record", "ip", known)
	s.health.MarkSuccess()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		ip, err := s.syncFrom(ctx, known)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, errPermanentSync):
			return err
		case err != nil:
			s.logger.Warn("sync failed, will retry", "error", err)
		default:
			known = ip
			s.health.MarkSuccess()
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// loadRecord reads the record's current IP, retrying transient failures.
func (s *Syncer) loadRecord(ctx context.Context) (string, error) {
	for {
		known, err := s.record.Content(ctx)
		if err == nil {
			return known, nil
		}
		if ctx.Err() != nil || s.isPermanent(err) {
			return "", err
		}
		s.logger.Warn("loading record failed, will retry", "error", err, "retry_in", s.startupRetryDelay)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(s.startupRetryDelay):
		}
	}
}

// Sync reads the record and updates it when the public IP differs. It returns
// the public IP and whether the record was changed.
func (s *Syncer) Sync(ctx context.Context) (ip string, changed bool, err error) {
	known, err := s.record.Content(ctx)
	if err != nil {
		return "", false, err
	}
	if ip, err = s.syncFrom(ctx, known); err != nil {
		return "", false, err
	}
	return ip, ip != known, nil
}

// syncFrom updates the record when the public IP differs from known and
// returns the IP the record holds afterwards.
func (s *Syncer) syncFrom(ctx context.Context, known string) (string, error) {
	ip, err := s.lookup.Current(ctx)
	if err != nil {
		return known, err
	}
	if ip == known {
		return known, nil
	}
	s.logger.Info("updating record", "from", known, "to", ip)
	if err := s.record.Update(ctx, ip); err != nil {
		if s.isPermanent(err) {
			err = fmt.Errorf("%w: %w", errPermanentSync, err)
		}
		return known, err
	}
	return ip, nil
}
