package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublicIPLookupCurrent(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		status  int
		want    string
		wantErr bool
	}{
		{name: "ok", body: "9.8.7.6\n", status: http.StatusOK, want: "9.8.7.6"},
		{name: "ipv4 mapped", body: "::ffff:1.2.3.4", status: http.StatusOK, want: "1.2.3.4"},
		{name: "ipv6", body: "2001:db8::1", status: http.StatusOK, wantErr: true},
		{name: "garbage", body: "hello", status: http.StatusOK, wantErr: true},
		{name: "status", status: http.StatusInternalServerError, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			got, err := (&publicIPLookup{client: srv.Client(), url: srv.URL}).Current(context.Background())
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("Current() = %q, %v; want %q, wantErr %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestPublicIPLookupRequestErrors(t *testing.T) {
	for _, url := range []string{"http://127.0.0.1:1", "http://bad host"} {
		if _, err := (&publicIPLookup{client: http.DefaultClient, url: url}).Current(context.Background()); err == nil {
			t.Errorf("url %q: expected error", url)
		}
	}
}

func TestPublicIPLookupTruncatesBodyInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, strings.Repeat("x", 4096))
	}))
	defer srv.Close()

	_, err := (&publicIPLookup{client: srv.Client(), url: srv.URL}).Current(context.Background())
	if err == nil || len(err.Error()) > 200 {
		t.Errorf("err = %v, want short error", err)
	}
}

func TestNewIPv4ClientDialsIPv4(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "7.7.7.7")
	}))
	defer srv.Close()

	got, err := (&publicIPLookup{client: newIPv4Client(time.Second), url: srv.URL}).Current(context.Background())
	if err != nil || got != "7.7.7.7" {
		t.Errorf("Current() = %q, %v", got, err)
	}
}
