package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	publicIP    = "203.0.113.7"
	oldRecordIP = "198.51.100.1"
)

// fakeCloud stands in for Cloudflare and the one IP provider the tests choose.
type fakeCloud struct {
	mu               sync.Mutex
	recordIP         string
	cloudflareStatus int
	providerStatus   int
	puts             []string
	authorizations   []string
	// healthAddr is dialled on each provider request; healthServerSeen records
	// that something answered.
	healthAddr       string
	healthServerSeen bool
}

func newFakeCloud(recordIP string) *fakeCloud {
	return &fakeCloud{recordIP: recordIP, cloudflareStatus: http.StatusOK, providerStatus: http.StatusOK}
}

func (f *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Host {
	case "checkip.amazonaws.com":
		f.probeHealthAddr()
		if f.providerStatus != http.StatusOK {
			http.Error(w, "provider down", f.providerStatus)
			return
		}
		_, _ = io.WriteString(w, publicIP+"\n")
	case "api.cloudflare.com":
		f.serveCloudflare(w, r)
	default:
		http.Error(w, "unexpected host "+r.Host, http.StatusTeapot)
	}
}

func (f *fakeCloud) probeHealthAddr() {
	if f.healthAddr == "" {
		return
	}
	if conn, err := net.DialTimeout("tcp", f.healthAddr, time.Second); err == nil {
		_ = conn.Close()
		f.healthServerSeen = true
	}
}

func (f *fakeCloud) serveCloudflare(w http.ResponseWriter, r *http.Request) {
	f.authorizations = append(f.authorizations, r.Header.Get("Authorization"))
	if f.cloudflareStatus != http.StatusOK {
		http.Error(w, "denied", f.cloudflareStatus)
		return
	}
	if r.URL.Path != "/client/v4/zones/zone/dns_records/record" {
		http.Error(w, "wrong path "+r.URL.Path, http.StatusNotFound)
		return
	}
	if r.Method == http.MethodPut {
		var body struct{ Content string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.puts = append(f.puts, body.Content)
		f.recordIP = body.Content
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]string{
		"type": "A", "name": "home.example.com", "content": f.recordIP,
	}})
}

func syncEnv() map[string]string {
	env := baseEnv()
	env["IP_PROVIDERS"] = "amazon"
	return env
}

func runSync(t *testing.T, cloud *fakeCloud, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	srv := httptest.NewServer(cloud)
	defer srv.Close()
	a, out, errOut := newTestApp(env)
	a.httpClient = redirectClient(srv)
	code = a.execute(context.Background(), append([]string{"sync"}, args...))
	return code, out.String(), errOut.String()
}

func TestSyncReportsWhatItDid(t *testing.T) {
	tests := []struct {
		name     string
		recordIP string
		want     string
		wantPuts []string
	}{
		{"already current", publicIP, "home.example.com is already " + publicIP + "\n", nil},
		{"updated", oldRecordIP, "updated home.example.com: " + oldRecordIP + " -> " + publicIP + "\n", []string{publicIP}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloud := newFakeCloud(tt.recordIP)
			code, stdout, stderr := runSync(t, cloud, syncEnv())
			if code != exitOK || stdout != tt.want {
				t.Errorf("exit %d, stdout %q, stderr %q; want 0 and %q", code, stdout, stderr, tt.want)
			}
			if strings.Join(cloud.puts, ",") != strings.Join(tt.wantPuts, ",") {
				t.Errorf("updates = %q, want %q", cloud.puts, tt.wantPuts)
			}
			for _, auth := range cloud.authorizations {
				if auth != "Bearer key" {
					t.Errorf("Authorization = %q, want the configured token", auth)
				}
			}
		})
	}
}

func TestSyncFailuresExitWithOne(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeCloud, map[string]string)
		want  string
	}{
		{"cloudflare rejects the token", func(f *fakeCloud, _ map[string]string) { f.cloudflareStatus = http.StatusForbidden }, "permanent sync failure"},
		{"provider is down", func(f *fakeCloud, _ map[string]string) { f.providerStatus = http.StatusInternalServerError }, "msg=exiting"},
		{"zone is missing", func(_ *fakeCloud, env map[string]string) { delete(env, "CLOUDFLARE_ZONE_ID") }, "zone ID is not set"},
		{"unknown provider", func(_ *fakeCloud, env map[string]string) { env["IP_PROVIDERS"] = "nope" }, "IP provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloud, env := newFakeCloud(oldRecordIP), syncEnv()
			tt.setup(cloud, env)
			code, stdout, stderr := runSync(t, cloud, env)
			if code != exitFailure || stdout != "" || !strings.Contains(stderr, tt.want) {
				t.Errorf("exit %d, stdout %q, stderr %q; want 1, nothing and %q", code, stdout, stderr, tt.want)
			}
			if len(cloud.puts) != 0 {
				t.Errorf("updates = %q, want none", cloud.puts)
			}
		})
	}
}

func TestSyncTakesItsSettingsFromFlags(t *testing.T) {
	cloud := newFakeCloud(oldRecordIP)
	code, stdout, stderr := runSync(t, cloud, nil,
		"--token", "key", "--zone-id", "zone", "--record-id", "record", "--record-name", "home.example.com",
		"--ttl", "120", "--providers", "amazon")
	if want := "updated home.example.com: " + oldRecordIP + " -> " + publicIP + "\n"; code != exitOK || stdout != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and %q", code, stdout, stderr, want)
	}
}

func TestSyncDoesNotStartTheHealthServer(t *testing.T) {
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	if err := free.Close(); err != nil {
		t.Fatal(err)
	}
	env := syncEnv()
	env["HEALTH_ADDR"] = addr
	cloud := newFakeCloud(publicIP)
	cloud.healthAddr = addr

	code, stdout, stderr := runSync(t, cloud, env)
	if code != exitOK {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if cloud.healthServerSeen {
		t.Errorf("something listened on the health address %s during the sync", addr)
	}
}

func TestSyncIgnoresTheDaemonInterval(t *testing.T) {
	env := syncEnv()
	env["SYNC_INTERVAL"] = "bogus"
	if code, _, stderr := runSync(t, newFakeCloud(publicIP), env); code != exitOK {
		t.Errorf("exit %d, stderr %q; want 0, as sync does not use SYNC_INTERVAL", code, stderr)
	}
}
