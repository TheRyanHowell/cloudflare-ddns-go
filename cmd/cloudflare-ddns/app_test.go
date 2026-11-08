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
	"github.com/TheRyanHowell/cloudflare-ddns-go/internal/health"
	"github.com/TheRyanHowell/cloudflare-ddns-go/publicip"
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
		{"bad health address", []string{healthcheckCommand}, map[string]string{"HEALTH_ADDR": "nonsense"}, "invalid health address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, stderr := newTestApp(tt.env)
			code := a.execute(context.Background(), tt.args)
			if code != exitFailure || !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("exit %d, stderr %q; want %d and %q", code, stderr.String(), exitFailure, tt.want)
			}
		})
	}
}

func TestRunHealthcheck(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		a, _, _ := newTestApp(map[string]string{"HEALTH_ADDR": srv.Listener.Addr().String()})
		code := a.execute(context.Background(), []string{healthcheckCommand})
		srv.Close()
		if (code != exitOK) != (status != http.StatusOK) {
			t.Errorf("status %d: exit code %d", status, code)
		}
	}
}

func TestRunAllHealthServerFailureIsFatal(t *testing.T) {
	ln := httptest.NewServer(http.NotFoundHandler())
	defer ln.Close()
	srv := health.NewServer(ln.Listener.Addr().String(), http.NotFoundHandler())

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
	srv := health.NewServer("127.0.0.1:0", http.NotFoundHandler())
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
		{30 * time.Second, 1, 75*time.Second + publicip.RequestTimeout + cloudflare.RequestTimeout},
		{30 * time.Second, 2, 75*time.Second + 2*publicip.RequestTimeout + cloudflare.RequestTimeout},
		{5 * time.Minute, 7, 12*time.Minute + 30*time.Second + 7*publicip.RequestTimeout + cloudflare.RequestTimeout},
	}
	for _, tt := range tests {
		if got := healthStaleAfter(tt.interval, tt.providers); got != tt.want {
			t.Errorf("healthStaleAfter(%v, %d) = %v, want %v", tt.interval, tt.providers, got, tt.want)
		}
	}
}
