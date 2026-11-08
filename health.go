package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

const (
	healthPath        = "/healthz"
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// health tracks when the daemon last completed a sync successfully.
type health struct {
	mu          sync.Mutex
	lastSuccess time.Time
	now         func() time.Time
}

func newHealth() *health { return &health{now: time.Now} }

func (h *health) markSuccess() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastSuccess = h.now()
}

// age reports how long ago the last success was, and false if there was none.
func (h *health) age() (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lastSuccess.IsZero() {
		return 0, false
	}
	return h.now().Sub(h.lastSuccess), true
}

// handler reports 200 while the last success is newer than staleAfter.
func (h *health) handler(staleAfter time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		age, ok := h.age()
		switch {
		case !ok:
			http.Error(w, "no successful sync yet", http.StatusServiceUnavailable)
		case age > staleAfter:
			http.Error(w, fmt.Sprintf("last successful sync %s ago", age.Round(time.Second)), http.StatusServiceUnavailable)
		default:
			fmt.Fprintln(w, "ok")
		}
	})
}

func newHealthServer(addr string, h http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET "+healthPath, h)
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
}

// serveHealth serves srv until ctx is cancelled, then shuts it down.
func serveHealth(ctx context.Context, srv *http.Server) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return srv.Close()
		}
		return nil
	}
}

// healthURL turns a listen address into the URL the local probe should call.
func healthURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid health address %q: %w", addr, err)
	}
	host = probeHost(host)
	return "http://" + net.JoinHostPort(host, port) + healthPath, nil
}

// probeHost maps an empty or wildcard listen host to the loopback address of
// the same family.
func probeHost(host string) string {
	if host == "" {
		return "127.0.0.1"
	}
	if addr, err := netip.ParseAddr(host); err == nil && addr.IsUnspecified() {
		if addr.Is4() {
			return "127.0.0.1"
		}
		return "::1"
	}
	return host
}

// newProbeClient returns a client that ignores proxy environment variables, so
// the probe always reaches the local endpoint.
func newProbeClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: timeout}
}

// probeHealth returns nil when the health endpoint at url answers 200.
func probeHealth(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: %s", resp.Status)
	}
	return nil
}
