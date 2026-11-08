package main

import (
	"context"
	"fmt"

	"github.com/TheRyanHowell/cloudflare-ddns-go/cloudflare"
	"github.com/TheRyanHowell/cloudflare-ddns-go/ddns"
)

// syncFlags are the settings one sync needs; the daemon's timing and health
// settings do not apply.
var syncFlags = []string{"token", "token-file", "zone-id", "record-id", "record-name", "ttl", "providers"}

// observedRecord remembers the address the record held when it was read, which
// Syncer.Sync does not return.
type observedRecord struct {
	ddns.RecordStore
	before string
}

func (o *observedRecord) Content(ctx context.Context) (string, error) {
	content, err := o.RecordStore.Content(ctx)
	if err == nil {
		o.before = content
	}
	return content, err
}

// runSync reads the record, updates it if the public IP differs, reports which
// and returns.
func (a app) runSync(ctx context.Context, getenv func(string) string) error {
	cfg, err := loadConfig(withoutInterval(getenv))
	if err != nil {
		return err
	}
	lookup, err := a.newLookup(cfg.providers, cfg.interval)
	if err != nil {
		return err
	}
	record := &observedRecord{RecordStore: a.newRecord(cfg)}
	s := ddns.New(record, lookup, ddns.WithLogger(a.logger), ddns.WithPermanentCheck(cloudflare.IsPermanent))

	ip, changed, err := s.Sync(ctx)
	if err != nil {
		return fmt.Errorf("sync %s: %w", cfg.recordName, err)
	}
	if changed {
		printf(a.stdout, "updated %s: %s -> %s\n", cfg.recordName, record.before, ip)
		return nil
	}
	printf(a.stdout, "%s is already %s\n", cfg.recordName, ip)
	return nil
}

// withoutInterval hides SYNC_INTERVAL, which only the daemon uses, so a bad
// value shared with the daemon's environment does not stop a one-off sync.
func withoutInterval(getenv func(string) string) func(string) string {
	return func(key string) string {
		if key == "SYNC_INTERVAL" {
			return ""
		}
		return getenv(key)
	}
}
