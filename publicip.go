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
	akamaiURL    = "https://whatismyip.akamai.com/"
	ipinfoURL    = "https://ipinfo.io/ip"
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

// ipProviders are the services IP_PROVIDERS can name.
var ipProviders = map[string]ipProvider{
	"cloudflare": {url: cloudflareTraceURL, extract: traceIP},
	"amazon":     {url: amazonCheckIPURL, extract: strings.TrimSpace},
	"ipify":      {url: ipifyURL, extract: strings.TrimSpace},
	"icanhazip":  {url: icanhazipURL, extract: strings.TrimSpace},
	"akamai":     {url: akamaiURL, extract: strings.TrimSpace},
	"ipinfo":     {url: ipinfoURL, extract: strings.TrimSpace},
}

// newPublicIPSource tries the named providers in order. Each name must be a
// key of ipProviders.
func newPublicIPSource(logger *slog.Logger, client *http.Client, names []string) *fallbackLookup {
	sources := make([]ipSource, 0, len(names))
	for _, name := range names {
		p := ipProviders[name]
		sources = append(sources, &publicIPLookup{client: client, url: p.url, extract: p.extract})
	}
	return newFallbackLookup(logger, sources...)
}

// fallbackLookup tries each source in order and returns the first address. A
// source that fails is skipped until its cooldown ends. It is not safe for
// concurrent use.
type fallbackLookup struct {
	logger  *slog.Logger
	sources []*trackedSource
	now     func() time.Time
}

// trackedSource remembers a source's consecutive failures and when it may be
// tried again.
type trackedSource struct {
	src      ipSource
	failures int
	until    time.Time
}

const (
	// baseCooldown is how long a source is skipped after one failure. It
	// doubles with each consecutive failure, up to maxCooldown.
	baseCooldown = time.Minute
	maxCooldown  = time.Hour
)

// errProvidersCoolingDown is returned, without making any requests, when every
// source is cooling down.
var errProvidersCoolingDown = errors.New("every public IP provider is cooling down after failures")

// cooldown returns how long to skip a source after failures consecutive failures.
func cooldown(failures int) time.Duration {
	d := baseCooldown
	for i := 1; i < failures && d < maxCooldown; i++ {
		d *= 2
	}
	return min(d, maxCooldown)
}

func newFallbackLookup(logger *slog.Logger, sources ...ipSource) *fallbackLookup {
	f := &fallbackLookup{logger: logger, now: time.Now}
	for _, src := range sources {
		f.sources = append(f.sources, &trackedSource{src: src})
	}
	return f
}

// recordFailure starts a cooldown, honouring a longer Retry-After, and returns it.
func (ts *trackedSource) recordFailure(now time.Time, err error) time.Duration {
	ts.failures++
	wait := cooldown(ts.failures)
	var se *statusError
	if errors.As(err, &se) {
		wait = max(wait, se.retryAfter)
	}
	ts.until = now.Add(wait)
	return wait
}

// Current returns the first answer from a source that is not cooling down,
// logging each failure that is covered by a later source, or every error
// joined if all the sources it tried failed.
func (f *fallbackLookup) Current(ctx context.Context) (string, error) {
	now := f.now()
	var errs []error
	for i, ts := range f.sources {
		if now.Before(ts.until) {
			continue
		}
		ip, err := ts.src.Current(ctx)
		if err == nil {
			ts.failures, ts.until = 0, time.Time{}
			return ip, nil
		}
		if ctx.Err() != nil {
			return "", err
		}
		wait := ts.recordFailure(now, err)
		if i < len(f.sources)-1 {
			f.logger.Warn("public IP source failed", "error", err, "retry_in", wait)
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return "", fmt.Errorf("%w; next retry in %s", errProvidersCoolingDown, f.nextRetry(now).Round(time.Second))
	}
	return "", errors.Join(errs...)
}

// nextRetry returns how long until the first source's cooldown ends.
func (f *fallbackLookup) nextRetry(now time.Time) time.Duration {
	next := f.sources[0].until
	for _, ts := range f.sources[1:] {
		if ts.until.Before(next) {
			next = ts.until
		}
	}
	return next.Sub(now)
}
