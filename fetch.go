package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// maxResponseBytes caps how much of a response body is accepted.
	maxResponseBytes = 1 << 20
	// maxErrorSnippetBytes caps how much of an error body is quoted in errors.
	maxErrorSnippetBytes = 256
)

// statusError is returned for any non-200 response.
type statusError struct {
	code   int
	status string
	body   string
}

func (e *statusError) Error() string {
	return "unexpected status " + e.status + ": " + e.body
}

// permanent reports whether retrying err cannot help: rejected credentials, a
// missing record, or a record that is not usable.
func permanent(err error) bool {
	if errors.Is(err, errInvalidRecord) {
		return true
	}
	var se *statusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

// fetch performs req and returns the body of a 200 response.
func fetch(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // nothing useful to do on failure

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{code: resp.StatusCode, status: resp.Status, body: snippet(body)}
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return body, nil
}

func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > maxErrorSnippetBytes {
		return strings.ToValidUTF8(text[:maxErrorSnippetBytes], "") + "..."
	}
	return text
}
