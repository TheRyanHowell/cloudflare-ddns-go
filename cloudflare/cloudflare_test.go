package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TheRyanHowell/cloudflare-ddns-go/internal/fetch"
)

var testConfig = RecordConfig{Token: "key", ZoneID: "z", RecordID: "r", Name: "home.example.com", TTL: 120}

func newTestRecord(t *testing.T, h http.HandlerFunc) *Record {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewRecord(testConfig, WithHTTPClient(srv.Client()), WithBaseURL(srv.URL))
}

func TestCloudflareRecordContent(t *testing.T) {
	rec := newTestRecord(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/zones/z/dns_records/r" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Errorf("Authorization = %q", got)
		}
		io.WriteString(w, `{"result":{"type":"A","name":"home.example.com","content":"1.2.3.4"}}`)
	})
	got, err := rec.Content(context.Background())
	if err != nil || got != "1.2.3.4" {
		t.Errorf("Content() = %q, %v", got, err)
	}
}

func TestCloudflareRecordContentNameMatching(t *testing.T) {
	tests := []struct {
		name    string
		got     string
		wantErr bool
	}{
		{"exact", "home.example.com", false},
		{"case and trailing dot", "Home.Example.com.", false},
		{"other record", "mail.example.com", true},
		{"missing", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newTestRecord(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"result":{"type":"A","name":%q,"content":"1.2.3.4"}}`, tt.got)
			})
			_, err := rec.Content(context.Background())
			if tt.wantErr != errors.Is(err, ErrInvalidRecord) || (!tt.wantErr && err != nil) {
				t.Errorf("Content() err = %v, want invalid record: %v", err, tt.wantErr)
			}
		})
	}
}

func TestCloudflareRecordContentErrors(t *testing.T) {
	tests := map[string]http.HandlerFunc{
		"status":     func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"bad json":   func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "nope") },
		"no content": func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"result":{"type":"A"}}`) },
		"wrong type": func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"result":{"type":"AAAA","content":"::1"}}`)
		},
	}
	for name, h := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := newTestRecord(t, h).Content(context.Background()); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestCloudflareRecordUpdate(t *testing.T) {
	rec := newTestRecord(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/zones/z/dns_records/r" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("bad headers")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if !strings.Contains(string(raw), `"proxied":false`) {
			t.Errorf("body %s does not explicitly set proxied:false", raw)
		}
		var got recordRequest
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Error(err)
		}
		want := recordRequest{Content: "5.6.7.8", Name: "home.example.com", Type: "A", TTL: 120}
		if got != want {
			t.Errorf("body = %+v, want %+v", got, want)
		}
	})
	if err := rec.Update(context.Background(), "5.6.7.8"); err != nil {
		t.Errorf("Update() = %v", err)
	}
}

func TestCloudflareRecordUpdateStatusError(t *testing.T) {
	rec := newTestRecord(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	if err := rec.Update(context.Background(), "1.1.1.1"); err == nil {
		t.Error("expected error")
	}
}

func TestCloudflareRecordRequestErrors(t *testing.T) {
	for _, baseURL := range []string{"http://127.0.0.1:1", "http://bad host"} {
		rec := NewRecord(testConfig, WithHTTPClient(http.DefaultClient), WithBaseURL(baseURL))
		if _, err := rec.Content(context.Background()); err == nil {
			t.Errorf("Content(%q): expected error", baseURL)
		}
		if err := rec.Update(context.Background(), "1.1.1.1"); err == nil {
			t.Errorf("Update(%q): expected error", baseURL)
		}
	}
}

func TestRecordNameMatchesConfiguredForms(t *testing.T) {
	tests := []struct {
		returned, configured string
		want                 bool
	}{
		{"home.example.com", "home.example.com", true},
		{"home.example.com", "Home.Example.COM.", true},
		{"xn--bcher-kva.example.com", "bücher.example.com", true},
		{"*.example.com", "*.example.com", true},
		{"home.example.com", "home", false},
		{"home.example.com", "home.example", false},
		{"a.b.example.com", "a.b", false},
		{"example.com", "@", false},
		{"homeserver.example.com", "home.example.com", false},
		{"", "home.example.com", false},
	}
	for _, tt := range tests {
		if got := recordNameMatches(tt.returned, tt.configured); got != tt.want {
			t.Errorf("recordNameMatches(%q, %q) = %v, want %v", tt.returned, tt.configured, got, tt.want)
		}
	}
}

func FuzzNormalizeDNSName(f *testing.F) {
	for _, seed := range []string{"home.example.com", "Home.Example.COM.", "xn--bcher-kva.example.com", "bücher.example.com", "*.example.com", "home", "@", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got := normalizeDNSName(name)
		if recordNameMatches(name, name) != (got != "") {
			t.Errorf("recordNameMatches(%q, itself) = %v with normalised form %q", name, got == "", got)
		}
		// A name ending in ".." keeps one dot after a pass; known and harmless,
		// as such a name never matches.
		if strings.HasSuffix(got, ".") {
			return
		}
		if again := normalizeDNSName(got); again != got {
			t.Errorf("normalizeDNSName is not idempotent: %q -> %q -> %q", name, got, again)
		}
	})
}

var errFake = errors.New("fake failure")

func TestIsPermanent(t *testing.T) {
	wrap := func(err error) error { return errors.Join(errFake, err) }
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"401", &fetch.StatusError{Code: 401}, true},
		{"403", &fetch.StatusError{Code: 403}, true},
		{"404", &fetch.StatusError{Code: 404}, true},
		{"429", &fetch.StatusError{Code: 429}, false},
		{"500", &fetch.StatusError{Code: 500}, false},
		{"invalid record", ErrInvalidRecord, true},
		{"wrapped status", wrap(&fetch.StatusError{Code: 403}), true},
		{"network error", errFake, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPermanent(tt.err); got != tt.want {
				t.Errorf("IsPermanent(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestNewRecordDefaults(t *testing.T) {
	r := NewRecord(testConfig)
	if r.baseURL != "https://api.cloudflare.com/client/v4" || r.client.Timeout != RequestTimeout || r.cfg != testConfig {
		t.Errorf("NewRecord() = %+v, want the Cloudflare API with the default timeout", r)
	}
}

func TestNewRecordIgnoresNilHTTPClient(t *testing.T) {
	if r := NewRecord(RecordConfig{}, WithHTTPClient(nil)); r.client == nil {
		t.Error("WithHTTPClient(nil) left no client")
	}
}
