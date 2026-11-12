// Package proxystore keeps the list of named egress proxies managed on the
// plugin page. A credential uses a listed proxy by carrying its URL in
// proxy_url; the list itself is plugin data in <data-dir>/proxies.json.
package proxystore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
)

const (
	fileName = "proxies.json"
	// MaxName bounds a proxy's display name in characters.
	MaxName = 64
	// maxProxies bounds the list so a runaway client cannot grow the file.
	maxProxies = 500
)

var (
	// ErrNotFound reports an unknown proxy ID.
	ErrNotFound = errors.New("proxy not found")
	// ErrDuplicate reports a URL that is already in the list.
	ErrDuplicate = errors.New("proxy already exists")
)

// InputError reports an invalid name or URL.
type InputError struct {
	Field   string
	Message string
}

func (e *InputError) Error() string { return e.Field + ": " + e.Message }

// Proxy is one listed egress proxy. URL is stored as entered (trimmed),
// including any credentials.
type Proxy struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Label is the proxy as scheme://host:port, without credentials or path.
func (p Proxy) Label() string {
	parsed, errParse := url.Parse(p.URL)
	if errParse != nil || parsed.Host == "" {
		return p.URL
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
}

// List is a snapshot of the proxies, in the order they were added.
type List []Proxy

// Get returns the proxy with an ID.
func (l List) Get(id string) (Proxy, bool) {
	id = strings.TrimSpace(id)
	for _, p := range l {
		if p.ID == id {
			return p, true
		}
	}
	return Proxy{}, false
}

// Match returns the proxy whose URL is exactly rawURL (trimmed).
func (l List) Match(rawURL string) (Proxy, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return Proxy{}, false
	}
	for _, p := range l {
		if p.URL == rawURL {
			return p, true
		}
	}
	return Proxy{}, false
}

type fileFormat struct {
	Version int     `json:"version"`
	Proxies []Proxy `json:"proxies"`
}

// Store holds the proxy list. With no directory configured it keeps the
// list in memory only.
type Store struct {
	mu      sync.Mutex
	dir     string
	items   List
	loadErr string
	now     func() time.Time
}

// New returns an empty in-memory store.
func New() *Store {
	return &Store{now: time.Now}
}

// Configure points the store at a data directory and loads its list. It
// does nothing when the directory is unchanged, unless the last load failed.
func (s *Store) Configure(dir string) {
	dir = strings.TrimSpace(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir == s.dir && s.loadErr == "" {
		return
	}
	s.dir, s.items, s.loadErr = dir, nil, ""
	if dir == "" {
		return
	}
	raw, errRead := os.ReadFile(filepath.Join(dir, fileName))
	if errors.Is(errRead, os.ErrNotExist) {
		return
	}
	if errRead != nil {
		s.loadErr = "read proxy list: " + errRead.Error()
		return
	}
	var file fileFormat
	if errUnmarshal := json.Unmarshal(raw, &file); errUnmarshal != nil {
		s.loadErr = "read proxy list: " + errUnmarshal.Error()
		return
	}
	for _, p := range file.Proxies {
		if p.ID == "" || p.URL == "" {
			continue
		}
		s.items = append(s.items, p)
	}
}

// List returns the proxies and the load error, if the file could not be
// read. While the file is unreadable the store refuses changes so it does
// not overwrite it.
func (s *Store) List() (List, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(List(nil), s.items...), s.loadErr
}

// Add appends a proxy. An empty name defaults to the proxy's host:port.
func (s *Store) Add(name, rawURL string) (Proxy, error) {
	rawURL, errURL := checkURL(rawURL)
	if errURL != nil {
		return Proxy{}, errURL
	}
	name, errName := checkName(name, rawURL)
	if errName != nil {
		return Proxy{}, errName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if errWritable := s.writableLocked(); errWritable != nil {
		return Proxy{}, errWritable
	}
	if _, dup := s.items.Match(rawURL); dup {
		return Proxy{}, ErrDuplicate
	}
	if len(s.items) >= maxProxies {
		return Proxy{}, &InputError{Field: "url", Message: fmt.Sprintf("the list is full (%d proxies)", maxProxies)}
	}
	p := Proxy{ID: newID(), Name: name, URL: rawURL, CreatedAt: s.now().UTC().Format(time.RFC3339)}
	next := append(append(List(nil), s.items...), p)
	if errSave := s.saveLocked(next); errSave != nil {
		return Proxy{}, errSave
	}
	s.items = next
	return p, nil
}

// AddMissing appends every URL not yet in the list, named after its
// host:port, and returns how many were added. Invalid URLs are skipped.
func (s *Store) AddMissing(rawURLs []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if errWritable := s.writableLocked(); errWritable != nil {
		return 0, errWritable
	}
	next := append(List(nil), s.items...)
	added := 0
	for _, rawURL := range rawURLs {
		clean, errURL := checkURL(rawURL)
		if errURL != nil {
			continue
		}
		if _, dup := next.Match(clean); dup || len(next) >= maxProxies {
			continue
		}
		name, _ := checkName("", clean)
		next = append(next, Proxy{ID: newID(), Name: name, URL: clean, CreatedAt: s.now().UTC().Format(time.RFC3339)})
		added++
	}
	if added == 0 {
		return 0, nil
	}
	if errSave := s.saveLocked(next); errSave != nil {
		return 0, errSave
	}
	s.items = next
	return added, nil
}

// Update renames a proxy and/or changes its URL; nil leaves a field as it
// is. It returns the proxy before and after the change.
func (s *Store) Update(id string, name, rawURL *string) (before, after Proxy, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if errWritable := s.writableLocked(); errWritable != nil {
		return Proxy{}, Proxy{}, errWritable
	}
	index := s.indexLocked(id)
	if index < 0 {
		return Proxy{}, Proxy{}, ErrNotFound
	}
	before = s.items[index]
	after = before
	if rawURL != nil {
		clean, errURL := checkURL(*rawURL)
		if errURL != nil {
			return before, before, errURL
		}
		if other, dup := s.items.Match(clean); dup && other.ID != before.ID {
			return before, before, ErrDuplicate
		}
		after.URL = clean
	}
	if name != nil {
		clean, errName := checkName(*name, after.URL)
		if errName != nil {
			return before, before, errName
		}
		after.Name = clean
	}
	if after == before {
		return before, after, nil
	}
	next := append(List(nil), s.items...)
	next[index] = after
	if errSave := s.saveLocked(next); errSave != nil {
		return before, before, errSave
	}
	s.items = next
	return before, after, nil
}

// Delete removes a proxy and returns it.
func (s *Store) Delete(id string) (Proxy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if errWritable := s.writableLocked(); errWritable != nil {
		return Proxy{}, errWritable
	}
	index := s.indexLocked(id)
	if index < 0 {
		return Proxy{}, ErrNotFound
	}
	removed := s.items[index]
	next := append(append(List(nil), s.items[:index]...), s.items[index+1:]...)
	if errSave := s.saveLocked(next); errSave != nil {
		return Proxy{}, errSave
	}
	s.items = next
	return removed, nil
}

func (s *Store) indexLocked(id string) int {
	id = strings.TrimSpace(id)
	for i, p := range s.items {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) writableLocked() error {
	if s.loadErr != "" {
		return errors.New(s.loadErr + " (fix or remove " + fileName + " first)")
	}
	return nil
}

// saveLocked writes the list atomically; the file holds proxy credentials,
// so it is readable by the owner only.
func (s *Store) saveLocked(items List) error {
	if s.dir == "" {
		return nil
	}
	if errDir := os.MkdirAll(s.dir, 0o700); errDir != nil {
		return fmt.Errorf("save proxy list: %w", errDir)
	}
	if items == nil {
		items = List{}
	}
	raw, errMarshal := json.MarshalIndent(fileFormat{Version: 1, Proxies: items}, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("save proxy list: %w", errMarshal)
	}
	tmp, errTemp := os.CreateTemp(s.dir, fileName+".*.tmp")
	if errTemp != nil {
		return fmt.Errorf("save proxy list: %w", errTemp)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if errChmod := tmp.Chmod(0o600); errChmod != nil {
		_ = tmp.Close()
		return fmt.Errorf("save proxy list: %w", errChmod)
	}
	if _, errWrite := tmp.Write(append(raw, '\n')); errWrite != nil {
		_ = tmp.Close()
		return fmt.Errorf("save proxy list: %w", errWrite)
	}
	if errSync := tmp.Sync(); errSync != nil {
		_ = tmp.Close()
		return fmt.Errorf("save proxy list: %w", errSync)
	}
	if errClose := tmp.Close(); errClose != nil {
		return fmt.Errorf("save proxy list: %w", errClose)
	}
	if errRename := os.Rename(tmpName, filepath.Join(s.dir, fileName)); errRename != nil {
		return fmt.Errorf("save proxy list: %w", errRename)
	}
	return nil
}

// checkURL accepts a real proxy URL; the built-in choices (inherit, direct)
// are not list entries.
func checkURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "":
		return "", &InputError{Field: "url", Message: "a proxy URL is required"}
	case "direct", "none":
		return "", &InputError{Field: "url", Message: "direct is built in; enter a proxy URL"}
	}
	if errProxy := wokey.ValidateProxy(raw); errProxy != nil {
		return "", &InputError{Field: "url", Message: errProxy.Error()}
	}
	return raw, nil
}

func checkName(name, rawURL string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		if parsed, errParse := url.Parse(rawURL); errParse == nil && parsed.Host != "" {
			name = parsed.Host
		} else {
			name = rawURL
		}
	}
	if utf8.RuneCountInString(name) > MaxName {
		return "", &InputError{Field: "name", Message: fmt.Sprintf("name must be at most %d characters", MaxName)}
	}
	return name, nil
}

func newID() string {
	var buf [6]byte
	if _, errRand := rand.Read(buf[:]); errRand != nil {
		return fmt.Sprintf("p%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
