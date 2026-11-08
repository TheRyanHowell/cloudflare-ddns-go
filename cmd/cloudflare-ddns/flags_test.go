package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// allFlags pairs every flag with the variable it falls back to.
var allFlags = []struct{ flag, env string }{
	{"token", "CLOUDFLARE_API_TOKEN"},
	{"token-file", "CLOUDFLARE_API_TOKEN_FILE"},
	{"zone-id", "CLOUDFLARE_ZONE_ID"},
	{"record-id", "CLOUDFLARE_RECORD_ID"},
	{"record-name", "CLOUDFLARE_RECORD_NAME"},
	{"ttl", "CLOUDFLARE_RECORD_TTL"},
	{"interval", "SYNC_INTERVAL"},
	{"providers", "IP_PROVIDERS"},
	{"health-addr", "HEALTH_ADDR"},
}

func allFlagNames() []string {
	var names []string
	for _, f := range allFlags {
		names = append(names, f.flag)
	}
	return names
}

// overlayFor parses args against every flag and returns the environment they overlay.
func overlayFor(t *testing.T, args []string, env map[string]string) func(string) string {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	addFlags(cmd, allFlagNames()...)
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%q) = %v", args, err)
	}
	return overlay(envFunc(env), cmd.Flags())
}

func TestFlagsOverrideTheirVariables(t *testing.T) {
	for _, f := range allFlags {
		t.Run(f.flag, func(t *testing.T) {
			env := map[string]string{f.env: "from-env"}
			if got := overlayFor(t, nil, env)(f.env); got != "from-env" {
				t.Errorf("without the flag: %q, want the environment's value", got)
			}
			if got := overlayFor(t, []string{"--" + f.flag, "from-flag"}, env)(f.env); got != "from-flag" {
				t.Errorf("with the flag: %q, want the flag's value", got)
			}
		})
	}
}

func TestFlagSetToEmptyStillWins(t *testing.T) {
	getenv := overlayFor(t, []string{"--zone-id="}, map[string]string{"CLOUDFLARE_ZONE_ID": "from-env"})
	if got := getenv("CLOUDFLARE_ZONE_ID"); got != "" {
		t.Errorf("zone = %q, want empty", got)
	}
}

func TestTokenFlagsIgnoreEveryTokenVariable(t *testing.T) {
	env := map[string]string{
		"CLOUDFLARE_API_TOKEN": "env-token", "CLOUDFLARE_API_TOKEN_FILE": "/env/file", "CLOUDFLARE_API_KEY": "env-key",
		"CLOUDFLARE_ZONE_ID": "zone",
	}
	tests := []struct {
		name string
		args []string
		want map[string]string
	}{
		{"no flag", nil, map[string]string{"CLOUDFLARE_API_TOKEN": "env-token", "CLOUDFLARE_API_TOKEN_FILE": "/env/file", "CLOUDFLARE_API_KEY": "env-key"}},
		{"token", []string{"--token", "flag"}, map[string]string{"CLOUDFLARE_API_TOKEN": "flag", "CLOUDFLARE_API_TOKEN_FILE": "", "CLOUDFLARE_API_KEY": ""}},
		{"token file", []string{"--token-file", "/flag/file"}, map[string]string{"CLOUDFLARE_API_TOKEN": "", "CLOUDFLARE_API_TOKEN_FILE": "/flag/file", "CLOUDFLARE_API_KEY": ""}},
		{"both", []string{"--token", "a", "--token-file", "b"}, map[string]string{"CLOUDFLARE_API_TOKEN": "a", "CLOUDFLARE_API_TOKEN_FILE": "b", "CLOUDFLARE_API_KEY": ""}},
		{"other flag", []string{"--zone-id", "z"}, map[string]string{"CLOUDFLARE_API_TOKEN": "env-token", "CLOUDFLARE_API_KEY": "env-key", "CLOUDFLARE_ZONE_ID": "z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := overlayFor(t, tt.args, env)
			for key, want := range tt.want {
				if got := getenv(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

func TestTokenFlagsFeedConfigValidation(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := baseEnv()
	env["CLOUDFLARE_API_KEY"] = "legacy"

	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{"environment alone is ambiguous", nil, "", "set only one of CLOUDFLARE_API_TOKEN, CLOUDFLARE_API_KEY"},
		{"token flag overrides the environment", []string{"--token", "flag-token"}, "flag-token", ""},
		{"token file flag overrides the environment", []string{"--token-file", tokenFile}, "file-token", ""},
		{"both flags", []string{"--token", "a", "--token-file", tokenFile}, "", "set only one of CLOUDFLARE_API_TOKEN, CLOUDFLARE_API_TOKEN_FILE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadConfig(overlayFor(t, tt.args, env))
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || cfg.apiToken != tt.want {
				t.Errorf("token = %q, %v; want %q", cfg.apiToken, err, tt.want)
			}
		})
	}
}

func TestFlagValuesGoThroughConfigValidation(t *testing.T) {
	a, _, stderr := newTestApp(baseEnv())
	if code := a.execute(context.Background(), []string{"run", "--interval", "5"}); code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr.String(), `SYNC_INTERVAL \"5\" must be a duration`) {
		t.Errorf("stderr = %q, want the SYNC_INTERVAL validation error", stderr.String())
	}
}

func TestCommandsDeclareOnlyTheirFlags(t *testing.T) {
	a, _, _ := newTestApp(nil)
	tests := []struct {
		command []string
		flags   []string
	}{
		{nil, allFlagNames()},
		{[]string{"run"}, allFlagNames()},
		{[]string{"healthcheck"}, []string{"health-addr"}},
		{[]string{"version"}, nil},
	}
	for _, tt := range tests {
		cmd, _, err := a.newRootCmd().Find(tt.command)
		if err != nil {
			t.Fatalf("Find(%q) = %v", tt.command, err)
		}
		var got []string
		cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if f.Name != "help" {
				got = append(got, f.Name)
			}
		})
		slices.Sort(got)
		want := slices.Sorted(slices.Values(tt.flags))
		if !slices.Equal(got, want) {
			t.Errorf("%q flags = %q, want %q", tt.command, got, want)
		}
	}
}

func TestUsageErrorsExitWithTwo(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"run", "--bogus"}, "unknown flag: --bogus"},
		{"flag missing its value", []string{"run", "--interval"}, "flag needs an argument: --interval"},
		{"flag of another command", []string{"healthcheck", "--token", "x"}, "unknown flag: --token"},
		{"extra argument", []string{"healthcheck", "extra"}, `unexpected argument "extra"`},
		{"flag without a command", []string{"--bogus"}, "unknown flag: --bogus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, stdout, stderr := newTestApp(nil)
			if code := a.execute(context.Background(), tt.args); code != exitUsage {
				t.Errorf("exit code = %d, want %d", code, exitUsage)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), tt.want) || !strings.Contains(stderr.String(), "Usage:") {
				t.Errorf("stdout = %q, stderr = %q; want %q and the usage on stderr", stdout.String(), stderr.String(), tt.want)
			}
		})
	}
}

func TestCommandHelpListsItsFlags(t *testing.T) {
	a, stdout, stderr := newTestApp(nil)
	if code := a.execute(context.Background(), []string{"run", "-h"}); code != exitOK {
		t.Fatalf("execute(run -h) = %d", code)
	}
	out := stdout.String()
	for _, want := range []string{"--interval", "SYNC_INTERVAL", "--token-file", "process list"} {
		if !strings.Contains(out, want) {
			t.Errorf("run -h output lacks %q:\n%s", want, out)
		}
	}

	stdout.Reset()
	if code := a.execute(context.Background(), []string{"healthcheck", "--help"}); code != exitOK {
		t.Fatalf("execute(healthcheck --help) = %d", code)
	}
	if out := stdout.String(); !strings.Contains(out, "--health-addr") || strings.Contains(out, "--token") {
		t.Errorf("healthcheck --help output = %q, want only its own flag", out)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestHealthcheckFlagOverridesEnvironment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	a, _, stderr := newTestApp(map[string]string{"HEALTH_ADDR": "nonsense"})
	args := []string{"healthcheck", "--health-addr", srv.Listener.Addr().String()}
	if code := a.execute(context.Background(), args); code != exitOK {
		t.Errorf("exit code = %d, stderr %q; want the flag's address to be probed", code, stderr.String())
	}
}

func TestFlagsWithoutACommandConfigureTheDaemon(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, _, stderr := newTestApp(nil)
	args := []string{
		"--token", "t", "--zone-id", "z", "--record-id", "r", "--record-name", "home.example.com",
		"--health-addr", "127.0.0.1:0",
	}
	if code := a.execute(ctx, args); code != exitOK {
		t.Errorf("exit code = %d, stderr %q; want the daemon to start from flags and stop with the context", code, stderr.String())
	}
}
