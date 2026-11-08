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

	var stderr bytes.Buffer
	if code := exitCode([]string{healthcheckCommand}, healthy, &stderr); code != 0 || stderr.Len() != 0 {
		t.Errorf("healthy probe: exit %d, stderr %q; want 0 and no output", code, stderr.String())
	}
	if code := exitCode([]string{"bogus"}, healthy, &stderr); code != 1 || !strings.Contains(stderr.String(), `msg=exiting error="unknown command \"bogus\""`) {
		t.Errorf("unknown command: exit %d, stderr %q; want 1 and the error logged", code, stderr.String())
	}
}
