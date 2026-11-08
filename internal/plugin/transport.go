package plugin

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// proxySetting is a resolved egress choice for one upstream call.
type proxySetting struct {
	// key identifies the cached client: "env", "direct" or the proxy URL.
	key string
	url *url.URL
}

// resolveProxy mirrors CPA's proxy semantics: the auth file proxy_url wins,
// then the global proxy-url from the CPA config, then the process environment.
// "direct" or "none" disables proxying explicitly.
func resolveProxy(authProxy, globalProxy string) (proxySetting, error) {
	for _, candidate := range []string{authProxy, globalProxy} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		switch strings.ToLower(candidate) {
		case "direct", "none":
			return proxySetting{key: "direct"}, nil
		}
		parsed, errParse := parseProxyURL(candidate)
		if errParse != nil {
			return proxySetting{}, errParse
		}
		return proxySetting{key: parsed.String(), url: parsed}, nil
	}
	return proxySetting{key: "env"}, nil
}

func parseProxyURL(raw string) (*url.URL, error) {
	parsed, errParse := url.Parse(raw)
	if errParse != nil {
		return nil, fmt.Errorf("invalid proxy_url: %w", errParse)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("invalid proxy_url: unsupported scheme %q (use http, https, socks5 or socks5h)", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("invalid proxy_url: host is required")
	}
	return parsed, nil
}

// validateProxyValue checks a proxy_url value entered through the management
// page. Empty, direct and none are accepted.
func validateProxyValue(raw string) error {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "", "direct", "none":
		return nil
	}
	_, errParse := parseProxyURL(raw)
	return errParse
}

// clientCache keeps one HTTP client per egress so connections are reused
// across requests that share a proxy.
type clientCache struct {
	mu      sync.Mutex
	clients map[string]*http.Client
}

func newClientCache() *clientCache {
	return &clientCache{clients: make(map[string]*http.Client)}
}

func (c *clientCache) client(setting proxySetting) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.clients[setting.key]; ok {
		return client
	}
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	switch {
	case setting.url != nil:
		transport.Proxy = http.ProxyURL(setting.url)
	case setting.key == "direct":
		transport.Proxy = nil
	default:
		transport.Proxy = http.ProxyFromEnvironment
	}
	// Request deadlines come from contexts; the client itself has no timeout
	// so long streams are not cut off.
	client := &http.Client{Transport: transport}
	c.clients[setting.key] = client
	return client
}

func (c *clientCache) closeIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, client := range c.clients {
		client.CloseIdleConnections()
	}
}
