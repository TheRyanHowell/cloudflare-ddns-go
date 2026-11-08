package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// maxResponseBytes caps how much of a response body is accepted.
	maxResponseBytes = 1 << 20
	// maxRetryAfter caps how long a server can ask us to wait.
	maxRetryAfter = 24 * time.Hour
	// maxErrorSnippetBytes caps how much of an error body is quoted in errors.
	maxErrorSnippetBytes = 256
)

// statusError is returned for any non-200 response.
type statusError struct {
	code   int
	status string
	body   string
	// retryAfter is the server's Retry-After delay, or 0 when absent.
	retryAfter time.Duration
}

func (e *statusError) Error() string {
	return "unexpected status " + e.status + ": " + e.body
}

// transportError marks a request that got no usable response: the request
// failed, or the body could not be read in full.
type transportError struct {
	err error
}

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// isTransportError reports whether err means no usable response arrived.
func isTransportError(err error) bool {
	var te *transportError
	return errors.As(err, &te)
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
		return nil, &transportError{err: err}
	}
	defer resp.Body.Close() //nolint:errcheck // nothing useful to do on failure

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &transportError{err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{
			code:       resp.StatusCode,
			status:     resp.Status,
			body:       snippet(body),
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
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

// parseRetryAfter reads a Retry-After header given in seconds or as an HTTP
// date, returning 0 when it is absent, invalid or in the past.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	var d time.Duration
	secs, err := strconv.Atoi(value)
	switch {
	case err == nil:
		d = time.Duration(min(secs, int(maxRetryAfter/time.Second))) * time.Second
	case errors.Is(err, strconv.ErrRange):
		if !strings.HasPrefix(value, "-") {
			d = maxRetryAfter
		}
	default:
		if at, err := http.ParseTime(value); err == nil {
			d = at.Sub(now)
		}
	}
	return min(max(d, 0), maxRetryAfter)
}
