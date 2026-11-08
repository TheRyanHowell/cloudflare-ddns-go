package main

import (
	"fmt"
	"strings"
)

// config holds the Cloudflare settings read from the environment.
type config struct {
	apiKey     string
	zoneID     string
	recordID   string
	recordName string
}

// defaultHealthAddr is where the health endpoint listens unless HEALTH_ADDR is set.
const defaultHealthAddr = "127.0.0.1:8080"

// healthAddr returns the health endpoint's listen address.
func healthAddr(getenv func(string) string) string {
	if addr := strings.TrimSpace(getenv("HEALTH_ADDR")); addr != "" {
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
		v := strings.TrimSpace(getenv(f.env))
		if v == "" {
			return config{}, fmt.Errorf("%s is not set", f.env)
		}
		*f.dst = v
	}
	return cfg, nil
}
