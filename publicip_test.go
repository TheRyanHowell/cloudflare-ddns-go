package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
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

			got, err := (&publicIPLookup{client: srv.Client(), ipProvider: ipProvider{url: srv.URL, extract: tt.extract}}).Current(context.Background())
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("Current() = %q, %v; want %q, wantErr %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestPublicIPLookupRequestErrors(t *testing.T) {
	for _, url := range []string{"http://127.0.0.1:1", "http://bad host"} {
		if _, err := (&publicIPLookup{client: http.DefaultClient, ipProvider: ipProvider{url: url, extract: strings.TrimSpace}}).Current(context.Background()); err == nil {
			t.Errorf("url %q: expected error", url)
		}
	}
}

func TestPublicIPLookupTruncatesBodyInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, strings.Repeat("x", 4096))
	}))
	defer srv.Close()

	_, err := (&publicIPLookup{client: srv.Client(), ipProvider: ipProvider{url: srv.URL, extract: strings.TrimSpace}}).Current(context.Background())
	if err == nil || len(err.Error()) > 200+len(srv.URL) || !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("err = %v, want short error naming %s", err, srv.URL)
	}
}

func TestNewIPv4ClientDialsIPv4(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "7.7.7.7")
	}))
	defer srv.Close()

	got, err := (&publicIPLookup{client: newIPv4Client(time.Second), ipProvider: ipProvider{url: srv.URL, extract: strings.TrimSpace}}).Current(context.Background())
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
	f := newFallbackLookup(discardLogger(), testInterval, sources...)
	f.now = func() time.Time { return *now }
	return f
}

// errUnreachable is a transport failure: no response arrived.
var errUnreachable = fmt.Errorf("get public IP: %w", &transportError{err: &url.Error{Op: "Get", URL: "https://example.test", Err: errFake}})

// testInterval is the sync interval the fallback tests assume.
const testInterval = 5 * time.Minute

var testEpoch = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func TestFallbackLookupUsesFirstSuccess(t *testing.T) {
	primary := &stubSource{ip: "1.1.1.1"}
	secondary := &stubSource{ip: "2.2.2.2"}

	got, err := newFallbackLookup(discardLogger(), testInterval, primary, secondary).Current(context.Background())
	if err != nil || got != "1.1.1.1" || secondary.calls != 0 {
		t.Errorf("Current() = %q, %v, secondary calls %d; want primary only", got, err, secondary.calls)
	}
}

func TestFallbackLookupFallsBackAndWarns(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	primary := &stubSource{err: errors.New("primary down")}
	secondary := &stubSource{ip: "2.2.2.2"}

	got, err := newFallbackLookup(logger, testInterval, primary, secondary).Current(context.Background())
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

	_, err := newFallbackLookup(logger, testInterval, sources...).Current(context.Background())
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Errorf("err = %v, want both source errors", err)
	}
	for _, want := range []string{"a down", "b down"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs %q do not mention %q", logs.String(), want)
		}
	}
}

func TestFallbackLookupStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	secondary := &stubSource{ip: "2.2.2.2"}
	sources := []ipSource{&stubSource{err: context.Canceled}, secondary}

	_, err := newFallbackLookup(discardLogger(), testInterval, sources...).Current(ctx)
	if err == nil || secondary.calls != 0 {
		t.Errorf("err = %v, secondary calls %d; want error and no fallback", err, secondary.calls)
	}
}

func TestNewPublicIPSourceUsesProvidersInOrder(t *testing.T) {
	var urls []string
	for _, src := range newPublicIPSource(discardLogger(), http.DefaultClient, []string{"ipify", "cloudflare", "akamai"}, testInterval).sources {
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
		"opendns":    dohResponse(t, dnsmessage.RCodeSuccess, "myip.opendns.com.", [4]byte{9, 8, 7, 6}),
		"ripe":       `{"status":"ok","data":{"ip":"9.8.7.6"}}`,
		"hetzner":    "9.8.7.6\n",
		"mullvad":    `{"ip":"9.8.7.6","mullvad_exit_ip":false}`,
	}
	if len(bodies) != len(ipProviders) {
		t.Fatalf("test covers %d providers, want all %d", len(bodies), len(ipProviders))
	}
	for name, body := range bodies {
		p, ok := findProvider(name)
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
	half := testInterval / 2
	tests := []struct {
		failures int
		interval time.Duration
		want     time.Duration
	}{
		{1, testInterval, testInterval + half},
		{2, testInterval, 2*testInterval + half},
		{3, testInterval, 4*testInterval + half},
		{1000, testInterval, maxCooldown + half},
		{1, 24 * time.Hour, 36 * time.Hour},
		{5, 24 * time.Hour, 36 * time.Hour},
	}
	for _, tt := range tests {
		if got := cooldown(tt.failures, tt.interval); got != tt.want {
			t.Errorf("cooldown(%d, %v) = %v, want %v", tt.failures, tt.interval, got, tt.want)
		}
	}
}

func TestFallbackLookupSkipsSyncsAfterFailures(t *testing.T) {
	now := testEpoch
	primary := &stubSource{err: errFake}
	f := newTestFallback(&now, primary, &stubSource{ip: "2.2.2.2"})

	// The first failure skips one sync, the second skips two.
	i := testInterval
	assertPrimaryCalls(t, f, &now, primary, []callStep{{0, 1}, {i, 1}, {2 * i, 2}, {3 * i, 2}, {4 * i, 2}, {5 * i, 3}})
}

func TestFallbackLookupSuccessResetsBackoff(t *testing.T) {
	now := testEpoch
	primary := &stubSource{ip: "1.1.1.1", errs: []error{errFake, errFake, nil, errFake}}
	f := newTestFallback(&now, primary, &stubSource{ip: "2.2.2.2"})

	i := testInterval
	for _, at := range []time.Duration{0, 2 * i, 5 * i, 5 * i} {
		now = testEpoch.Add(at)
		if _, err := f.Current(context.Background()); err != nil {
			t.Fatalf("at %v: %v", at, err)
		}
	}
	// The failure after the success skips only one sync again.
	now = testEpoch.Add(7 * i)
	if _, err := f.Current(context.Background()); err != nil || primary.calls != 5 {
		t.Errorf("err = %v, primary calls = %d, want 5", err, primary.calls)
	}
}

func TestFallbackLookupHonoursRetryAfter(t *testing.T) {
	now := testEpoch
	limited := &statusError{code: http.StatusTooManyRequests, status: "429 Too Many Requests", retryAfter: 10 * time.Minute}
	primary := &stubSource{err: limited}
	f := newTestFallback(&now, primary, &stubSource{ip: "2.2.2.2"})

	assertPrimaryCalls(t, f, &now, primary, []callStep{{0, 1}, {10*time.Minute - time.Second, 1}, {10 * time.Minute, 2}})
}

func TestFallbackLookupAllCoolingDownMakesNoRequests(t *testing.T) {
	now := testEpoch
	limited := &statusError{code: http.StatusTooManyRequests, status: "429 Too Many Requests", retryAfter: time.Hour}
	a, b := &stubSource{err: limited}, &stubSource{err: limited}
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

func TestFallbackLookupAllFailingStartsNoCooldown(t *testing.T) {
	// When every provider fails, the local network is the likely cause, so no
	// provider is charged and all are tried again at the next sync.
	now := testEpoch
	a, b := &stubSource{err: errUnreachable}, &stubSource{err: errUnreachable}
	f := newTestFallback(&now, a, b)
	for range 3 {
		if _, err := f.Current(context.Background()); err == nil {
			t.Fatal("expected error")
		}
		now = now.Add(30 * time.Second)
	}
	if a.calls != 3 || b.calls != 3 {
		t.Errorf("calls = %d, %d; want both tried at every sync", a.calls, b.calls)
	}
}

func TestFallbackLookupOutageDoesNotEscalateCooldowns(t *testing.T) {
	now := testEpoch
	primary := &stubSource{ip: "1.1.1.1", errs: []error{errUnreachable, errUnreachable, errUnreachable, errUnreachable}}
	secondary := &stubSource{ip: "2.2.2.2", errs: []error{errUnreachable, errUnreachable, errUnreachable}}
	f := newTestFallback(&now, primary, secondary)

	// Three syncs of total outage, then the secondary recovers.
	for range 3 {
		if _, err := f.Current(context.Background()); err == nil {
			t.Fatal("expected error during outage")
		}
		now = now.Add(5 * time.Minute)
	}
	if got, err := f.Current(context.Background()); err != nil || got != "2.2.2.2" {
		t.Fatalf("after outage: Current() = %q, %v; want 2.2.2.2", got, err)
	}
	// The primary was charged one failure, not four, so it skips one sync only.
	now = now.Add(testInterval)
	if got, err := f.Current(context.Background()); err != nil || got != "2.2.2.2" {
		t.Errorf("Current() = %q, %v; want the primary still skipped", got, err)
	}
	now = now.Add(testInterval)
	if got, err := f.Current(context.Background()); err != nil || got != "1.1.1.1" {
		t.Errorf("Current() = %q, %v; want the primary back", got, err)
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

func TestCloudflareProviderURLs(t *testing.T) {
	p := mustProvider(t, "cloudflare")
	if p.url != "https://api.cloudflare.com/cdn-cgi/trace" || p.dnsFallbackURL != "https://1.1.1.1/cdn-cgi/trace" {
		t.Errorf("cloudflare urls = %q, %q; want the hostname, falling back to the IP literal", p.url, p.dnsFallbackURL)
	}
}

// dohResponse builds a DNS response answering name with an A record for ip.
func dohResponse(t *testing.T, rcode dnsmessage.RCode, name string, ip [4]byte) string {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, RCode: rcode})
	n := dnsmessage.MustNewName(name)
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("myip.opendns.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}); err != nil {
		t.Fatal(err)
	}
	if err := b.StartAnswers(); err != nil {
		t.Fatal(err)
	}
	if rcode == dnsmessage.RCodeSuccess {
		hdr := dnsmessage.ResourceHeader{Name: n, Class: dnsmessage.ClassINET, TTL: 0}
		if err := b.AResource(hdr, dnsmessage.AResource{A: ip}); err != nil {
			t.Fatal(err)
		}
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return string(msg)
}

func TestDoHIPv4(t *testing.T) {
	ip := [4]byte{9, 8, 7, 6}
	notResponse := []byte(dohResponse(t, dnsmessage.RCodeSuccess, "myip.opendns.com.", ip))
	notResponse[2] &^= 0x80 // clear the QR (response) bit

	tests := map[string]struct {
		body string
		want string
	}{
		"answer":         {dohResponse(t, dnsmessage.RCodeSuccess, "myip.opendns.com.", ip), "9.8.7.6"},
		"name case":      {dohResponse(t, dnsmessage.RCodeSuccess, "MyIP.OpenDNS.com.", ip), "9.8.7.6"},
		"nxdomain":       {dohResponse(t, dnsmessage.RCodeNameError, "myip.opendns.com.", ip), ""},
		"other name":     {dohResponse(t, dnsmessage.RCodeSuccess, "example.com.", ip), ""},
		"not a response": {string(notResponse), ""},
		"garbage":        {"9.8.7.6", ""},
		"empty":          {"", ""},
	}
	for name, tt := range tests {
		if got := dohIPv4(tt.body); got != tt.want {
			t.Errorf("%s: dohIPv4() = %q, want %q", name, got, tt.want)
		}
	}
}

func TestOpenDNSQueryAsksForMyIP(t *testing.T) {
	u, err := url.Parse(mustProvider(t, "opendns").url)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "doh.opendns.com" || u.Path != "/dns-query" {
		t.Errorf("url = %s, want https://doh.opendns.com/dns-query", u)
	}
	raw, err := base64.RawURLEncoding.DecodeString(u.Query().Get("dns"))
	if err != nil {
		t.Fatal(err)
	}
	var p dnsmessage.Parser
	hdr, err := p.Start(raw)
	if err != nil {
		t.Fatal(err)
	}
	q, err := p.Question()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.ID != 0 || hdr.Response || !hdr.RecursionDesired || q.Name.String() != "myip.opendns.com." || q.Type != dnsmessage.TypeA || q.Class != dnsmessage.ClassINET {
		t.Errorf("query = %+v %+v, want ID 0 recursive A IN myip.opendns.com.", hdr, q)
	}
}

func TestPublicIPLookupSendsAccept(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept")
		io.WriteString(w, "9.8.7.6")
	}))
	defer srv.Close()

	l := &publicIPLookup{client: srv.Client(), ipProvider: ipProvider{url: srv.URL, extract: strings.TrimSpace, accept: dnsMessageType}}
	if _, err := l.Current(context.Background()); err != nil || got != dnsMessageType {
		t.Errorf("Accept = %q, err %v; want %q", got, err, dnsMessageType)
	}
}

func mustProvider(t *testing.T, name string) ipProvider {
	t.Helper()
	p, ok := findProvider(name)
	if !ok {
		t.Fatalf("provider %q missing", name)
	}
	return p
}

// callStep is a clock offset and the primary's expected call count after a lookup at it.
type callStep struct {
	at        time.Duration
	wantCalls int
}

// assertPrimaryCalls runs a lookup at each step and checks how often primary was asked.
func assertPrimaryCalls(t *testing.T, f *fallbackLookup, now *time.Time, primary *stubSource, steps []callStep) {
	t.Helper()
	for _, step := range steps {
		*now = testEpoch.Add(step.at)
		if _, err := f.Current(context.Background()); err != nil {
			t.Fatalf("at %v: %v", step.at, err)
		}
		if primary.calls != step.wantCalls {
			t.Errorf("at %v: primary calls = %d, want %d", step.at, primary.calls, step.wantCalls)
		}
	}
}

func TestFallbackLookupChargesProvidersThatRespondedDuringOutage(t *testing.T) {
	// A response proves the network works, so a provider that answered with an
	// error is charged even when every provider fails. Only transport errors
	// count as a local outage.
	now := testEpoch
	blocked := &stubSource{err: fmt.Errorf("get public IP: %w", &statusError{code: http.StatusForbidden, status: "403 Forbidden"})}
	unreachable := &stubSource{err: errUnreachable}
	f := newTestFallback(&now, blocked, unreachable)

	if _, err := f.Current(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	now = now.Add(30 * time.Second)
	if _, err := f.Current(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if blocked.calls != 1 || unreachable.calls != 2 {
		t.Errorf("calls = %d (blocked), %d (unreachable); want 1 and 2", blocked.calls, unreachable.calls)
	}
}

func TestIsTransportErrorOnRealLookups(t *testing.T) {
	refused := &publicIPLookup{client: http.DefaultClient, ipProvider: ipProvider{url: "http://127.0.0.1:1", extract: strings.TrimSpace}}
	if _, err := refused.Current(context.Background()); !isTransportError(err) {
		t.Errorf("refused connection: isTransportError(%v) = false, want true", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	forbidden := &publicIPLookup{client: srv.Client(), ipProvider: ipProvider{url: srv.URL, extract: strings.TrimSpace}}
	if _, err := forbidden.Current(context.Background()); err == nil || isTransportError(err) {
		t.Errorf("403 response: isTransportError(%v) = true, want false", err)
	}
	truncated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, "9.8.")
	}))
	defer truncated.Close()
	cut := &publicIPLookup{client: truncated.Client(), ipProvider: ipProvider{url: truncated.URL, extract: strings.TrimSpace}}
	if _, err := cut.Current(context.Background()); !isTransportError(err) {
		t.Errorf("body cut short: isTransportError(%v) = false, want true", err)
	}
}

func TestJSONIP(t *testing.T) {
	tests := []struct {
		path []string
		body string
		want string
	}{
		{[]string{"ip"}, `{"ip":"9.8.7.6"}`, "9.8.7.6"},
		{[]string{"data", "ip"}, `{"data":{"ip":"9.8.7.6"}}`, "9.8.7.6"},
		{[]string{"data", "ip"}, `{"data":{"addr":"9.8.7.6"}}`, ""},
		{[]string{"data", "ip"}, `{"data":"9.8.7.6"}`, ""},
		{[]string{"ip"}, `{"ip":42}`, ""},
		{[]string{"ip"}, `not json`, ""},
	}
	for _, tt := range tests {
		if got := jsonIP(tt.path...)(tt.body); got != tt.want {
			t.Errorf("jsonIP(%q)(%s) = %q, want %q", tt.path, tt.body, got, tt.want)
		}
	}
}

// dnsTestClient fails DNS for host "dns-fails.test" and sends every other
// connection to srv.
func dnsTestClient(srv *httptest.Server) *http.Client {
	dialer := &net.Dialer{}
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, _, _ := net.SplitHostPort(addr); host == "dns-fails.test" {
				return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
			}
			return dialer.DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}}
}

func TestPublicIPLookupDNSFallback(t *testing.T) {
	tests := []struct {
		name    string
		primary string
		status  int
		wantIP  string
		wantErr bool
	}{
		{name: "dns failure uses fallback", primary: "http://dns-fails.test/", status: http.StatusOK, wantIP: "9.8.7.6"},
		{name: "http error does not", primary: "http://reachable.test/", status: http.StatusForbidden, wantErr: true},
		{name: "fallback failure is reported", primary: "http://dns-fails.test/", status: http.StatusForbidden, wantErr: true},
		{name: "fallback server error is reported", primary: "http://dns-fails.test/", status: http.StatusBadGateway, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetches := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fetches++
				w.WriteHeader(tt.status)
				io.WriteString(w, "9.8.7.6")
			}))
			defer srv.Close()

			l := &publicIPLookup{client: dnsTestClient(srv), ipProvider: ipProvider{url: tt.primary, dnsFallbackURL: "http://fallback.test/", extract: strings.TrimSpace}}
			got, err := l.Current(context.Background())
			if got != tt.wantIP || (err != nil) != tt.wantErr || fetches != 1 {
				t.Errorf("Current() = %q, %v with %d fetches; want %q, error %v, 1 fetch", got, err, fetches, tt.wantIP, tt.wantErr)
			}
			// A host that answered with an error is not unreachable, even after a DNS failure.
			if err != nil && isTransportError(err) {
				t.Errorf("isTransportError(%v) = true; the answering host should be charged", err)
			}
		})
	}
}

func TestFallbackLookupStopsAfterThreeUnreachable(t *testing.T) {
	now := testEpoch
	var srcs []*stubSource
	var sources []ipSource
	for range 5 {
		s := &stubSource{err: errUnreachable}
		srcs = append(srcs, s)
		sources = append(sources, s)
	}
	f := newTestFallback(&now, sources...)

	if _, err := f.Current(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if got := []int{srcs[0].calls, srcs[1].calls, srcs[2].calls, srcs[3].calls, srcs[4].calls}; !slices.Equal(got, []int{1, 1, 1, 0, 0}) {
		t.Fatalf("first sync calls = %v, want only the first three asked", got)
	}

	// The next sync moves on to the providers not yet asked.
	now = now.Add(testInterval)
	if _, err := f.Current(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if got := []int{srcs[0].calls, srcs[1].calls, srcs[2].calls, srcs[3].calls, srcs[4].calls}; !slices.Equal(got, []int{1, 1, 1, 1, 1}) {
		t.Errorf("second sync calls = %v, want the last two asked", got)
	}
}

func TestFallbackLookupChargesUnreachableProvidersOnceAnotherAnswers(t *testing.T) {
	now := testEpoch
	a, b, c := &stubSource{err: errUnreachable}, &stubSource{err: errUnreachable}, &stubSource{err: errUnreachable}
	d := &stubSource{ip: "4.4.4.4"}
	f := newTestFallback(&now, a, b, c, d)

	if _, err := f.Current(context.Background()); err == nil || d.calls != 0 {
		t.Fatalf("err = %v, d calls = %d; want a stop before d", err, d.calls)
	}
	// d answers, so the network works: a, b and c are charged a real failure
	// and stay skipped at the sync after.
	for range 2 {
		now = now.Add(testInterval)
		if got, err := f.Current(context.Background()); err != nil || got != "4.4.4.4" {
			t.Fatalf("Current() = %q, %v; want d's answer", got, err)
		}
	}
	if a.calls != 1 || b.calls != 1 || c.calls != 1 {
		t.Errorf("calls = %d, %d, %d; want each asked once", a.calls, b.calls, c.calls)
	}
}

func TestFallbackLookupDoesNotStopEarlyWithoutUntriedProviders(t *testing.T) {
	// With only three providers, a stop would skip all of them next sync.
	now := testEpoch
	a, b, c := &stubSource{errs: []error{errUnreachable}, ip: "1.1.1.1"}, &stubSource{err: errUnreachable}, &stubSource{err: errUnreachable}
	f := newTestFallback(&now, a, b, c)
	if _, err := f.Current(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	now = now.Add(testInterval)
	if got, err := f.Current(context.Background()); err != nil || got != "1.1.1.1" {
		t.Errorf("Current() = %q, %v; want all retried at the next sync", got, err)
	}
}

func TestFallbackLookupClearsUnreachableMarkWhenRetried(t *testing.T) {
	// a is skipped after an early stop, then retried during a total outage.
	// Once b answers later, a must not be charged for the stale skip.
	now := testEpoch
	a := &stubSource{err: errUnreachable}
	x := &stubSource{errs: []error{errUnreachable, errFake}}
	y := &stubSource{err: errUnreachable}
	b := &stubSource{errs: []error{errUnreachable}, ip: "2.2.2.2"}
	f := newTestFallback(&now, a, x, y, b)

	if _, err := f.Current(context.Background()); err == nil { // a, x, y stop early
		t.Fatal("expected error")
	}
	// x's answered error breaks the run, so every source fails without a stop.
	now = now.Add(2 * testInterval)
	if _, err := f.Current(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if f.sources[0].unreachable != nil {
		t.Errorf("a's unreachable mark = %v after it was retried, want cleared", f.sources[0].unreachable)
	}
}
