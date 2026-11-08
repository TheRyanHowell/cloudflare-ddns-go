package main

import (
	"slices"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	tokenEnv     = "CLOUDFLARE_API_TOKEN"
	tokenFileEnv = "CLOUDFLARE_API_TOKEN_FILE"
)

// setting is a flag and the environment variable it overrides.
type setting struct {
	flag string
	env  string
	help string
}

var settings = []setting{
	{"token", tokenEnv, "Cloudflare API token; visible in the process list, so prefer --token-file or the environment"},
	{"token-file", tokenFileEnv, "path to a file holding the Cloudflare API token"},
	{"zone-id", "CLOUDFLARE_ZONE_ID", "Cloudflare zone ID"},
	{"record-id", "CLOUDFLARE_RECORD_ID", "ID of the A record to update"},
	{"record-name", "CLOUDFLARE_RECORD_NAME", "the record's full name, such as home.example.com"},
	{"ttl", "CLOUDFLARE_RECORD_TTL", "record TTL in seconds: 1 for automatic, or 30 to 86400"},
	{"interval", "SYNC_INTERVAL", "how often to check the public IP, such as 10m"},
	{"providers", "IP_PROVIDERS", "comma-separated public IP providers, in the order to try them"},
	{"health-addr", "HEALTH_ADDR", "listen address of the health endpoint"},
}

func settingNames() []string {
	names := make([]string, len(settings))
	for i, s := range settings {
		names[i] = s.flag
	}
	return names
}

func settingFor(flag string) (setting, bool) {
	i := slices.IndexFunc(settings, func(s setting) bool { return s.flag == flag })
	if i < 0 {
		return setting{}, false
	}
	return settings[i], true
}

// addFlags declares the named settings as string flags on cmd. Values stay
// strings so the config loader validates them and words its errors as before.
func addFlags(cmd *cobra.Command, names ...string) {
	for _, s := range settings {
		if slices.Contains(names, s.flag) {
			cmd.Flags().String(s.flag, "", s.help+" (env "+s.env+")")
		}
	}
}

// overlay returns a getenv that answers from the flags that were set, then
// from base. Setting either token flag hides every token variable, so a flag
// replaces the environment's token instead of conflicting with it.
func overlay(base func(string) string, flags *pflag.FlagSet) func(string) string {
	set := map[string]string{}
	flags.Visit(func(f *pflag.Flag) {
		if s, ok := settingFor(f.Name); ok {
			set[s.env] = f.Value.String()
		}
	})
	_, tokenSet := set[tokenEnv]
	_, tokenFileSet := set[tokenFileEnv]
	hideTokens := tokenSet || tokenFileSet

	return func(key string) string {
		if v, ok := set[key]; ok {
			return v
		}
		if hideTokens && slices.Contains(tokenSources, key) {
			return ""
		}
		return base(key)
	}
}
