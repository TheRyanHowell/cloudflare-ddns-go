package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func useServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	oldApi, oldIp := cloudflareApiBaseUrl, publicIpUrl
	cloudflareApiBaseUrl, publicIpUrl = srv.URL, srv.URL
	t.Cleanup(func() {
		srv.Close()
		cloudflareApiBaseUrl, publicIpUrl = oldApi, oldIp
	})
}

func mustPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("expected panic")
		}
	}()
	f()
}

func TestGetDnsRecordValue(t *testing.T) {
	useServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/zones/z/dns_records/r" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("bad auth header %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"result":{"content":"1.2.3.4"}}`)
	})
	if got := getDnsRecordValue("key", "z", "r"); got != "1.2.3.4" {
		t.Errorf("got %q", got)
	}
}

func TestGetDnsRecordValueFailures(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"status":   func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
		"bad json": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "nope") },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			useServer(t, h)
			mustPanic(t, func() { getDnsRecordValue("k", "z", "r") })
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		useServer(t, func(http.ResponseWriter, *http.Request) {})
		cloudflareApiBaseUrl = "http://127.0.0.1:1"
		mustPanic(t, func() { getDnsRecordValue("k", "z", "r") })
	})
}

func TestUpdateDnsRecordValue(t *testing.T) {
	useServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/zones/z/dns_records/r" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("bad headers")
		}
		var got RequestModel
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		want := RequestModel{Content: "5.6.7.8", Name: "home.example.com", Type: "A", TTL: 300}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})
	updateDnsRecordValue("key", "z", "r", "home.example.com", "5.6.7.8")
}

func TestUpdateDnsRecordValueFailures(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		useServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
		mustPanic(t, func() { updateDnsRecordValue("k", "z", "r", "n", "1.1.1.1") })
	})
	t.Run("unreachable", func(t *testing.T) {
		useServer(t, func(http.ResponseWriter, *http.Request) {})
		cloudflareApiBaseUrl = "http://127.0.0.1:1"
		mustPanic(t, func() { updateDnsRecordValue("k", "z", "r", "n", "1.1.1.1") })
	})
}

func TestGetCurrentPublicIp(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		want       string
		panics     bool
	}{
		{"ok", "9.8.7.6\n", 200, "9.8.7.6", false},
		{"ipv4 mapped", "::ffff:1.2.3.4", 200, "1.2.3.4", false},
		{"ipv6", "2001:db8::1", 200, "", true},
		{"garbage", "hello", 200, "", true},
		{"status", "", 500, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			useServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				io.WriteString(w, c.body)
			})
			if c.panics {
				mustPanic(t, func() { getCurrentPublicIp() })
				return
			}
			if got := getCurrentPublicIp(); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		useServer(t, func(http.ResponseWriter, *http.Request) {})
		publicIpUrl = "http://127.0.0.1:1"
		mustPanic(t, func() { getCurrentPublicIp() })
	})
}

func TestLoadConfig(t *testing.T) {
	for _, name := range requiredEnvVars {
		t.Setenv(name, "v-"+name)
	}
	cfg, missing := loadConfig()
	if missing != "" || cfg.apiKey != "v-CLOUDFLARE_API_KEY" || cfg.zoneId != "v-CLOUDFLARE_ZONE_ID" ||
		cfg.recordId != "v-CLOUDFLARE_RECORD_ID" || cfg.recordName != "v-CLOUDFLARE_RECORD_NAME" {
		t.Errorf("unexpected result %+v, missing %q", cfg, missing)
	}
	for _, name := range requiredEnvVars {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "")
			if _, missing := loadConfig(); missing != name {
				t.Errorf("missing = %q, want %q", missing, name)
			}
		})
	}
}

func TestMainWithoutConfigReturns(t *testing.T) {
	for _, name := range requiredEnvVars {
		t.Setenv(name, "")
	}
	main()
}

func TestSyncRecord(t *testing.T) {
	var puts int
	useServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			puts++
			return
		}
		io.WriteString(w, "4.4.4.4")
	})
	cfg := config{apiKey: "k", zoneId: "z", recordId: "r", recordName: "n"}

	if got := syncRecord(cfg, "4.4.4.4"); got != "4.4.4.4" || puts != 0 {
		t.Errorf("unchanged ip: got %q, puts %d", got, puts)
	}
	if got := syncRecord(cfg, "1.1.1.1"); got != "4.4.4.4" || puts != 1 {
		t.Errorf("changed ip: got %q, puts %d", got, puts)
	}
}
