package wokey

import "testing"

func TestResolveEgress(t *testing.T) {
	cases := []struct {
		auth, global, key string
	}{
		{"", "", "env"},
		{"", "http://g.example:8080", "http://g.example:8080"},
		{"socks5://u:p@a.example:1080", "http://g.example:8080", "socks5://u:p@a.example:1080"},
		{"direct", "http://g.example:8080", "direct"},
		{"NONE", "", "direct"},
		{"", "direct", "direct"},
	}
	for _, tc := range cases {
		setting, errProxy := ResolveEgress(tc.auth, tc.global)
		if errProxy != nil || setting.key != tc.key {
			t.Errorf("ResolveEgress(%q, %q) = %+v, %v; want %s", tc.auth, tc.global, setting, errProxy, tc.key)
		}
	}
	if _, errProxy := ResolveEgress("ftp://x.example", ""); errProxy == nil {
		t.Error("ftp proxy accepted")
	}
	for _, ok := range []string{"", "direct", "none", "http://h:1", "https://h:1", "socks5h://h:1"} {
		if errProxy := ValidateProxy(ok); errProxy != nil {
			t.Errorf("ValidateProxy(%q) = %v", ok, errProxy)
		}
	}
	for _, bad := range []string{"h:1080", "socks4://h:1", "http://"} {
		if errProxy := ValidateProxy(bad); errProxy == nil {
			t.Errorf("ValidateProxy(%q) accepted", bad)
		}
	}
}
