package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFetch(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantErr    string
		wantErrNot string
	}{
		{name: "ok", status: http.StatusOK, body: "hi"},
		{name: "status includes body", status: http.StatusForbidden, body: `{"errors":["bad token"]}`, wantErr: "bad token"},
		{name: "status body truncated", status: http.StatusBadRequest, body: strings.Repeat("x", 4096), wantErr: "...", wantErrNot: strings.Repeat("x", maxErrorSnippetBytes+1)},
		{name: "too large", status: http.StatusOK, body: strings.Repeat("x", maxResponseBytes+1), wantErr: "exceeds"},
		{name: "oversize error reports status", status: http.StatusForbidden, body: strings.Repeat("x", maxResponseBytes+1), wantErr: "403 Forbidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)

			got, err := fetch(srv.Client(), req)
			if tt.wantErr == "" {
				if err != nil || string(got) != tt.body {
					t.Errorf("fetch() = %q, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if tt.wantErrNot != "" && strings.Contains(err.Error(), tt.wantErrNot) {
				t.Errorf("error not truncated: %d bytes", len(err.Error()))
			}
		})
	}
}

func TestSnippetIsValidUTF8(t *testing.T) {
	got := snippet([]byte(strings.Repeat("é", maxErrorSnippetBytes)))
	if !utf8.ValidString(got) {
		t.Errorf("snippet produced invalid UTF-8: %q", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tests := map[string]time.Duration{
		"":                              0,
		"120":                           2 * time.Minute,
		" 30 ":                          30 * time.Second,
		"-5":                            0,
		"soon":                          0,
		"Sun, 08 Nov 2026 12:10:00 GMT": 10 * time.Minute,
		"Sun, 08 Nov 2026 11:00:00 GMT": 0,
		"999999999":                     maxRetryAfter,
		"99999999999999999999":          maxRetryAfter,
		"-99999999999999999999":         0,
	}
	for in, want := range tests {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFetchStatusErrorCarriesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)

	_, err := fetch(srv.Client(), req)
	var se *statusError
	if !errors.As(err, &se) || se.retryAfter != 2*time.Minute {
		t.Errorf("err = %#v, want statusError with retryAfter 2m", err)
	}
}
