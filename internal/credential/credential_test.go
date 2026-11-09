package credential

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/config"
	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestCredentialFromMapModelSelection(t *testing.T) {
	cred := FromMap(map[string]any{"type": "wokey", "api_key": " sk-1 "})
	if cred.APIKey != "sk-1" || !cred.AllModels || cred.Models != nil || cred.Prefix != nil {
		t.Fatalf("absent models: %+v", cred)
	}

	cred = FromMap(map[string]any{"api_key": "sk-1", "models": nil})
	if !cred.AllModels {
		t.Fatalf("null models should mean all: %+v", cred)
	}

	cred = FromMap(map[string]any{"api_key": "sk-1", "models": []any{}})
	if cred.AllModels || len(cred.Models) != 0 {
		t.Fatalf("empty array is an explicit (empty) selection: %+v", cred)
	}

	cred = FromMap(map[string]any{"api_key": "sk-1", "models": []any{"b", " a ", "b", "", 7}})
	if cred.AllModels || !reflect.DeepEqual(cred.Models, []string{"a", "b"}) {
		t.Fatalf("selection = %+v", cred)
	}
}

func TestCredentialFromMapPrefixAndAliases(t *testing.T) {
	cred := FromMap(map[string]any{"api-key": "sk-2", "prefix": "", "proxy-url": "direct", "base-url": "https://x.example"})
	if cred.APIKey != "sk-2" || cred.ProxyURL != "direct" || cred.BaseURL != "https://x.example" {
		t.Fatalf("aliases: %+v", cred)
	}
	if cred.Prefix == nil || *cred.Prefix != "" {
		t.Fatalf("empty prefix must be kept as an explicit empty prefix: %+v", cred.Prefix)
	}
	cfg := config.Default()
	if got := cred.EffectivePrefix(cfg); got != "" {
		t.Fatalf("effectivePrefix = %q", got)
	}

	cred = FromMap(map[string]any{"api_key": "sk-2", "prefix": "/team/"})
	if got := cred.EffectivePrefix(cfg); got != "team" {
		t.Fatalf("effectivePrefix = %q", got)
	}
	cred = FromMap(map[string]any{"api_key": "sk-2", "prefix": nil})
	if got := cred.EffectivePrefix(cfg); got != "wokey" {
		t.Fatalf("null prefix should use the default, got %q", got)
	}
	// The retired model_prefix field is ignored.
	cred = FromMap(map[string]any{"api_key": "sk-2", "model_prefix": "old"})
	if got := cred.EffectivePrefix(cfg); got != "wokey" {
		t.Fatalf("model_prefix must be ignored, got %q", got)
	}
}

func TestEffectiveBaseURL(t *testing.T) {
	cfg := config.Default()
	if got := (Credential{}).EffectiveBaseURL(cfg); got != cfg.BaseURL {
		t.Fatalf("default = %q", got)
	}
	if got := (Credential{BaseURL: "https://gw.example/v1/"}).EffectiveBaseURL(cfg); got != "https://gw.example" {
		t.Fatalf("override = %q", got)
	}
	if got := (Credential{BaseURL: "not a url"}).EffectiveBaseURL(cfg); got != cfg.BaseURL {
		t.Fatalf("invalid override should fall back, got %q", got)
	}
}

func TestCredentialFromMapsMetadataWins(t *testing.T) {
	cred, merged, errCred := FromMaps([]byte(`{"type":"wokey","api_key":"sk-file","label":"file"}`), map[string]any{"label": "meta"})
	if errCred != nil {
		t.Fatal(errCred)
	}
	if cred.APIKey != "sk-file" || cred.Label != "meta" || merged["label"] != "meta" {
		t.Fatalf("cred=%+v merged=%v", cred, merged)
	}
	if _, _, errCred = FromMaps([]byte(`{bad`), nil); errCred == nil {
		t.Fatal("broken storage JSON accepted")
	}
}

func TestParseAuthFile(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	resp, errParse := ParseAuthFile(pluginapi.AuthParseRequest{FileName: "x.json", RawJSON: []byte(`{"type":"codex","api_key":"k"}`)}, now, "wokey")
	if errParse != nil || resp.Handled {
		t.Fatalf("other provider: %+v %v", resp, errParse)
	}
	resp, errParse = ParseAuthFile(pluginapi.AuthParseRequest{FileName: "x.json", RawJSON: []byte(`not json`)}, now, "wokey")
	if errParse != nil || resp.Handled {
		t.Fatalf("non-JSON: %+v %v", resp, errParse)
	}
	if _, errParse = ParseAuthFile(pluginapi.AuthParseRequest{FileName: "x.json", RawJSON: []byte(`{"type":"wokey"}`)}, now, "wokey"); errParse == nil {
		t.Fatal("wokey file without api_key accepted")
	}

	raw := []byte(`{"type":"Wokey","api_key":"sk-abcdefghijklmnop","proxy_url":"socks5://127.0.0.1:1080","disabled":true}`)
	resp, errParse = ParseAuthFile(pluginapi.AuthParseRequest{FileName: "wokey-1.json", RawJSON: raw}, now, "wokey")
	if errParse != nil || !resp.Handled {
		t.Fatalf("wokey file: %+v %v", resp, errParse)
	}
	auth := resp.Auth
	if auth.Provider != wokey.ProviderID || auth.FileName != "wokey-1.json" || auth.ProxyURL != "socks5://127.0.0.1:1080" || !auth.Disabled {
		t.Fatalf("auth = %+v", auth)
	}
	if auth.Prefix != "wokey" {
		t.Fatalf("files without prefix get the plugin default, got %q", auth.Prefix)
	}
	if auth.Label != "wokey-1" {
		t.Fatalf("label should fall back to the file name without extension, got %q", auth.Label)
	}
	if auth.Metadata["type"] != wokey.ProviderID {
		t.Fatalf("metadata type = %v", auth.Metadata["type"])
	}
	if !auth.NextRefreshAfter.Equal(now.Add(refreshHorizon)) {
		t.Fatalf("NextRefreshAfter = %v", auth.NextRefreshAfter)
	}
}

func TestMaskKeyAndFileName(t *testing.T) {
	cases := map[string]string{
		"":                      "****",
		"ab":                    "****",
		"abcdef":                "ab****",
		"sk-wk-1234567890abcd":  "sk-wk-…abcd",
		" sk-wk-1234567890abcd": "sk-wk-…abcd",
	}
	for in, want := range cases {
		if got := MaskKey(in); got != want {
			t.Errorf("MaskKey(%q) = %q, want %q", in, got, want)
		}
	}
	name := FileNameForKey("sk-wk-1234567890abcd")
	if name != FileNameForKey(" sk-wk-1234567890abcd ") {
		t.Fatal("file name must not depend on surrounding spaces")
	}
	if !strings.HasPrefix(name, "wokey-") || !strings.HasSuffix(name, ".json") || len(name) != len("wokey-")+10+len(".json") {
		t.Fatalf("file name = %q", name)
	}
	if strings.Contains(name, "1234567890") {
		t.Fatalf("file name leaks the key: %q", name)
	}
}

func TestDecodeFieldsKeepsJSONNumbers(t *testing.T) {
	fields, errDecode := DecodeFields([]byte(`{"priority": 7, "big": 12345678901234567890}`))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if got := jsonfield.Int(fields, "priority"); got != 7 {
		t.Fatalf("priority = %d", got)
	}
	raw, _ := json.Marshal(fields)
	if !strings.Contains(string(raw), "12345678901234567890") {
		t.Fatalf("re-encoding changed a number the plugin does not own: %s", raw)
	}
	if jsonfield.Int(map[string]any{"priority": 3.0}, "priority") != 3 || jsonfield.Int(map[string]any{"priority": 4}, "priority") != 4 {
		t.Fatal("float64/int priority not read")
	}
}

func TestFileStem(t *testing.T) {
	for in, want := range map[string]string{
		"wokey-ab12.json":       "wokey-ab12",
		" /auths/wokey-x.json ": "wokey-x",
		`C:\auths\wokey-y.json`: "wokey-y",
		"plain":                 "plain",
		".json":                 "",
	} {
		if got := fileStem(in); got != want {
			t.Errorf("fileStem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveName(t *testing.T) {
	cases := []struct {
		name, path, want string
	}{
		{"wokey-1.json", "", "wokey-1.json"},
		{"", "/auths/sub/wokey-2.JSON", "wokey-2.JSON"},
		{"label only", `C:\auths\wokey-3.json`, "wokey-3.json"},
		{"x", "/auths/x", ""},
	}
	for _, tc := range cases {
		if got := SaveName(tc.name, tc.path); got != tc.want {
			t.Errorf("SaveName(%q, %q) = %q, want %q", tc.name, tc.path, got, tc.want)
		}
	}
}
