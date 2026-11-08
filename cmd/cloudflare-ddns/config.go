package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/TheRyanHowell/cloudflare-ddns-go/publicip"
)

// config holds the Cloudflare settings read from the environment.
type config struct {
	apiToken   string
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
	var err error
	if cfg.apiToken, err = apiToken(getenv); err != nil {
		return config{}, err
	}
	fields := []struct {
		env, name string
		dst       *string
	}{
		{"CLOUDFLARE_ZONE_ID", "zone ID", &cfg.zoneID},
		{"CLOUDFLARE_RECORD_ID", "record ID", &cfg.recordID},
		{"CLOUDFLARE_RECORD_NAME", "record name", &cfg.recordName},
	}
	for _, f := range fields {
		v := envValue(getenv, f.env)
		if v == "" {
			return config{}, fmt.Errorf("%s is not set", f.name)
		}
		*f.dst = v
	}
	if !strings.Contains(strings.TrimSuffix(cfg.recordName, "."), ".") {
		return config{}, fmt.Errorf("record name %q must be the record's full name, such as home.example.com", cfg.recordName)
	}

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
// of distinct provider names in the order to try them. Unset means every
// provider in the default order.
func ipProviderList(getenv func(string) string) ([]string, error) {
	v := envValue(getenv, "IP_PROVIDERS")
	if v == "" {
		return publicip.Providers(), nil
	}
	var names []string
	for raw := range strings.SplitSeq(v, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			return nil, fmt.Errorf("IP providers %q have an empty entry", v)
		}
		names = append(names, name)
		// Validating each prefix reports the first bad entry, which is name.
		if err := publicip.Validate(names); err != nil {
			return nil, ipProviderError(v, name, err)
		}
	}
	return names, nil
}

// ipProviderError describes why IP_PROVIDERS value v cannot name name.
func ipProviderError(v, name string, err error) error {
	if errors.Is(err, publicip.ErrDuplicateProvider) {
		return fmt.Errorf("IP providers %q name %q more than once", v, name)
	}
	return fmt.Errorf("IP provider %q must be one of %s", name, strings.Join(publicip.Providers(), ", "))
}

// syncInterval reads SYNC_INTERVAL as a Go duration, such as "10m".
func syncInterval(getenv func(string) string) (time.Duration, error) {
	v := envValue(getenv, "SYNC_INTERVAL")
	if v == "" {
		return defaultSyncInterval, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < minSyncInterval || d > maxSyncInterval {
		return 0, fmt.Errorf("sync interval %q must be a duration between %s and %s", v, minSyncInterval, maxSyncInterval)
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
		return 0, fmt.Errorf("record TTL %q must be %d (automatic) or between %d and %d seconds", v, autoRecordTTL, minRecordTTL, maxRecordTTL)
	}
	return ttl, nil
}

// envValue returns the environment variable key with surrounding space removed.
func envValue(getenv func(string) string, key string) string {
	return strings.TrimSpace(getenv(key))
}

// tokenSources are the variables that can supply the API token. The last is
// the legacy name, kept so existing configurations still work.
var tokenSources = []string{"CLOUDFLARE_API_TOKEN", "CLOUDFLARE_API_TOKEN_FILE", "CLOUDFLARE_API_KEY"}

// apiToken reads the API token from exactly one of tokenSources. The _FILE
// variable names a file holding the token, such as a Docker secret.
func apiToken(getenv func(string) string) (string, error) {
	var set []string
	for _, key := range tokenSources {
		if envValue(getenv, key) != "" {
			set = append(set, key)
		}
	}
	switch len(set) {
	case 0:
		return "", errors.New("API token is not set")
	case 1:
	default:
		return "", errors.New("set the API token only once, either directly or as a file")
	}
	if set[0] != "CLOUDFLARE_API_TOKEN_FILE" {
		return envValue(getenv, set[0]), nil
	}
	path := envValue(getenv, set[0])
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read API token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("API token file %q is empty", path)
	}
	return token, nil
}
