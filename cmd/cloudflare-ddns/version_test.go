package main

import (
	"context"
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: v}}, true }
	}
	noInfo := func() (*debug.BuildInfo, bool) { return nil, false }

	tests := []struct {
		name    string
		preset  string
		readers func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"linker value wins", "v1.2.3", info("v0.4.0"), "v1.2.3"},
		{"module version", "", info("v0.4.0"), "v0.4.0"},
		{"development build", "", info("(devel)"), "dev"},
		{"empty module version", "", info(""), "dev"},
		{"no build info", "", noInfo, "dev"},
	}
	for _, tt := range tests {
		if got := resolveVersion(tt.preset, tt.readers); got != tt.want {
			t.Errorf("%s: resolveVersion(%q) = %q, want %q", tt.name, tt.preset, got, tt.want)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	old := version
	version = "v9.9.9"
	t.Cleanup(func() { version = old })

	a, stdout, stderr := newTestApp(nil)
	if code := a.execute(context.Background(), []string{"version"}); code != exitOK {
		t.Fatalf("execute(version) = %d, stderr %q", code, stderr.String())
	}
	if stdout.String() != "v9.9.9\n" || stderr.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q; want the version alone", stdout.String(), stderr.String())
	}
}
