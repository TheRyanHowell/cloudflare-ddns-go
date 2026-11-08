package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	publicIPURL = "https://icanhazip.com/"
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
	url    string
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
	text := strings.TrimSpace(string(body))
	addr, err := netip.ParseAddr(text)
	if err != nil || !addr.Unmap().Is4() {
		return "", fmt.Errorf("public IP %.*q is not an IPv4 address", maxQuotedBodyChars, text)
	}
	return addr.Unmap().String(), nil
}
