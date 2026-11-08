package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// config holds the Cloudflare settings read from the environment.
type config struct {
	apiKey     string
	zoneID     string
	recordID   string
	recordName string
	interval   time.Duration
	ttl        int

	providers []string
}

const (
	// defaultSyncInterval is how often the public IP is checked unless SYNC_INTERVAL is set.
	defaultSyncInterval = 5 * time.Minute
	// minSyncInterval keeps the daemon from hammering the IP lookup services.
	minSyncInterval = 30 * time.Second
	// maxSyncInterval keeps a stuck daemon detectable by the health check.
	maxSyncInterval = 24 * time.Hour

	// defaultRecordTTL is the record TTL in seconds unless CLOUDFLARE_RECORD_TTL is set.
	defaultRecordTTL = 300
	// autoRecordTTL tells Cloudflare to choose the TTL.
	autoRecordTTL = 1
	// minRecordTTL and maxRecordTTL are Cloudflare's bounds for an explicit TTL.
	minRecordTTL = 30
	maxRecordTTL = 86400
)

// defaultHealthAddr is where the health endpoint listens unless HEALTH_ADDR is set.
const defaultHealthAddr = "127.0.0.1:8080"

// healthAddr returns the health endpoint's listen address.
func healthAddr(getenv func(string) string) string {
	if addr := envValue(getenv, "HEALTH_ADDR"); addr != "" {
		return addr
	}
	return defaultHealthAddr
}

// loadConfig builds a config from getenv, failing on the first unset variable.
func loadConfig(getenv func(string) string) (config, error) {
	var cfg config
	fields := []struct {
		env string
		dst *string
	}{
		{"CLOUDFLARE_API_KEY", &cfg.apiKey},
		{"CLOUDFLARE_ZONE_ID", &cfg.zoneID},
		{"CLOUDFLARE_RECORD_ID", &cfg.recordID},
		{"CLOUDFLARE_RECORD_NAME", &cfg.recordName},
	}
	for _, f := range fields {
		v := envValue(getenv, f.env)
		if v == "" {
			return config{}, fmt.Errorf("%s is not set", f.env)
		}
		*f.dst = v
	}
	if !strings.Contains(strings.TrimSuffix(cfg.recordName, "."), ".") {
		return config{}, fmt.Errorf("CLOUDFLARE_RECORD_NAME %q must be the record's full name, such as home.example.com", cfg.recordName)
	}

	var err error
	if cfg.interval, err = syncInterval(getenv); err != nil {
		return config{}, err
	}
	if cfg.ttl, err = recordTTL(getenv); err != nil {
		return config{}, err
	}
	if cfg.providers, err = ipProviderList(getenv); err != nil {
		return config{}, err
	}
	return cfg, nil
}

// ipProviderList reads IP_PROVIDERS, a comma-separated, case-insensitive list
// of distinct names from ipProviders in the order to try them. Unset means
// every provider in the default order.
func ipProviderList(getenv func(string) string) ([]string, error) {
	v := envValue(getenv, "IP_PROVIDERS")
	if v == "" {
		return providerNames(), nil
	}
	var names []string
	for raw := range strings.SplitSeq(v, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case name == "":
			return nil, fmt.Errorf("IP_PROVIDERS %q has an empty entry", v)
		case slices.Contains(names, name):
			return nil, fmt.Errorf("IP_PROVIDERS %q names %q more than once", v, name)
		}
		if _, ok := findProvider(name); !ok {
			return nil, fmt.Errorf("IP_PROVIDERS entry %q must be one of %s", name, strings.Join(providerNames(), ", "))
		}
		names = append(names, name)
	}
	return names, nil
}

// syncInterval reads SYNC_INTERVAL as a Go duration, such as "10m".
func syncInterval(getenv func(string) string) (time.Duration, error) {
	v := envValue(getenv, "SYNC_INTERVAL")
	if v == "" {
		return defaultSyncInterval, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < minSyncInterval || d > maxSyncInterval {
		return 0, fmt.Errorf("SYNC_INTERVAL %q must be a duration between %s and %s", v, minSyncInterval, maxSyncInterval)
	}
	return d, nil
}

// recordTTL reads CLOUDFLARE_RECORD_TTL in seconds: 1 for automatic, or 30 to 86400.
func recordTTL(getenv func(string) string) (int, error) {
	v := envValue(getenv, "CLOUDFLARE_RECORD_TTL")
	if v == "" {
		return defaultRecordTTL, nil
	}
	ttl, err := strconv.Atoi(v)
	if err != nil || (ttl != autoRecordTTL && (ttl < minRecordTTL || ttl > maxRecordTTL)) {
		return 0, fmt.Errorf("CLOUDFLARE_RECORD_TTL %q must be %d (automatic) or between %d and %d seconds", v, autoRecordTTL, minRecordTTL, maxRecordTTL)
	}
	return ttl, nil
}

// envValue returns the environment variable key with surrounding space removed.
func envValue(getenv func(string) string, key string) string {
	return strings.TrimSpace(getenv(key))
}
