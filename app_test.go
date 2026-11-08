package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func envFunc(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

var fullEnv = map[string]string{
	"CLOUDFLARE_API_KEY":     "key",
	"CLOUDFLARE_ZONE_ID":     "zone",
	"CLOUDFLARE_RECORD_ID":   "record",
	"CLOUDFLARE_RECORD_NAME": "home.example.com",
	"HEALTH_ADDR":            "127.0.0.1:0",
}

func TestRunErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"missing config", nil, map[string]string{}, "CLOUDFLARE_API_KEY is not set"},
		{"unknown command", []string{"bogus"}, fullEnv, `unknown command "bogus"`},
		{"bad health address", []string{healthcheckCommand}, map[string]string{"HEALTH_ADDR": "nonsense"}, "invalid health address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(context.Background(), discardLogger(), tt.args, envFunc(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestRunHealthcheck(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		env := map[string]string{"HEALTH_ADDR": srv.Listener.Addr().String()}
		err := run(context.Background(), discardLogger(), []string{healthcheckCommand}, envFunc(env))
		srv.Close()
		if (err != nil) != (status != http.StatusOK) {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
}

func TestRunStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, discardLogger(), nil, envFunc(fullEnv)); err != nil {
		t.Errorf("run() = %v, want nil", err)
	}
}

func TestRunAllHealthServerFailureIsFatal(t *testing.T) {
	ln := httptest.NewServer(http.NotFoundHandler())
	defer ln.Close()
	srv := newHealthServer(ln.Listener.Addr().String(), http.NotFoundHandler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := newTestSyncer(&fakeRecord{content: "1.1.1.1"}, &fakeLookup{ips: []string{"1.1.1.1"}})

	err := runAll(ctx, srv, s)
	if err == nil || !strings.Contains(err.Error(), "health server") {
		t.Errorf("err = %v, want health server error", err)
	}
}

func TestRunAllStopsBothOnSyncerError(t *testing.T) {
	denied := &statusError{code: 403, status: "403 Forbidden"}
	srv := newHealthServer("127.0.0.1:0", http.NotFoundHandler())
	s := newTestSyncer(&fakeRecord{contentErrs: []error{denied}}, &fakeLookup{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runAll(ctx, srv, s); !errors.Is(err, denied) {
		t.Errorf("err = %v, want %v", err, denied)
	}
}

func TestHealthStaleAfter(t *testing.T) {
	tests := map[time.Duration]time.Duration{
		30 * time.Second: 75*time.Second + maxSyncDuration,
		5 * time.Minute:  12*time.Minute + 30*time.Second + maxSyncDuration,
	}
	for interval, want := range tests {
		if got := healthStaleAfter(interval); got != want {
			t.Errorf("healthStaleAfter(%v) = %v, want %v", interval, got, want)
		}
	}
	if maxSyncDuration < 3*requestTimeout {
		t.Errorf("maxSyncDuration %v is below two lookups and an update (%v)", maxSyncDuration, 3*requestTimeout)
	}
}
