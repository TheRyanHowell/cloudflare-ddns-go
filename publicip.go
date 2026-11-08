package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	// cloudflareTraceURL's IPv4 literal avoids a DNS lookup and guarantees the
	// request goes over IPv4.
	cloudflareTraceURL = "https://1.1.1.1/cdn-cgi/trace"
	amazonCheckIPURL   = "https://checkip.amazonaws.com/"
	// ipifyURL is ipify's IPv4-only host.
	ipifyURL = "https://api4.ipify.org/"
	// icanhazipURL is run by Cloudflare.
	icanhazipURL = "https://icanhazip.com/"
	// traceIPKey prefixes the client address line in a Cloudflare trace.
	traceIPKey = "ip="
	// maxQuotedBodyChars caps how much of an unexpected body is quoted in errors.
	maxQuotedBodyChars = 64
	// dialTimeout bounds establishing a connection to the IP service.
	dialTimeout = 10 * time.Second
)

// newIPv4Client returns a client that only dials over IPv4, so a dual-stack
// host never learns an IPv6 address from the lookup service.
func newIPv4Client(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: dialTimeout}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp4", addr)
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// publicIPLookup discovers this host's public IPv4 address.
type publicIPLookup struct {
	client  *http.Client
	url     string
	extract func(body string) string
}

// Current returns the public IPv4 address in dotted form.
func (l *publicIPLookup) Current(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url, nil)
	if err != nil {
		return "", fmt.Errorf("build public IP request: %w", err)
	}
	body, err := fetch(l.client, req)
	if err != nil {
		return "", fmt.Errorf("get public IP: %w", err)
	}
	text := l.extract(string(body))
	addr, err := netip.ParseAddr(text)
	if err != nil || !addr.Unmap().Is4() {
		return "", fmt.Errorf("public IP from %s: %.*q is not an IPv4 address", l.url, maxQuotedBodyChars, text)
	}
	return addr.Unmap().String(), nil
}

// traceIP reads the address from the ip= line of a Cloudflare trace, or
// returns "" when there is none.
func traceIP(body string) string {
	for line := range strings.Lines(body) {
		if ip, ok := strings.CutPrefix(line, traceIPKey); ok {
			return strings.TrimSpace(ip)
		}
	}
	return ""
}

// ipProvider is a public IP service and how to read its response.
type ipProvider struct {
	url     string
	extract func(body string) string
}

// ipProviders are the services IP_PROVIDER and IP_FALLBACK_PROVIDER can name.
var ipProviders = map[string]ipProvider{
	"cloudflare": {url: cloudflareTraceURL, extract: traceIP},
	"amazon":     {url: amazonCheckIPURL, extract: strings.TrimSpace},
	"ipify":      {url: ipifyURL, extract: strings.TrimSpace},
	"icanhazip":  {url: icanhazipURL, extract: strings.TrimSpace},
}

// newPublicIPSource tries primary, then fallback. Both must be keys of ipProviders.
func newPublicIPSource(logger *slog.Logger, client *http.Client, primary, fallback string) *fallbackLookup {
	sources := make([]ipSource, 0, 2)
	for _, name := range []string{primary, fallback} {
		p := ipProviders[name]
		sources = append(sources, &publicIPLookup{client: client, url: p.url, extract: p.extract})
	}
	return &fallbackLookup{logger: logger, sources: sources}
}

// fallbackLookup tries each source in order and returns the first address.
type fallbackLookup struct {
	logger  *slog.Logger
	sources []ipSource
}

// Current returns the first source's answer, logging each failure that is
// covered by a later source, or every error joined if all sources fail.
func (f *fallbackLookup) Current(ctx context.Context) (string, error) {
	var errs []error
	for i, src := range f.sources {
		ip, err := src.Current(ctx)
		if err == nil {
			return ip, nil
		}
		if ctx.Err() != nil {
			return "", err
		}
		if i < len(f.sources)-1 {
			f.logger.Warn("public IP source failed", "error", err)
		}
		errs = append(errs, err)
	}
	return "", errors.Join(errs...)
}
