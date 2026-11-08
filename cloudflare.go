package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	cloudflareAPIBaseURL = "https://api.cloudflare.com/client/v4"
	recordType           = "A"
)

// errInvalidRecord marks a record that exists but cannot be managed.
var errInvalidRecord = errors.New("invalid DNS record")

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

// cloudflareRecord reads and updates one DNS record through the Cloudflare API.
type cloudflareRecord struct {
	client  *http.Client
	baseURL string
	cfg     config
}

func (r *cloudflareRecord) newRequest(ctx context.Context, method string, body io.Reader) (*http.Request, error) {
	url := r.baseURL + "/zones/" + r.cfg.zoneID + "/dns_records/" + r.cfg.recordID
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.cfg.apiKey)
	return req, nil
}

// Content returns the IP the record currently holds.
func (r *cloudflareRecord) Content(ctx context.Context) (string, error) {
	req, err := r.newRequest(ctx, http.MethodGet, nil)
	if err != nil {
		return "", fmt.Errorf("build record request: %w", err)
	}
	body, err := fetch(r.client, req)
	if err != nil {
		return "", fmt.Errorf("get DNS record: %w", err)
	}
	var resp recordResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decode DNS record: %w", err)
	}
	if resp.Result.Type != recordType {
		return "", fmt.Errorf("%w: type %q, want %q", errInvalidRecord, resp.Result.Type, recordType)
	}
	if resp.Result.Content == "" {
		return "", fmt.Errorf("%w: no content", errInvalidRecord)
	}
	// Update writes recordName, so a record ID for another name would be renamed.
	if !sameDNSName(resp.Result.Name, r.cfg.recordName) {
		return "", fmt.Errorf("%w: name %q, want %q", errInvalidRecord, resp.Result.Name, r.cfg.recordName)
	}
	return resp.Result.Content, nil
}

// Update points the record at ip.
func (r *cloudflareRecord) Update(ctx context.Context, ip string) error {
	payload, err := json.Marshal(recordRequest{
		Content: ip,
		Name:    r.cfg.recordName,
		Type:    recordType,
		TTL:     r.cfg.ttl,
	})
	if err != nil {
		return fmt.Errorf("encode DNS record: %w", err)
	}
	req, err := r.newRequest(ctx, http.MethodPut, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build update request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := fetch(r.client, req); err != nil {
		return fmt.Errorf("update DNS record: %w", err)
	}
	return nil
}

// sameDNSName compares DNS names case-insensitively, ignoring a trailing dot.
func sameDNSName(a, b string) bool {
	return a != "" && strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}
