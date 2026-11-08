package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// redirectClient sends every request to srv, whatever its URL, and leaves
// the original host in the request's Host for the handler to tell services apart.
func redirectClient(srv *httptest.Server) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		redirected := r.Clone(r.Context())
		redirected.Host = r.URL.Host
		redirected.URL.Scheme = "http"
		redirected.URL.Host = srv.Listener.Addr().String()
		return srv.Client().Transport.RoundTrip(redirected)
	})}
}

func TestIPPrintsTheAddressAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "checkip.amazonaws.com" {
			t.Errorf("request for host %q, want only the chosen provider", r.Host)
		}
		_, _ = w.Write([]byte("203.0.113.7\n"))
	}))
	defer srv.Close()

	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"flag", []string{"ip", "--providers", "amazon"}, nil},
		{"environment", []string{"ip"}, map[string]string{"IP_PROVIDERS": "amazon"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, stdout, stderr := newTestApp(tt.env)
			a.httpClient = redirectClient(srv)
			if code := a.execute(context.Background(), tt.args); code != exitOK {
				t.Fatalf("exit code = %d, stderr %q", code, stderr.String())
			}
			if stdout.String() != "203.0.113.7\n" || stderr.Len() != 0 {
				t.Errorf("stdout = %q, stderr = %q; want the address alone", stdout.String(), stderr.String())
			}
		})
	}
}

func TestIPFailures(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failing.Close()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"provider fails", []string{"ip", "--providers", "amazon"}, "msg=exiting"},
		{"unknown provider", []string{"ip", "--providers", "nope"}, `IP provider \"nope\" must be one of`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, stdout, stderr := newTestApp(nil)
			a.httpClient = redirectClient(failing)
			if code := a.execute(context.Background(), tt.args); code != exitFailure {
				t.Errorf("exit code = %d, want %d", code, exitFailure)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("stdout = %q, stderr = %q; want nothing and %q", stdout.String(), stderr.String(), tt.want)
			}
		})
	}
}

func TestInterruptedCommandExitsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, out, errOut := newTestApp(map[string]string{})
	if code := a.execute(ctx, []string{"ip", "--providers", "amazon"}); code != exitOK || out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and no output after an interrupt", code, out.String(), errOut.String())
	}
}
