// Package cloudflare reads and updates one DNS A record through the Cloudflare API.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TheRyanHowell/cloudflare-ddns-go/internal/fetch"
	"golang.org/x/net/idna"
)

const (
	defaultBaseURL = "https://api.cloudflare.com/client/v4"
	recordType     = "A"
	// requestTimeout bounds each request of the default client.
	requestTimeout = 30 * time.Second
)

// ErrInvalidRecord marks a record that exists but cannot be managed.
var ErrInvalidRecord = errors.New("invalid DNS record")

// recordRequest is the body Cloudflare expects when overwriting a DNS record.
type recordRequest struct {
	Content string `json:"content"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	TTL     int    `json:"ttl"`
	// Proxied is always false: records are DNS-only, never behind Cloudflare's proxy.
	Proxied bool `json:"proxied"`
}

// recordResponse is the part of Cloudflare's record response that is used.
type recordResponse struct {
	Result struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"result"`
}

// RecordConfig identifies the record and the values Update writes.
type RecordConfig struct {
	Token    string
	ZoneID   string
	RecordID string
	// Name is the record's full name, which Content checks and Update writes.
	Name string
	TTL  int
}

// Record reads and updates one DNS record through the Cloudflare API.
type Record struct {
	client  *http.Client
	baseURL string
	cfg     RecordConfig
}

// Option configures NewRecord.
type Option func(*Record)

// WithHTTPClient replaces the default client.
func WithHTTPClient(c *http.Client) Option { return func(r *Record) { r.client = c } }

// WithBaseURL replaces the Cloudflare API base URL, for tests.
func WithBaseURL(url string) Option { return func(r *Record) { r.baseURL = url } }

// NewRecord returns a Record for cfg.
func NewRecord(cfg RecordConfig, opts ...Option) *Record {
	r := &Record{client: &http.Client{Timeout: requestTimeout}, baseURL: defaultBaseURL, cfg: cfg}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Record) newRequest(ctx context.Context, method string, body io.Reader) (*http.Request, error) {
	url := r.baseURL + "/zones/" + r.cfg.ZoneID + "/dns_records/" + r.cfg.RecordID
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.cfg.Token)
	return req, nil
}

// Content returns the IP the record currently holds.
func (r *Record) Content(ctx context.Context) (string, error) {
	req, err := r.newRequest(ctx, http.MethodGet, nil)
	if err != nil {
		return "", fmt.Errorf("build record request: %w", err)
	}
	body, err := fetch.Body(r.client, req)
	if err != nil {
		return "", fmt.Errorf("get DNS record: %w", err)
	}
	var resp recordResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decode DNS record: %w", err)
	}
	if resp.Result.Type != recordType {
		return "", fmt.Errorf("%w: type %q, want %q", ErrInvalidRecord, resp.Result.Type, recordType)
	}
	if resp.Result.Content == "" {
		return "", fmt.Errorf("%w: no content", ErrInvalidRecord)
	}
	// Update writes Name, so a record ID for another name would be renamed.
	if !recordNameMatches(resp.Result.Name, r.cfg.Name) {
		return "", fmt.Errorf("%w: name %q, want %q (use the record's full name)", ErrInvalidRecord, resp.Result.Name, r.cfg.Name)
	}
	return resp.Result.Content, nil
}

// Update points the record at ip.
func (r *Record) Update(ctx context.Context, ip string) error {
	payload, err := json.Marshal(recordRequest{
		Content: ip,
		Name:    r.cfg.Name,
		Type:    recordType,
		TTL:     r.cfg.TTL,
	})
	if err != nil {
		return fmt.Errorf("encode DNS record: %w", err)
	}
	req, err := r.newRequest(ctx, http.MethodPut, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build update request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := fetch.Body(r.client, req); err != nil {
		return fmt.Errorf("update DNS record: %w", err)
	}
	return nil
}

// recordNameMatches reports whether the record Cloudflare returned, always a
// full ASCII name, is the configured one, compared exactly after normalising.
func recordNameMatches(returned, configured string) bool {
	r := normalizeDNSName(returned)
	return r != "" && r == normalizeDNSName(configured)
}

// normalizeDNSName lowercases name, drops a trailing dot and converts
// Unicode labels to punycode.
func normalizeDNSName(name string) string {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if ascii, err := idna.ToASCII(name); err == nil {
		return ascii
	}
	return name
}

// IsPermanent reports whether retrying err cannot help: rejected credentials,
// a missing record, or a record that is not usable.
func IsPermanent(err error) bool {
	if errors.Is(err, ErrInvalidRecord) {
		return true
	}
	var se *fetch.StatusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}
