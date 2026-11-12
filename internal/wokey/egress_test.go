package wokey

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResolveEgress(t *testing.T) {
	cases := []struct {
		auth, global, key, label string
	}{
		{"", "", "env", "env"},
		{"", "http://g.example:8080", "http://g.example:8080", "http://g.example:8080"},
		{"socks5://u:p@a.example:1080/x", "http://g.example:8080", "socks5://u:p@a.example:1080/x", "socks5://a.example:1080"},
		{"direct", "http://g.example:8080", "direct", "direct"},
		{"NONE", "", "direct", "direct"},
		{"", "direct", "direct", "direct"},
	}
	for _, tc := range cases {
		setting, errProxy := ResolveEgress(tc.auth, tc.global)
		if errProxy != nil || setting.key != tc.key || setting.Label() != tc.label {
			t.Errorf("ResolveEgress(%q, %q) = %+v (label %q), %v; want %s, %s", tc.auth, tc.global, setting, setting.Label(), errProxy, tc.key, tc.label)
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

func TestQueueOneAtATimePerEgress(t *testing.T) {
	q := NewQueue()
	proxyA, _ := ResolveEgress("socks5://a.example:1080", "")
	sameProxyA, _ := ResolveEgress("", "socks5://a.example:1080")
	direct, _ := ResolveEgress("direct", "")
	bg := context.Background()

	release, errAcquire := q.Acquire(bg, proxyA)
	if errAcquire != nil {
		t.Fatal(errAcquire)
	}
	// Another egress is not held up.
	short, cancel := context.WithTimeout(bg, time.Second)
	releaseDirect, errDirect := q.Acquire(short, direct)
	cancel()
	if errDirect != nil {
		t.Fatalf("direct waited for the proxy: %v", errDirect)
	}
	releaseDirect()
	// The same proxy, whichever setting chose it, waits; the wait ends with ctx.
	short, cancel = context.WithTimeout(bg, 30*time.Millisecond)
	_, errWait := q.Acquire(short, sameProxyA)
	cancel()
	if !errors.Is(errWait, context.DeadlineExceeded) {
		t.Fatalf("second holder of the same egress: %v", errWait)
	}
	// It gets its turn once the first holder is done.
	got := make(chan struct{})
	go func() {
		releaseNext, errNext := q.Acquire(bg, sameProxyA)
		if errNext == nil {
			releaseNext()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("the waiter went ahead while the egress was held")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("the waiter never got its turn")
	}
}
