package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TheRyanHowell/cloudflare-ddns-go/cloudflare"
	"github.com/TheRyanHowell/cloudflare-ddns-go/ddns"
	"github.com/TheRyanHowell/cloudflare-ddns-go/internal/fetch"
)

var errFake = errors.New("fake failure")

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeRecord fails Content with contentErrs, one per call, then returns content.
type fakeRecord struct {
	content     string
	contentErrs []error
}

func (f *fakeRecord) Content(context.Context) (string, error) {
	if len(f.contentErrs) == 0 {
		return f.content, nil
	}
	err := f.contentErrs[0]
	f.contentErrs = f.contentErrs[1:]
	return "", err
}

func (f *fakeRecord) Update(context.Context, string) error { return nil }

// fakeLookup returns ips in order, then errFake.
type fakeLookup struct{ ips []string }

func (f *fakeLookup) Current(context.Context) (string, error) {
	if len(f.ips) == 0 {
		return "", errFake
	}
	ip := f.ips[0]
	f.ips = f.ips[1:]
	return ip, nil
}

func newTestSyncer(record *fakeRecord, lookup *fakeLookup) *ddns.Syncer {
	return ddns.New(record, lookup,
		ddns.WithLogger(discardLogger()),
		ddns.WithPermanentCheck(cloudflare.IsPermanent),
		ddns.WithInterval(time.Millisecond),
		ddns.WithStartupRetryDelay(time.Millisecond),
		ddns.WithExitDelay(0),
	)
}

func envFunc(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// fullEnv is the required variables plus a health address any free port satisfies.
var fullEnv = func() map[string]string {
	env := baseEnv()
	env["HEALTH_ADDR"] = "127.0.0.1:0"
	return env
}()

func TestRunErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"missing config", nil, map[string]string{}, "CLOUDFLARE_API_TOKEN is not set"},
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
	denied := &fetch.StatusError{Code: 403, Status: "403 Forbidden"}
	srv := newHealthServer("127.0.0.1:0", http.NotFoundHandler())
	s := newTestSyncer(&fakeRecord{contentErrs: []error{denied}}, &fakeLookup{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runAll(ctx, srv, s); !errors.Is(err, denied) {
		t.Errorf("err = %v, want %v", err, denied)
	}
}

func TestHealthStaleAfter(t *testing.T) {
	// One request timeout per provider, plus one for the record update.
	tests := []struct {
		interval  time.Duration
		providers int
		want      time.Duration
	}{
		{30 * time.Second, 1, 75*time.Second + 2*requestTimeout},
		{30 * time.Second, 2, 75*time.Second + 3*requestTimeout},
		{5 * time.Minute, 7, 12*time.Minute + 30*time.Second + 8*requestTimeout},
	}
	for _, tt := range tests {
		if got := healthStaleAfter(tt.interval, tt.providers); got != tt.want {
			t.Errorf("healthStaleAfter(%v, %d) = %v, want %v", tt.interval, tt.providers, got, tt.want)
		}
	}
}
