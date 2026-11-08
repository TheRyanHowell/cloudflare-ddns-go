// Package publicip finds this host's public IPv4 address by asking a list of
// providers in order, skipping those that recently failed.
package publicip

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

	"github.com/TheRyanHowell/cloudflare-ddns-go/internal/fetch"
	"golang.org/x/net/dns/dnsmessage"
)

const (
	// The bare 1.1.1.1 host intermittently returns error 1034.
	cloudflareTraceURL   = "https://api.cloudflare.com/cdn-cgi/trace"
	cloudflareTraceIPURL = "https://1.1.1.1/cdn-cgi/trace"
	amazonCheckIPURL     = "https://checkip.amazonaws.com/"
	ipifyURL             = "https://api4.ipify.org/"
	icanhazipURL         = "https://icanhazip.com/"
	// The dns parameter is a fixed RFC 8484 query: ID 0, recursion desired, A IN myip.opendns.com.
	opendnsDoHURL   = "https://doh.opendns.com/dns-query?dns=AAABAAABAAAAAAAABG15aXAHb3BlbmRucwNjb20AAAEAAQ"
	opendnsMyIPName = "myip.opendns.com."
	dnsMessageType  = "application/dns-message"
	ripeURL         = "https://stat.ripe.net/data/whats-my-ip/data.json?sourceapp=cloudflare-ddns-go"
	hetznerURL      = "https://ip.hetzner.com/"
	mullvadURL      = "https://am.i.mullvad.net/json"
	akamaiURL       = "https://whatismyip.akamai.com/"
	ipinfoURL       = "https://ipinfo.io/ip"
	traceIPKey      = "ip="
	// maxQuotedBodyChars caps how much of an unexpected body is quoted in errors.
	maxQuotedBodyChars = 64
	// dialTimeout bounds establishing a connection to the IP service.
	dialTimeout = 10 * time.Second
	// RequestTimeout bounds each request of the default client.
	RequestTimeout = 30 * time.Second
	// defaultInterval is the sync interval cooldowns assume unless WithInterval is set.
	defaultInterval = 5 * time.Minute
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
	body, err := fetch.Body(l.client, req)
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

// ipProviders returns the services New can name, in the default order.
func ipProviders() []namedProvider {
	return []namedProvider{
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
}

// Providers returns every provider name in the default order.
func Providers() []string {
	providers := ipProviders()
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.name
	}
	return names
}

// findProvider returns the provider called name.
func findProvider(name string) (ipProvider, bool) {
	providers := ipProviders()
	i := slices.IndexFunc(providers, func(p namedProvider) bool { return p.name == name })
	if i < 0 {
		return ipProvider{}, false
	}
	return providers[i].ipProvider, true
}

// ipSource reports the current public IP.
type ipSource interface {
	Current(ctx context.Context) (string, error)
}

// options are the settings New applies.
type options struct {
	interval time.Duration
	logger   *slog.Logger
	client   *http.Client
}

// Option configures New.
type Option func(*options)

// WithInterval sets the sync interval that cooldowns are measured in.
func WithInterval(d time.Duration) Option { return func(o *options) { o.interval = d } }

// WithLogger sets the logger that provider failures are reported to.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithHTTPClient replaces the default client, which only dials over IPv4.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.client = c } }

// Errors Validate wraps for a bad name.
var (
	ErrUnknownProvider   = errors.New("unknown public IP provider")
	ErrDuplicateProvider = errors.New("public IP provider named more than once")
)

// Validate reports an error unless names is a non-empty list of distinct
// provider names.
func Validate(names []string) error {
	if len(names) == 0 {
		return errors.New("no public IP providers named")
	}
	for i, name := range names {
		if slices.Contains(names[:i], name) {
			return fmt.Errorf("%w: %q", ErrDuplicateProvider, name)
		}
		if _, ok := findProvider(name); !ok {
			return fmt.Errorf("%w %q, must be one of %s", ErrUnknownProvider, name, strings.Join(Providers(), ", "))
		}
	}
	return nil
}

// New returns a Lookup that tries the named providers in order.
func New(names []string, opts ...Option) (*Lookup, error) {
	if err := Validate(names); err != nil {
		return nil, err
	}
	o := options{interval: defaultInterval, logger: slog.Default()}
	for _, opt := range opts {
		opt(&o)
	}
	if o.client == nil {
		o.client = newIPv4Client(RequestTimeout)
	}
	sources := make([]ipSource, 0, len(names))
	for _, name := range names {
		p, _ := findProvider(name)
		sources = append(sources, &publicIPLookup{client: o.client, ipProvider: p})
	}
	return newLookup(o.logger, o.interval, sources...), nil
}

// Lookup tries each source in order and returns the first address. A
// source that fails is skipped until its cooldown ends, unless it is the only
// one. It is not safe for concurrent use.
type Lookup struct {
	logger   *slog.Logger
	sources  []*trackedSource
	now      func() time.Time
	interval time.Duration
}

// trackedSource remembers a source's consecutive failures and when it may be
// tried again.
type trackedSource struct {
	src      ipSource
	failures int
	until    time.Time
	// unreachable holds the error of a source skipped after a lookup stopped
	// early; the source is charged a failure once another source answers.
	unreachable error
}

// maxUnreachablePerLookup is how many sources in a row may be unreachable
// before a lookup stops, since that points to a local outage.
const maxUnreachablePerLookup = 3

// maxCooldown caps a source's cooldown, unless the sync interval is longer.
const maxCooldown = time.Hour

// ErrProvidersCoolingDown is returned, without making any requests, when every
// source is cooling down.
var ErrProvidersCoolingDown = errors.New("every public IP provider is cooling down after failures")

// cooldown returns how long to skip a source after failures consecutive
// failures: the next 1, 2, 4… syncs, up to maxCooldown or one interval. Half an
// interval of slack keeps the following sync inside the cooldown.
func cooldown(failures int, interval time.Duration) time.Duration {
	limit := max(maxCooldown, interval)
	d := interval
	for i := 1; i < failures && d < limit; i++ {
		d *= 2
	}
	return min(d, limit) + interval/2
}

func newLookup(logger *slog.Logger, interval time.Duration, sources ...ipSource) *Lookup {
	f := &Lookup{logger: logger, now: time.Now, interval: interval}
	for _, src := range sources {
		f.sources = append(f.sources, &trackedSource{src: src})
	}
	return f
}

// reset clears the source's failures after a success.
func (ts *trackedSource) reset() {
	ts.failures, ts.until, ts.unreachable = 0, time.Time{}, nil
}

// skipOneSync skips the source for the next sync without counting a failure.
func (ts *trackedSource) skipOneSync(now time.Time, interval time.Duration, err error) {
	ts.until = now.Add(cooldown(1, interval))
	ts.unreachable = err
}

// chargeUnreachable counts a failure for each source skipped as unreachable,
// now that another source has shown the network works.
func (f *Lookup) chargeUnreachable(now time.Time) {
	for _, ts := range f.sources {
		if err := ts.unreachable; err != nil {
			wait := ts.recordFailure(now, f.interval, err)
			f.logger.Warn("public IP source was unreachable while others answered", "error", err, "retry_in", wait)
		}
	}
}

// recordFailure starts a cooldown, honouring a longer Retry-After, and returns it.
func (ts *trackedSource) recordFailure(now time.Time, interval time.Duration, err error) time.Duration {
	ts.failures++
	ts.unreachable = nil
	wait := max(cooldown(ts.failures, interval), fetch.RetryAfter(err))
	ts.until = now.Add(wait)
	return wait
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
func (f *Lookup) Current(ctx context.Context) (string, error) {
	now := f.now()
	var failures []sourceFailure
	unreachable := 0
	for i, ts := range f.sources {
		if now.Before(ts.until) {
			continue
		}
		ip, err := ts.src.Current(ctx)
		if err == nil {
			f.succeeded(ts, now, failures)
			return ip, nil
		}
		if ctx.Err() != nil {
			return "", err
		}
		failures = append(failures, sourceFailure{ts: ts, err: err})
		if !fetch.IsTransportError(err) {
			unreachable = 0
			continue
		}
		if unreachable++; unreachable == maxUnreachablePerLookup && f.untriedAfter(i, now) {
			return "", f.failed(now, failures, failures[len(failures)-unreachable:])
		}
	}
	return "", f.failed(now, failures, nil)
}

// succeeded settles a lookup that ts answered.
func (f *Lookup) succeeded(ts *trackedSource, now time.Time, failures []sourceFailure) {
	for _, sf := range failures {
		f.settle(sf, now, true)
	}
	ts.reset()
	f.chargeUnreachable(now)
}

// failed settles a lookup that no source answered. skipped are the unreachable
// sources a stop leaves out of the next sync.
func (f *Lookup) failed(now time.Time, failures, skipped []sourceFailure) error {
	if len(failures) == 0 {
		return fmt.Errorf("%w; next retry in %s", ErrProvidersCoolingDown, f.nextRetry(now).Round(time.Second))
	}
	errs := make([]error, len(failures))
	for i, sf := range failures {
		errs[i] = sf.err
		f.settle(sf, now, !fetch.IsTransportError(sf.err))
	}
	for _, sf := range skipped {
		sf.ts.skipOneSync(now, f.interval, sf.err)
	}
	return errors.Join(errs...)
}

// untriedAfter reports whether a source after index i is not cooling down.
func (f *Lookup) untriedAfter(i int, now time.Time) bool {
	return slices.ContainsFunc(f.sources[i+1:], func(ts *trackedSource) bool { return !now.Before(ts.until) })
}

// settle logs a failure and, when charge is true, starts its cooldown. A lone
// source is never put on cooldown.
func (f *Lookup) settle(sf sourceFailure, now time.Time, charge bool) {
	if len(f.sources) == 1 {
		// A cooldown would only fail the next sync, as there is nothing to fall
		// back to, so only a Retry-After the provider sent applies.
		wait := fetch.RetryAfter(sf.err)
		sf.ts.until = now.Add(wait)
		f.logger.Warn("public IP source failed", "error", sf.err, "retry_in", wait)
		return
	}
	if !charge {
		sf.ts.unreachable = nil
		f.logger.Warn("public IP source failed", "error", sf.err)
		return
	}
	wait := sf.ts.recordFailure(now, f.interval, sf.err)
	f.logger.Warn("public IP source failed", "error", sf.err, "retry_in", wait)
}

// nextRetry returns how long until the first source's cooldown ends.
func (f *Lookup) nextRetry(now time.Time) time.Duration {
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
