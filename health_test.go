package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthHandler(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	const staleAfter = time.Minute
	tests := []struct {
		name       string
		lastAge    time.Duration // negative: never succeeded
		wantStatus int
		wantBody   string
	}{
		{"never succeeded", -1, http.StatusServiceUnavailable, "no successful sync"},
		{"fresh", 10 * time.Second, http.StatusOK, "ok"},
		{"at limit", staleAfter, http.StatusOK, "ok"},
		{"stale", staleAfter + time.Second, http.StatusServiceUnavailable, "1m1s ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &health{now: func() time.Time { return now }}
			if tt.lastAge >= 0 {
				h.lastSuccess = now.Add(-tt.lastAge)
			}
			rec := httptest.NewRecorder()
			h.handler(staleAfter).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, healthPath, nil))
			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("got %d %q, want %d containing %q", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantBody)
			}
		})
	}
}

func TestHealthMarkSuccess(t *testing.T) {
	h := newHealth()
	if _, ok := h.age(); ok {
		t.Fatal("age reported before any success")
	}
	h.markSuccess()
	if age, ok := h.age(); !ok || age > time.Minute {
		t.Errorf("age() = %v, %v", age, ok)
	}
}

func TestHealthServerRoutes(t *testing.T) {
	srv := httptest.NewServer(newHealthServer("", newHealth().handler(time.Minute)).Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + healthPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET %s = %d, want 503", healthPath, resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+healthPath, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", resp.StatusCode)
	}
}

func TestServeHealthShutsDownOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveHealth(ctx, newHealthServer("127.0.0.1:0", http.NotFoundHandler())) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serveHealth() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveHealth did not return after cancel")
	}
}

func TestServeHealthListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ln.Close(); err != nil {
			t.Error(err)
		}
	})

	srv := newHealthServer(ln.Addr().String(), http.NotFoundHandler())
	if err := serveHealth(context.Background(), srv); err == nil {
		t.Error("expected listen error for address in use")
	}
}

func TestHealthURL(t *testing.T) {
	tests := []struct {
		addr    string
		want    string
		wantErr bool
	}{
		{":8080", "http://127.0.0.1:8080/healthz", false},
		{"0.0.0.0:9000", "http://127.0.0.1:9000/healthz", false},
		{"[::]:9000", "http://[::1]:9000/healthz", false},
		{"10.1.2.3:9000", "http://10.1.2.3:9000/healthz", false},
		{"[::1]:80", "http://[::1]:80/healthz", false},
		{"nonsense", "", true},
	}
	for _, tt := range tests {
		got, err := healthURL(tt.addr)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("healthURL(%q) = %q, %v; want %q, wantErr %v", tt.addr, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestProbeHealth(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		err := probeHealth(context.Background(), srv.Client(), srv.URL)
		srv.Close()
		if (err != nil) != (status != http.StatusOK) {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
	if err := probeHealth(context.Background(), http.DefaultClient, "http://127.0.0.1:1"); err == nil {
		t.Error("expected error for unreachable endpoint")
	}
	if err := probeHealth(context.Background(), http.DefaultClient, "http://bad host"); err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestProbeClientIgnoresProxyEnv(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	if err := probeHealth(context.Background(), newProbeClient(time.Second), srv.URL); err != nil {
		t.Errorf("probe through client = %v", err)
	}
	if newProbeClient(time.Second).Transport.(*http.Transport).Proxy != nil {
		t.Error("probe client must not use a proxy")
	}
}

func TestHealthReportsPermanentFailure(t *testing.T) {
	h := newHealth()
	h.markSuccess()
	h.markPermanentFailure(errFake)

	rec := httptest.NewRecorder()
	h.handler(time.Hour).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, healthPath, nil))
	body := rec.Body.String()
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(body, "permanent failure") || strings.Contains(body, "fake failure") {
		t.Errorf("status %d body %q; want 503 despite a recent success, without the error detail", rec.Code, body)
	}
}
