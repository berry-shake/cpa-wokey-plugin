package wokey

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Egress is a resolved egress choice for one upstream call: an explicit
// proxy, a direct connection or the process environment.
type Egress struct {
	// key identifies the cached client: "env", "direct" or the proxy URL.
	key string
	url *url.URL
}

// ResolveEgress mirrors CPA's proxy semantics: the auth file proxy_url wins,
// then the global proxy-url from the CPA config, then the process environment.
// "direct" or "none" disables proxying explicitly.
func ResolveEgress(authProxy, globalProxy string) (Egress, error) {
	for _, candidate := range []string{authProxy, globalProxy} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		switch strings.ToLower(candidate) {
		case "direct", "none":
			return Egress{key: "direct"}, nil
		}
		parsed, errParse := parseProxyURL(candidate)
		if errParse != nil {
			return Egress{}, errParse
		}
		return Egress{key: parsed.String(), url: parsed}, nil
	}
	return Egress{key: "env"}, nil
}

// Label names the egress for the request log: "direct", "env" or the proxy
// as scheme://host:port, without credentials or path.
func (e Egress) Label() string {
	if e.url == nil {
		return e.key
	}
	return (&url.URL{Scheme: e.url.Scheme, Host: e.url.Host}).String()
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

// ValidateProxy checks a proxy_url value entered through the management
// page. Empty, direct and none are accepted.
func ValidateProxy(raw string) error {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "", "direct", "none":
		return nil
	}
	_, errParse := parseProxyURL(raw)
	return errParse
}

// Queue lets one holder at a time through each egress. Waiters get their
// turn in the order they arrived; different egresses do not wait for each
// other.
type Queue struct {
	mu    sync.Mutex
	slots map[string]chan struct{}
}

// NewQueue returns a queue with no egress in use.
func NewQueue() *Queue {
	return &Queue{slots: make(map[string]chan struct{})}
}

// Acquire waits for the egress's turn and returns the function that ends
// it. It gives up with ctx's error when ctx ends first.
func (q *Queue) Acquire(ctx context.Context, e Egress) (release func(), err error) {
	q.mu.Lock()
	slot, ok := q.slots[e.key]
	if !ok {
		slot = make(chan struct{}, 1)
		q.slots[e.key] = slot
	}
	q.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Clients keeps one HTTP client per egress so connections are reused
// across requests that share a proxy.
type Clients struct {
	mu      sync.Mutex
	clients map[string]*http.Client
}

// NewClients returns an empty client cache.
func NewClients() *Clients {
	return &Clients{clients: make(map[string]*http.Client)}
}

// Client returns the shared client for an egress, creating it on first use.
func (c *Clients) Client(setting Egress) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.clients[setting.key]; ok {
		return client
	}
	// Request deadlines come from contexts; the client itself has no timeout
	// so long streams are not cut off.
	client := &http.Client{Transport: NewTransport(setting)}
	c.clients[setting.key] = client
	return client
}

// NewTransport builds an HTTP transport that goes out through an egress.
func NewTransport(setting Egress) *http.Transport {
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
	return transport
}

// CloseIdle closes idle connections of every cached client.
func (c *Clients) CloseIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, client := range c.clients {
		client.CloseIdleConnections()
	}
}
