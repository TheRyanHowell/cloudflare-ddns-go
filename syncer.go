package main

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

// recordStore reads and updates the DNS record.
type recordStore interface {
	Content(ctx context.Context) (string, error)
	Update(ctx context.Context, ip string) error
}

// ipSource reports the current public IP.
type ipSource interface {
	Current(ctx context.Context) (string, error)
}

// syncer keeps the DNS record in step with the public IP.
type syncer struct {
	logger            *slog.Logger
	record            recordStore
	lookup            ipSource
	health            *health
	interval          time.Duration
	startupRetryDelay time.Duration
	exitDelay         time.Duration
}

// run syncs the record at startup and then every interval until ctx is
// cancelled. Transient failures are retried, at startup every
// startupRetryDelay and afterwards on the next interval. A permanent failure
// (rejected credentials, missing or unusable record) ends it after exitDelay,
// so a restarting container does not retry in a tight loop.
func (s *syncer) run(ctx context.Context) error {
	err := s.loop(ctx)
	if err == nil {
		return nil
	}
	s.health.markPermanentFailure(err)
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

func (s *syncer) loop(ctx context.Context) error {
	known, err := s.loadRecord(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	s.logger.Info("loaded current record", "ip", known)
	s.health.markSuccess()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		ip, err := s.sync(ctx, known)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, errPermanentSync):
			return err
		case err != nil:
			s.logger.Warn("sync failed, will retry", "error", err)
		default:
			known = ip
			s.health.markSuccess()
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// loadRecord reads the record's current IP, retrying transient failures.
func (s *syncer) loadRecord(ctx context.Context) (string, error) {
	for {
		known, err := s.record.Content(ctx)
		if err == nil {
			return known, nil
		}
		if ctx.Err() != nil || permanent(err) {
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

// sync updates the record when the public IP differs from known and returns
// the IP the record holds afterwards.
func (s *syncer) sync(ctx context.Context, known string) (string, error) {
	ip, err := s.lookup.Current(ctx)
	if err != nil {
		return known, err
	}
	if ip == known {
		return known, nil
	}
	s.logger.Info("updating record", "from", known, "to", ip)
	if err := s.record.Update(ctx, ip); err != nil {
		if permanent(err) {
			err = fmt.Errorf("%w: %w", errPermanentSync, err)
		}
		return known, err
	}
	return ip, nil
}
