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
	// cloudflareTraceURL is the primary IP source. Its IPv4 literal avoids a DNS
	// lookup and guarantees the request goes over IPv4.
	cloudflareTraceURL = "https://1.1.1.1/cdn-cgi/trace"
	// icanhazipURL is the fallback IP source. Cloudflare runs it too.
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

// plainTextIP reads a body that holds only the address, as icanhazip returns.
func plainTextIP(body string) string { return strings.TrimSpace(body) }

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

// newPublicIPSource asks Cloudflare first and falls back to icanhazip.
func newPublicIPSource(logger *slog.Logger, client *http.Client) *fallbackLookup {
	return &fallbackLookup{logger: logger, sources: []ipSource{
		&publicIPLookup{client: client, url: cloudflareTraceURL, extract: traceIP},
		&publicIPLookup{client: client, url: icanhazipURL, extract: plainTextIP},
	}}
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
	for _, src := range f.sources {
		ip, err := src.Current(ctx)
		if err == nil {
			return ip, nil
		}
		if ctx.Err() != nil {
			return "", err
		}
		f.logger.Warn("public IP source failed", "error", err)
		errs = append(errs, err)
	}
	return "", errors.Join(errs...)
}
