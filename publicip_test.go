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

// stubSource returns ip and err, counting calls.
type stubSource struct {
	ip    string
	err   error
	calls int
}

func (s *stubSource) Current(context.Context) (string, error) {
	s.calls++
	return s.ip, s.err
}

func TestFallbackLookupUsesFirstSuccess(t *testing.T) {
	primary := &stubSource{ip: "1.1.1.1"}
	secondary := &stubSource{ip: "2.2.2.2"}

	got, err := (&fallbackLookup{logger: discardLogger(), sources: []ipSource{primary, secondary}}).Current(context.Background())
	if err != nil || got != "1.1.1.1" || secondary.calls != 0 {
		t.Errorf("Current() = %q, %v, secondary calls %d; want primary only", got, err, secondary.calls)
	}
}

func TestFallbackLookupFallsBackAndWarns(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	primary := &stubSource{err: errors.New("primary down")}
	secondary := &stubSource{ip: "2.2.2.2"}

	got, err := (&fallbackLookup{logger: logger, sources: []ipSource{primary, secondary}}).Current(context.Background())
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

	_, err := (&fallbackLookup{logger: logger, sources: sources}).Current(context.Background())
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

	_, err := (&fallbackLookup{logger: discardLogger(), sources: sources}).Current(ctx)
	if err == nil || secondary.calls != 0 {
		t.Errorf("err = %v, secondary calls %d; want error and no fallback", err, secondary.calls)
	}
}

func TestNewPublicIPSourceUsesProvidersInOrder(t *testing.T) {
	var urls []string
	for _, src := range newPublicIPSource(discardLogger(), http.DefaultClient, "ipify", "cloudflare").sources {
		urls = append(urls, src.(*publicIPLookup).url)
	}
	want := []string{ipifyURL, cloudflareTraceURL}
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
