package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	healthy := envFunc(map[string]string{"HEALTH_ADDR": srv.Listener.Addr().String()})

	var stdout, stderr bytes.Buffer
	if code := exitCode([]string{healthcheckCommand}, healthy, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Errorf("healthy probe: exit %d, stderr %q; want 0 and no output", code, stderr.String())
	}
	if code := exitCode(nil, envFunc(nil), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), `msg=exiting error="CLOUDFLARE_API_TOKEN is not set`) {
		t.Errorf("missing config: exit %d, stderr %q; want 1 and the error logged", code, stderr.String())
	}
}
