package config

import (
	"testing"
	"time"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, warnings := Parse(nil)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if cfg != Default() {
		t.Fatalf("cfg = %+v, want defaults", cfg)
	}
	if cfg.BaseURL != "https://api.wokey.ai" || cfg.ModelPrefix != "wokey" {
		t.Fatalf("unexpected defaults %+v", cfg)
	}
}

func TestParseConfigOverrides(t *testing.T) {
	raw := []byte(`
enabled: true
priority: 3
base-url: https://proxy.example.com/wokey/v1/
model-prefix: /wk/
models-refresh-minutes: 0
request-timeout-seconds: 30
stream-idle-timeout-seconds: 45
`)
	cfg, warnings := Parse(raw)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if cfg.BaseURL != "https://proxy.example.com/wokey" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.ModelPrefix != "wk" {
		t.Errorf("ModelPrefix = %q", cfg.ModelPrefix)
	}
	if cfg.ModelsRefreshInterval != 0 {
		t.Errorf("ModelsRefreshInterval = %v", cfg.ModelsRefreshInterval)
	}
	if cfg.RequestTimeout != 30*time.Second || cfg.StreamIdleTimeout != 45*time.Second {
		t.Errorf("timeouts = %v / %v", cfg.RequestTimeout, cfg.StreamIdleTimeout)
	}
}

func TestParseConfigEmptyPrefixRegistersBareIDs(t *testing.T) {
	cfg, _ := Parse([]byte(`model-prefix: ""`))
	if cfg.ModelPrefix != "" {
		t.Fatalf("ModelPrefix = %q, want empty", cfg.ModelPrefix)
	}
}

func TestParseConfigInvalidValuesFallBack(t *testing.T) {
	cfg, warnings := Parse([]byte(`
base-url: ftp://example.com
models-refresh-minutes: -1
request-timeout-seconds: 0
stream-idle-timeout-seconds: -5
`))
	if len(warnings) != 4 {
		t.Fatalf("warnings = %v, want 4", warnings)
	}
	if cfg != Default() {
		t.Fatalf("cfg = %+v, want defaults", cfg)
	}

	cfg, warnings = Parse([]byte("base-url: [unterminated"))
	if len(warnings) != 1 || cfg != Default() {
		t.Fatalf("broken YAML: cfg=%+v warnings=%v", cfg, warnings)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://api.wokey.ai":          "https://api.wokey.ai",
		"https://api.wokey.ai/":         "https://api.wokey.ai",
		"https://api.wokey.ai/v1":       "https://api.wokey.ai",
		"https://api.wokey.ai/v1/?x=1":  "https://api.wokey.ai",
		"http://127.0.0.1:8080/gw/v1/":  "http://127.0.0.1:8080/gw",
		" https://api.wokey.ai/v1#frag": "https://api.wokey.ai",
	}
	for in, want := range cases {
		got, errBase := NormalizeBaseURL(in)
		if errBase != nil || got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want %q", in, got, errBase, want)
		}
	}
	for _, bad := range []string{"api.wokey.ai", "ftp://api.wokey.ai", "https://"} {
		if _, errBase := NormalizeBaseURL(bad); errBase == nil {
			t.Errorf("NormalizeBaseURL(%q) accepted", bad)
		}
	}
}
