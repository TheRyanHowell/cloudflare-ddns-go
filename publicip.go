package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	// cloudflareTraceURL uses a hostname because the bare 1.1.1.1 host
	// intermittently returns error 1034.
	cloudflareTraceURL = "https://api.cloudflare.com/cdn-cgi/trace"
	// cloudflareTraceIPURL is tried only when api.cloudflare.com cannot be resolved.
	cloudflareTraceIPURL = "https://1.1.1.1/cdn-cgi/trace"
	amazonCheckIPURL     = "https://checkip.amazonaws.com/"
	// ipifyURL is ipify's IPv4-only host.
	ipifyURL = "https://api4.ipify.org/"
	// icanhazipURL is run by Cloudflare.
	icanhazipURL = "https://icanhazip.com/"
	// opendnsDoHURL asks OpenDNS over DNS-over-HTTPS for the A record of
	// myip.opendns.com, which it answers with the client's address. The dns
	// parameter is a fixed RFC 8484 query: ID 0, recursion desired, A IN.
	opendnsDoHURL   = "https://doh.opendns.com/dns-query?dns=AAABAAABAAAAAAAABG15aXAHb3BlbmRucwNjb20AAAEAAQ"
	opendnsMyIPName = "myip.opendns.com."
	// dnsMessageType is the media type of a DNS-over-HTTPS response.
	dnsMessageType = "application/dns-message"
	// ripeURL's sourceapp parameter identifies the client, as RIPEstat asks.
	ripeURL    = "https://stat.ripe.net/data/whats-my-ip/data.json?sourceapp=cloudflare-ddns-go"
	hetznerURL = "https://ip.hetzner.com/"
	mullvadURL = "https://am.i.mullvad.net/json"
	akamaiURL  = "https://whatismyip.akamai.com/"
	ipinfoURL  = "https://ipinfo.io/ip"
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
	client *http.Client
	ipProvider
}

// Current returns the public IPv4 address in dotted form, retrying at
// dnsFallbackURL when the main host's name cannot be resolved.
func (l *publicIPLookup) Current(ctx context.Context) (string, error) {
	ip, err := l.fetchIP(ctx, l.url)
	var dnsErr *net.DNSError
	if err != nil && l.dnsFallbackURL != "" && errors.As(err, &dnsErr) {
		fallbackIP, fallbackErr := l.fetchIP(ctx, l.dnsFallbackURL)
		if fallbackErr != nil {
			// Only the fallback's error is wrapped, so it alone decides how the
			// failure is classified.
			return "", fmt.Errorf("%v; fallback: %w", err, fallbackErr)
		}
		return fallbackIP, nil
	}
	return ip, err
}

func (l *publicIPLookup) fetchIP(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build public IP request: %w", err)
	}
	if l.accept != "" {
		req.Header.Set("Accept", l.accept)
	}
	body, err := fetch(l.client, req)
	if err != nil {
		return "", fmt.Errorf("get public IP: %w", err)
	}
	text := l.extract(string(body))
	addr, err := netip.ParseAddr(text)
	if err != nil || !addr.Unmap().Is4() {
		return "", fmt.Errorf("public IP from %s: %.*q is not an IPv4 address", url, maxQuotedBodyChars, text)
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
	url            string
	dnsFallbackURL string
	extract        func(body string) string
	accept         string
}

// namedProvider is an ipProvider and the name IP_PROVIDERS uses for it.
type namedProvider struct {
	name string
	ipProvider
}

// ipProviders are the services IP_PROVIDERS can name, in the default order.
var ipProviders = []namedProvider{
	{"cloudflare", ipProvider{url: cloudflareTraceURL, dnsFallbackURL: cloudflareTraceIPURL, extract: traceIP}},
	{"amazon", ipProvider{url: amazonCheckIPURL, extract: strings.TrimSpace}},
	{"akamai", ipProvider{url: akamaiURL, extract: strings.TrimSpace}},
	{"opendns", ipProvider{url: opendnsDoHURL, extract: dohIPv4, accept: dnsMessageType}},
	{"ripe", ipProvider{url: ripeURL, extract: jsonIP("data", "ip")}},
	{"hetzner", ipProvider{url: hetznerURL, extract: strings.TrimSpace}},
	{"mullvad", ipProvider{url: mullvadURL, extract: jsonIP("ip")}},
	{"ipinfo", ipProvider{url: ipinfoURL, extract: strings.TrimSpace}},
	{"ipify", ipProvider{url: ipifyURL, extract: strings.TrimSpace}},
	{"icanhazip", ipProvider{url: icanhazipURL, extract: strings.TrimSpace}},
}

// providerNames returns every provider name in the default order.
func providerNames() []string {
	names := make([]string, len(ipProviders))
	for i, p := range ipProviders {
		names[i] = p.name
	}
	return names
}

// findProvider returns the provider called name.
func findProvider(name string) (ipProvider, bool) {
	i := slices.IndexFunc(ipProviders, func(p namedProvider) bool { return p.name == name })
	if i < 0 {
		return ipProvider{}, false
	}
	return ipProviders[i].ipProvider, true
}

// newPublicIPSource tries the named providers in order. Each name must be in
// ipProviders.
func newPublicIPSource(logger *slog.Logger, client *http.Client, names []string) *fallbackLookup {
	sources := make([]ipSource, 0, len(names))
	for _, name := range names {
		p, _ := findProvider(name)
		sources = append(sources, &publicIPLookup{client: client, ipProvider: p})
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
	wait := max(cooldown(ts.failures), retryAfter(err))
	ts.until = now.Add(wait)
	return wait
}

// retryAfter returns the Retry-After delay carried by err, or 0.
func retryAfter(err error) time.Duration {
	var se *statusError
	if errors.As(err, &se) {
		return se.retryAfter
	}
	return 0
}

// sourceFailure is a source and the error it returned during one lookup.
type sourceFailure struct {
	ts  *trackedSource
	err error
}

// Current returns the first answer from a source that is not cooling down,
// logging each failure, or every error joined if all the sources it tried
// failed. When every source fails, transport errors start no cooldown, since
// the local network is the likely cause.
func (f *fallbackLookup) Current(ctx context.Context) (string, error) {
	now := f.now()
	var failures []sourceFailure
	for _, ts := range f.sources {
		if now.Before(ts.until) {
			continue
		}
		ip, err := ts.src.Current(ctx)
		if err == nil {
			for _, sf := range failures {
				f.settle(sf, now, true)
			}
			ts.failures, ts.until = 0, time.Time{}
			return ip, nil
		}
		if ctx.Err() != nil {
			return "", err
		}
		failures = append(failures, sourceFailure{ts: ts, err: err})
	}
	if len(failures) == 0 {
		return "", fmt.Errorf("%w; next retry in %s", errProvidersCoolingDown, f.nextRetry(now).Round(time.Second))
	}
	errs := make([]error, len(failures))
	for i, sf := range failures {
		errs[i] = sf.err
		f.settle(sf, now, !isTransportError(sf.err))
	}
	return "", errors.Join(errs...)
}

// settle logs a failure and, when charge is true, starts its cooldown.
func (f *fallbackLookup) settle(sf sourceFailure, now time.Time, charge bool) {
	if !charge {
		f.logger.Warn("public IP source failed", "error", sf.err)
		return
	}
	wait := sf.ts.recordFailure(now, sf.err)
	f.logger.Warn("public IP source failed", "error", sf.err, "retry_in", wait)
}

// nextRetry returns how long until the first source's cooldown ends.
func (f *fallbackLookup) nextRetry(now time.Time) time.Duration {
	first := slices.MinFunc(f.sources, func(a, b *trackedSource) int { return a.until.Compare(b.until) })
	return first.until.Sub(now)
}

// dohIPv4 reads the A record for myip.opendns.com from a DNS-over-HTTPS
// response, or returns "" when there is none.
func dohIPv4(body string) string {
	var p dnsmessage.Parser
	hdr, err := p.Start([]byte(body))
	if err != nil || !hdr.Response || hdr.RCode != dnsmessage.RCodeSuccess {
		return ""
	}
	if err := p.SkipAllQuestions(); err != nil {
		return ""
	}
	answers, err := p.AllAnswers()
	if err != nil {
		return ""
	}
	for _, rr := range answers {
		a, ok := rr.Body.(*dnsmessage.AResource)
		if ok && strings.EqualFold(rr.Header.Name.String(), opendnsMyIPName) {
			return netip.AddrFrom4(a.A).String()
		}
	}
	return ""
}

// jsonIP returns an extractor that reads the string at path in a JSON object,
// or "" when it is missing or not a string.
func jsonIP(path ...string) func(string) string {
	return func(body string) string {
		var v any
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return ""
		}
		for _, key := range path {
			obj, ok := v.(map[string]any)
			if !ok {
				return ""
			}
			v = obj[key]
		}
		ip, _ := v.(string)
		return ip
	}
}
