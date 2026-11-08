package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPublicIPLookupCurrent(t *testing.T) {
	tests := []struct {
		name    string
		extract func(string) string
		body    string
		status  int
		want    string
		wantErr bool
	}{
		{name: "ok", extract: strings.TrimSpace, body: "9.8.7.6\n", status: http.StatusOK, want: "9.8.7.6"},
		{name: "ipv4 mapped", extract: strings.TrimSpace, body: "::ffff:1.2.3.4", status: http.StatusOK, want: "1.2.3.4"},
		{name: "ipv6", extract: strings.TrimSpace, body: "2001:db8::1", status: http.StatusOK, wantErr: true},
		{name: "garbage", extract: strings.TrimSpace, body: "hello", status: http.StatusOK, wantErr: true},
		{name: "status", extract: strings.TrimSpace, status: http.StatusInternalServerError, wantErr: true},
		{name: "trace", extract: traceIP, body: "fl=1\nh=1.1.1.1\nip=9.8.7.6\nts=1\n", status: http.StatusOK, want: "9.8.7.6"},
		{name: "trace ipv6", extract: traceIP, body: "ip=2001:db8::1\n", status: http.StatusOK, wantErr: true},
		{name: "trace without ip", extract: traceIP, body: "fl=1\nh=1.1.1.1\n", status: http.StatusOK, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			got, err := (&publicIPLookup{client: srv.Client(), url: srv.URL, extract: tt.extract}).Current(context.Background())
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("Current() = %q, %v; want %q, wantErr %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestPublicIPLookupRequestErrors(t *testing.T) {
	for _, url := range []string{"http://127.0.0.1:1", "http://bad host"} {
		if _, err := (&publicIPLookup{client: http.DefaultClient, url: url, extract: strings.TrimSpace}).Current(context.Background()); err == nil {
			t.Errorf("url %q: expected error", url)
		}
	}
}

func TestPublicIPLookupTruncatesBodyInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, strings.Repeat("x", 4096))
	}))
	defer srv.Close()

	_, err := (&publicIPLookup{client: srv.Client(), url: srv.URL, extract: strings.TrimSpace}).Current(context.Background())
	if err == nil || len(err.Error()) > 200+len(srv.URL) || !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("err = %v, want short error naming %s", err, srv.URL)
	}
}

func TestNewIPv4ClientDialsIPv4(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "7.7.7.7")
	}))
	defer srv.Close()

	got, err := (&publicIPLookup{client: newIPv4Client(time.Second), url: srv.URL, extract: strings.TrimSpace}).Current(context.Background())
	if err != nil || got != "7.7.7.7" {
		t.Errorf("Current() = %q, %v", got, err)
	}
}

func TestTraceIP(t *testing.T) {
	tests := map[string]string{
		"ip=1.2.3.4":                 "1.2.3.4",
		"fl=1\nip= 1.2.3.4 \nts=2\n": "1.2.3.4",
		"fl=1\r\nip=1.2.3.4\r\n":     "1.2.3.4",
		"fl=1\nvip=5.6.7.8\n":        "",
		"":                           "",
	}
	for body, want := range tests {
		if got := traceIP(body); got != want {
			t.Errorf("traceIP(%q) = %q, want %q", body, got, want)
		}
	}
}

// stubSource returns errs one per call, then ip and err, counting calls.
type stubSource struct {
	ip    string
	err   error
	errs  []error
	calls int
}

func (s *stubSource) Current(context.Context) (string, error) {
	s.calls++
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		if err != nil {
			return "", err
		}
		return s.ip, nil
	}
	return s.ip, s.err
}

// newTestFallback returns a fallbackLookup whose clock reads *now.
func newTestFallback(now *time.Time, sources ...ipSource) *fallbackLookup {
	f := newFallbackLookup(discardLogger(), sources...)
	f.now = func() time.Time { return *now }
	return f
}

var testEpoch = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func TestFallbackLookupUsesFirstSuccess(t *testing.T) {
	primary := &stubSource{ip: "1.1.1.1"}
	secondary := &stubSource{ip: "2.2.2.2"}

	got, err := newFallbackLookup(discardLogger(), primary, secondary).Current(context.Background())
	if err != nil || got != "1.1.1.1" || secondary.calls != 0 {
		t.Errorf("Current() = %q, %v, secondary calls %d; want primary only", got, err, secondary.calls)
	}
}

func TestFallbackLookupFallsBackAndWarns(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	primary := &stubSource{err: errors.New("primary down")}
	secondary := &stubSource{ip: "2.2.2.2"}

	got, err := newFallbackLookup(logger, primary, secondary).Current(context.Background())
	if err != nil || got != "2.2.2.2" {
		t.Errorf("Current() = %q, %v; want 2.2.2.2", got, err)
	}
	if !strings.Contains(logs.String(), "primary down") {
		t.Errorf("logs %q do not mention the primary failure", logs.String())
	}
}

func TestFallbackLookupAllFail(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	errA, errB := errors.New("a down"), errors.New("b down")
	sources := []ipSource{&stubSource{err: errA}, &stubSource{err: errB}}

	_, err := newFallbackLookup(logger, sources...).Current(context.Background())
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Errorf("err = %v, want both source errors", err)
	}
	if strings.Contains(logs.String(), "b down") {
		t.Errorf("logs %q repeat the last failure, which the caller logs", logs.String())
	}
}

func TestFallbackLookupStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	secondary := &stubSource{ip: "2.2.2.2"}
	sources := []ipSource{&stubSource{err: context.Canceled}, secondary}

	_, err := newFallbackLookup(discardLogger(), sources...).Current(ctx)
	if err == nil || secondary.calls != 0 {
		t.Errorf("err = %v, secondary calls %d; want error and no fallback", err, secondary.calls)
	}
}

func TestNewPublicIPSourceUsesProvidersInOrder(t *testing.T) {
	var urls []string
	for _, src := range newPublicIPSource(discardLogger(), http.DefaultClient, []string{"ipify", "cloudflare", "akamai"}).sources {
		urls = append(urls, src.src.(*publicIPLookup).url)
	}
	want := []string{ipifyURL, cloudflareTraceURL, akamaiURL}
	if !slices.Equal(urls, want) {
		t.Errorf("source order = %v, want %v", urls, want)
	}
}

func TestIPProvidersParseTheirFormat(t *testing.T) {
	bodies := map[string]string{
		"cloudflare": "fl=1\nh=1.1.1.1\nip=9.8.7.6\nts=1\n",
		"amazon":     "9.8.7.6\n",
		"ipify":      "9.8.7.6",
		"icanhazip":  "9.8.7.6\n",
		"akamai":     "9.8.7.6",
		"ipinfo":     "9.8.7.6",
	}
	if len(bodies) != len(ipProviders) {
		t.Fatalf("test covers %d providers, want all %d", len(bodies), len(ipProviders))
	}
	for name, body := range bodies {
		p, ok := ipProviders[name]
		if !ok {
			t.Errorf("provider %q missing", name)
			continue
		}
		if !strings.HasPrefix(p.url, "https://") {
			t.Errorf("provider %q url %q is not HTTPS", name, p.url)
		}
		if got := p.extract(body); got != "9.8.7.6" {
			t.Errorf("provider %q extracted %q from %q", name, got, body)
		}
	}
}

func TestCooldown(t *testing.T) {
	tests := map[int]time.Duration{
		1:    baseCooldown,
		2:    2 * baseCooldown,
		3:    4 * baseCooldown,
		7:    maxCooldown,
		1000: maxCooldown,
	}
	for failures, want := range tests {
		if got := cooldown(failures); got != want {
			t.Errorf("cooldown(%d) = %v, want %v", failures, got, want)
		}
	}
}

func TestFallbackLookupSkipsProviderInCooldown(t *testing.T) {
	now := testEpoch
	primary := &stubSource{err: errFake}
	f := newTestFallback(&now, primary, &stubSource{ip: "2.2.2.2"})

	for _, step := range []struct {
		at        time.Duration
		wantCalls int
	}{{0, 1}, {baseCooldown - time.Second, 1}, {baseCooldown, 2}} {
		now = testEpoch.Add(step.at)
		if got, err := f.Current(context.Background()); err != nil || got != "2.2.2.2" {
			t.Fatalf("at %v: Current() = %q, %v", step.at, got, err)
		}
		if primary.calls != step.wantCalls {
			t.Errorf("at %v: primary calls = %d, want %d", step.at, primary.calls, step.wantCalls)
		}
	}
}

func TestFallbackLookupBacksOffOnRepeatedFailure(t *testing.T) {
	now := testEpoch
	primary := &stubSource{err: errFake}
	f := newTestFallback(&now, primary, &stubSource{ip: "2.2.2.2"})

	// Failures at 0 and 1m give cooldowns of 1m then 2m, so 3m is the next try.
	for _, step := range []struct {
		at        time.Duration
		wantCalls int
	}{{0, 1}, {time.Minute, 2}, {3*time.Minute - time.Second, 2}, {3 * time.Minute, 3}} {
		now = testEpoch.Add(step.at)
		if _, err := f.Current(context.Background()); err != nil {
			t.Fatalf("at %v: %v", step.at, err)
		}
		if primary.calls != step.wantCalls {
			t.Errorf("at %v: primary calls = %d, want %d", step.at, primary.calls, step.wantCalls)
		}
	}
}

func TestFallbackLookupSuccessResetsBackoff(t *testing.T) {
	now := testEpoch
	primary := &stubSource{ip: "1.1.1.1", errs: []error{errFake, errFake, nil, errFake}}
	f := newTestFallback(&now, primary, &stubSource{ip: "2.2.2.2"})

	for _, at := range []time.Duration{0, time.Minute, 3 * time.Minute, 3 * time.Minute} {
		now = testEpoch.Add(at)
		if _, err := f.Current(context.Background()); err != nil {
			t.Fatalf("at %v: %v", at, err)
		}
	}
	// The failure after the success starts again at the base cooldown.
	now = testEpoch.Add(3*time.Minute + baseCooldown)
	if _, err := f.Current(context.Background()); err != nil || primary.calls != 5 {
		t.Errorf("err = %v, primary calls = %d, want 5", err, primary.calls)
	}
}

func TestFallbackLookupHonoursRetryAfter(t *testing.T) {
	now := testEpoch
	limited := &statusError{code: http.StatusTooManyRequests, status: "429 Too Many Requests", retryAfter: 10 * time.Minute}
	primary := &stubSource{err: limited}
	f := newTestFallback(&now, primary, &stubSource{ip: "2.2.2.2"})

	for _, step := range []struct {
		at        time.Duration
		wantCalls int
	}{{0, 1}, {10*time.Minute - time.Second, 1}, {10 * time.Minute, 2}} {
		now = testEpoch.Add(step.at)
		if _, err := f.Current(context.Background()); err != nil {
			t.Fatalf("at %v: %v", step.at, err)
		}
		if primary.calls != step.wantCalls {
			t.Errorf("at %v: primary calls = %d, want %d", step.at, primary.calls, step.wantCalls)
		}
	}
}

func TestFallbackLookupAllCoolingDownMakesNoRequests(t *testing.T) {
	now := testEpoch
	a, b := &stubSource{err: errFake}, &stubSource{err: errFake}
	f := newTestFallback(&now, a, b)
	if _, err := f.Current(context.Background()); err == nil {
		t.Fatal("expected error")
	}

	now = testEpoch.Add(30 * time.Second)
	_, err := f.Current(context.Background())
	if !errors.Is(err, errProvidersCoolingDown) || a.calls != 1 || b.calls != 1 {
		t.Errorf("err = %v, calls = %d, %d; want cooling-down error and no new requests", err, a.calls, b.calls)
	}
}

func TestFallbackLookupCancellationIsNotAFailure(t *testing.T) {
	now := testEpoch
	primary := &stubSource{errs: []error{context.Canceled}, ip: "1.1.1.1"}
	f := newTestFallback(&now, primary, &stubSource{ip: "2.2.2.2"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Current(ctx); err == nil {
		t.Fatal("expected error")
	}
	if got, err := f.Current(context.Background()); err != nil || got != "1.1.1.1" {
		t.Errorf("Current() = %q, %v; want primary retried at once", got, err)
	}
}
