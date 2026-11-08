package main

import "testing"

func TestHealthAddr(t *testing.T) {
	tests := map[string]string{"": defaultHealthAddr, "  ": defaultHealthAddr, " :9000 ": ":9000"}
	for in, want := range tests {
		got := healthAddr(func(string) string { return in })
		if got != want {
			t.Errorf("healthAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	env := map[string]string{
		"CLOUDFLARE_API_KEY":     "key",
		"CLOUDFLARE_ZONE_ID":     "zone",
		"CLOUDFLARE_RECORD_ID":   "record",
		"CLOUDFLARE_RECORD_NAME": "home.example.com",
	}
	getenv := func(k string) string { return env[k] }

	got, err := loadConfig(getenv)
	want := config{apiKey: "key", zoneID: "zone", recordID: "record", recordName: "home.example.com"}
	if err != nil || got != want {
		t.Fatalf("loadConfig() = %+v, %v; want %+v", got, err, want)
	}

	padded := func(k string) string { return " " + env[k] + "\n" }
	if got, err := loadConfig(padded); err != nil || got != want {
		t.Fatalf("padded loadConfig() = %+v, %v; want %+v", got, err, want)
	}

	for name := range env {
		t.Run(name, func(t *testing.T) {
			missing := func(k string) string {
				if k == name {
					return ""
				}
				return env[k]
			}
			_, err := loadConfig(missing)
			if err == nil || err.Error() != name+" is not set" {
				t.Errorf("err = %v, want %q is not set", err, name)
			}
		})
	}
}
