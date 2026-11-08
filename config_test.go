package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHealthAddr(t *testing.T) {
	tests := map[string]string{"": defaultHealthAddr, "  ": defaultHealthAddr, " :9000 ": ":9000"}
	for in, want := range tests {
		got := healthAddr(func(string) string { return in })
		if got != want {
			t.Errorf("healthAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// defaultProviders is the expected order when IP_PROVIDERS is unset.
var defaultProviders = []string{"cloudflare", "amazon", "akamai", "opendns", "ripe", "hetzner", "mullvad", "ipinfo", "ipify", "icanhazip"}

// baseEnv returns the required variables, set.
func baseEnv() map[string]string {
	return map[string]string{
		"CLOUDFLARE_API_KEY":     "key",
		"CLOUDFLARE_ZONE_ID":     "zone",
		"CLOUDFLARE_RECORD_ID":   "record",
		"CLOUDFLARE_RECORD_NAME": "home.example.com",
	}
}

func TestLoadConfig(t *testing.T) {
	env := baseEnv()
	getenv := envFunc(env)

	got, err := loadConfig(getenv)
	want := config{
		apiKey: "key", zoneID: "zone", recordID: "record", recordName: "home.example.com",
		interval: defaultSyncInterval, ttl: defaultRecordTTL,
		providers: defaultProviders,
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("loadConfig() = %+v, %v; want %+v", got, err, want)
	}

	padded := func(k string) string { return " " + env[k] + "\n" }
	if got, err := loadConfig(padded); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("padded loadConfig() = %+v, %v; want %+v", got, err, want)
	}

	for name := range env {
		t.Run(name, func(t *testing.T) {
			missing := func(k string) string {
				if k == name {
					return ""
				}
				return env[k]
			}
			_, err := loadConfig(missing)
			if err == nil || err.Error() != name+" is not set" {
				t.Errorf("err = %v, want %q is not set", err, name)
			}
		})
	}
}

func TestLoadConfigTimings(t *testing.T) {
	tests := []struct {
		name         string
		interval     string
		ttl          string
		wantInterval time.Duration
		wantTTL      int
		wantErr      string
	}{
		{name: "custom", interval: " 10m ", ttl: " 120 ", wantInterval: 10 * time.Minute, wantTTL: 120},
		{name: "minimums", interval: "30s", ttl: "30", wantInterval: 30 * time.Second, wantTTL: 30},
		{name: "auto ttl", ttl: "1", wantInterval: defaultSyncInterval, wantTTL: 1},
		{name: "max ttl", ttl: "86400", wantInterval: defaultSyncInterval, wantTTL: 86400},
		{name: "interval not a duration", interval: "5", wantErr: "SYNC_INTERVAL"},
		{name: "interval too short", interval: "29s", wantErr: "SYNC_INTERVAL"},
		{name: "interval maximum", interval: "24h", wantInterval: 24 * time.Hour, wantTTL: defaultRecordTTL},
		{name: "interval too long", interval: "24h1s", wantErr: "SYNC_INTERVAL"},
		{name: "ttl not a number", ttl: "5m", wantErr: "CLOUDFLARE_RECORD_TTL"},
		{name: "ttl zero", ttl: "0", wantErr: "CLOUDFLARE_RECORD_TTL"},
		{name: "ttl below minimum", ttl: "29", wantErr: "CLOUDFLARE_RECORD_TTL"},
		{name: "ttl above maximum", ttl: "86401", wantErr: "CLOUDFLARE_RECORD_TTL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := baseEnv()
			env["SYNC_INTERVAL"] = tt.interval
			env["CLOUDFLARE_RECORD_TTL"] = tt.ttl
			got, err := loadConfig(envFunc(env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got.interval != tt.wantInterval || got.ttl != tt.wantTTL {
				t.Errorf("loadConfig() = interval %v, ttl %d, err %v; want %v, %d", got.interval, got.ttl, err, tt.wantInterval, tt.wantTTL)
			}
		})
	}
}

func TestLoadConfigIPProviders(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    []string
		wantErr string
	}{
		{name: "default", want: defaultProviders},
		{name: "ordered and normalised", value: " Akamai , ipinfo,cloudflare ", want: []string{"akamai", "ipinfo", "cloudflare"}},
		{name: "single", value: "amazon", want: []string{"amazon"}},
		{name: "unknown", value: "amazon,example", wantErr: `"example"`},
		{name: "duplicate", value: "amazon,AMAZON", wantErr: "more than once"},
		{name: "empty entry", value: "amazon,,akamai", wantErr: "empty"},
		{name: "only separators", value: " , ", wantErr: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := baseEnv()
			env["IP_PROVIDERS"] = tt.value
			got, err := loadConfig(envFunc(env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), "IP_PROVIDERS") || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want IP_PROVIDERS error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(got.providers, tt.want) {
				t.Errorf("loadConfig() providers = %q, %v; want %q", got.providers, err, tt.want)
			}
		})
	}
}
