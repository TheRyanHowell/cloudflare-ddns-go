// Package health serves the /healthz endpoint and probes it.
package health

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

// State tracks when the daemon last completed a sync successfully.
type State struct {
	mu          sync.Mutex
	lastSuccess time.Time
	permanent   error
	now         func() time.Time
}

// New returns a State with no successful sync yet.
func New() *State { return &State{now: time.Now} }

// MarkSuccess records a successful sync now.
func (h *State) MarkSuccess() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastSuccess = h.now()
}

// age reports how long ago the last success was, and false if there was none.
func (h *State) age() (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lastSuccess.IsZero() {
		return 0, false
	}
	return h.now().Sub(h.lastSuccess), true
}

// Handler reports 200 while the last success is newer than staleAfter.
func (h *State) Handler(staleAfter time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		age, ok := h.age()
		failure := h.failure()
		switch {
		case failure != nil:
			http.Error(w, "permanent failure; see the logs", http.StatusServiceUnavailable)
		case !ok:
			http.Error(w, "no successful sync yet", http.StatusServiceUnavailable)
		case age > staleAfter:
			http.Error(w, fmt.Sprintf("last successful sync %s ago", age.Round(time.Second)), http.StatusServiceUnavailable)
		default:
			fmt.Fprintln(w, "ok") //nolint:errcheck // nothing useful to do on failure
		}
	})
}

// NewServer returns a server for h at GET /healthz on addr.
func NewServer(addr string, h http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET "+healthPath, h)
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
}

// Serve serves srv until ctx is cancelled, then shuts it down.
func Serve(ctx context.Context, srv *http.Server) error {
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

// URL turns a listen address into the URL the local probe should call.
func URL(addr string) (string, error) {
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

// NewProbeClient returns a client that ignores proxy environment variables, so
// the probe always reaches the local endpoint.
func NewProbeClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: timeout}
}

// Probe returns nil when the health endpoint at url answers 200.
func Probe(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // nothing useful to do on failure
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: %s", resp.Status)
	}
	return nil
}

// MarkPermanentFailure makes the health check fail from now on.
func (h *State) MarkPermanentFailure(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.permanent = err
}

func (h *State) failure() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.permanent
}
