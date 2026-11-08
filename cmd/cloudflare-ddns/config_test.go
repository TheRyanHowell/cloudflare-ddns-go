package main

import (
	"maps"
	"os"
	"path/filepath"
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
		"CLOUDFLARE_API_TOKEN":   "key",
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
		apiToken: "key", zoneID: "zone", recordID: "record", recordName: "home.example.com",
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
			want := map[string]string{
				"CLOUDFLARE_API_TOKEN":   "API token",
				"CLOUDFLARE_ZONE_ID":     "zone ID",
				"CLOUDFLARE_RECORD_ID":   "record ID",
				"CLOUDFLARE_RECORD_NAME": "record name",
			}[name] + " is not set"
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("err = %v, want %q", err, want)
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
		{name: "interval not a duration", interval: "5", wantErr: "sync interval"},
		{name: "interval too short", interval: "29s", wantErr: "sync interval"},
		{name: "interval maximum", interval: "24h", wantInterval: 24 * time.Hour, wantTTL: defaultRecordTTL},
		{name: "interval too long", interval: "24h1s", wantErr: "sync interval"},
		{name: "ttl not a number", ttl: "5m", wantErr: "record TTL"},
		{name: "ttl zero", ttl: "0", wantErr: "record TTL"},
		{name: "ttl below minimum", ttl: "29", wantErr: "record TTL"},
		{name: "ttl above maximum", ttl: "86401", wantErr: "record TTL"},
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
				if err == nil || !strings.Contains(err.Error(), "IP provider") || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want an IP provider error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(got.providers, tt.want) {
				t.Errorf("loadConfig() providers = %q, %v; want %q", got.providers, err, tt.want)
			}
		})
	}
}

func TestLoadConfigRequiresFullRecordName(t *testing.T) {
	for _, name := range []string{"@", "home", "home."} {
		env := baseEnv()
		env["CLOUDFLARE_RECORD_NAME"] = name
		if _, err := loadConfig(envFunc(env)); err == nil || !strings.Contains(err.Error(), "full") {
			t.Errorf("record name %q: err = %v, want an error asking for the full name", name, err)
		}
	}
}

func TestLoadConfigToken(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	tokenFile := writeFile("token", "  from-file\n")
	emptyFile := writeFile("empty", " \n")

	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr string
	}{
		{name: "token", env: map[string]string{"CLOUDFLARE_API_TOKEN": "tok"}, want: "tok"},
		{name: "legacy key", env: map[string]string{"CLOUDFLARE_API_KEY": "old"}, want: "old"},
		{name: "file", env: map[string]string{"CLOUDFLARE_API_TOKEN_FILE": tokenFile}, want: "from-file"},
		{name: "empty file", env: map[string]string{"CLOUDFLARE_API_TOKEN_FILE": emptyFile}, wantErr: "empty"},
		{name: "missing file", env: map[string]string{"CLOUDFLARE_API_TOKEN_FILE": filepath.Join(dir, "nope")}, wantErr: "nope"},
		{name: "two sources", env: map[string]string{"CLOUDFLARE_API_TOKEN": "tok", "CLOUDFLARE_API_TOKEN_FILE": tokenFile}, wantErr: "only once"},
		{name: "none", env: map[string]string{}, wantErr: "API token is not set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := baseEnv()
			delete(env, "CLOUDFLARE_API_TOKEN")
			maps.Copy(env, tt.env)
			got, err := loadConfig(envFunc(env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got.apiToken != tt.want {
				t.Errorf("token = %q, %v; want %q", got.apiToken, err, tt.want)
			}
		})
	}
}

func FuzzIPProviderList(f *testing.F) {
	for _, seed := range []string{"", " Akamai , ipinfo,cloudflare ", "amazon", "amazon,example", "amazon,AMAZON", "amazon,,akamai", " , "} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		names, err := ipProviderList(func(string) string { return value })
		if err != nil {
			if !strings.Contains(err.Error(), "IP provider") {
				t.Errorf("err = %v, want it to name the IP providers", err)
			}
			return
		}
		if len(names) == 0 {
			t.Fatalf("ipProviderList(%q) accepted an empty list", value)
		}
		seen := map[string]bool{}
		for _, name := range names {
			if seen[name] || !slices.Contains(defaultProviders, name) {
				t.Errorf("ipProviderList(%q) = %q, want distinct known names", value, names)
			}
			seen[name] = true
		}
	})
}

func TestLoadConfigIPProvidersErrorText(t *testing.T) {
	tests := map[string]string{
		"amazon,example": `IP provider "example" must be one of cloudflare, amazon, akamai, opendns, ripe, hetzner, mullvad, ipinfo, ipify, icanhazip`,
		"amazon,AMAZON":  `IP providers "amazon,AMAZON" name "amazon" more than once`,
		"amazon,,nope":   `IP providers "amazon,,nope" have an empty entry`,
		"nope,,amazon":   `IP provider "nope" must be one of cloudflare, amazon, akamai, opendns, ripe, hetzner, mullvad, ipinfo, ipify, icanhazip`,
	}
	for value, want := range tests {
		env := baseEnv()
		env["IP_PROVIDERS"] = value
		if _, err := loadConfig(envFunc(env)); err == nil || err.Error() != want {
			t.Errorf("IP_PROVIDERS=%q: err = %v, want %q", value, err, want)
		}
	}
}
