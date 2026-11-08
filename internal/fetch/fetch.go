// Package fetch performs HTTP requests and classifies their failures.
package fetch

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

// StatusError is returned for any non-200 response.
type StatusError struct {
	Code   int
	Status string
	Body   string
	// RetryAfter is the server's Retry-After delay, or 0 when absent.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return "unexpected status " + e.Status + ": " + e.Body
}

// TransportError marks a request that got no usable response: the request
// failed, or the body could not be read in full.
type TransportError struct {
	Err error
}

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// IsTransportError reports whether err means no usable response arrived.
func IsTransportError(err error) bool {
	var te *TransportError
	return errors.As(err, &te)
}

// RetryAfter returns the Retry-After delay carried by err, or 0.
func RetryAfter(err error) time.Duration {
	var se *StatusError
	if errors.As(err, &se) {
		return se.RetryAfter
	}
	return 0
}

// Body performs req and returns the body of a 200 response.
func Body(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, &TransportError{Err: err}
	}
	defer resp.Body.Close() //nolint:errcheck // nothing useful to do on failure

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	// An error status is kept even if its body was cut short, so its
	// Retry-After still applies.
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{
			Code:       resp.StatusCode,
			Status:     resp.Status,
			Body:       snippet(body),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}
	if err != nil {
		return nil, &TransportError{Err: err}
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
