package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultBaseURL               = "https://api.wokey.ai"
	defaultModelPrefix           = "wokey"
	defaultModelsRefreshMinutes  = 60
	defaultRequestTimeoutSeconds = 600
	defaultStreamIdleSeconds     = 300
	defaultDataDir               = "plugins/wokey-data"
	defaultLogRetention          = 100000
	defaultLogRetentionDays      = 30
	maxLogRetention              = 1000000
)

// Config is the plugin-owned subtree under plugins.configs.wokey in the CPA
// config file. CPA hands the plugin only that subtree (plus its own enabled and
// priority keys, which are ignored here).
type Config struct {
	// BaseURL is the Wokey API origin; paths such as /v1/messages are appended.
	BaseURL string
	// ModelPrefix is the default CPA auth prefix for Wokey keys ("wokey" ->
	// "wokey/claude-fable-5"); CPA applies it with its native prefix handling.
	// Empty adds no prefix. An auth file's own prefix field wins.
	ModelPrefix string
	// ModelsRefreshInterval is how often the public model catalog is polled.
	// Zero disables polling; the catalog is still loaded on demand.
	ModelsRefreshInterval time.Duration
	// RequestTimeout bounds non-streaming upstream calls.
	RequestTimeout time.Duration
	// StreamIdleTimeout aborts a stream that has produced no bytes for this long.
	StreamIdleTimeout time.Duration
	// DataDir holds plugin data (the request log). Relative paths resolve
	// against CPA's working directory.
	DataDir string
	// LogRetention is how many requests the request log keeps; 0 disables it.
	LogRetention int
	// LogRetentionDays drops request log entries older than this many days;
	// 0 keeps them until LogRetention pushes them out.
	LogRetentionDays int
}

type rawConfig struct {
	BaseURL               string  `yaml:"base-url"`
	ModelPrefix           *string `yaml:"model-prefix"`
	ModelsRefreshMinutes  *int    `yaml:"models-refresh-minutes"`
	RequestTimeoutSeconds *int    `yaml:"request-timeout-seconds"`
	StreamIdleSeconds     *int    `yaml:"stream-idle-timeout-seconds"`
	DataDir               string  `yaml:"data-dir"`
	LogRetention          *int    `yaml:"log-retention"`
	LogRetentionDays      *int    `yaml:"log-retention-days"`
}

func Default() Config {
	return Config{
		BaseURL:               defaultBaseURL,
		ModelPrefix:           defaultModelPrefix,
		ModelsRefreshInterval: defaultModelsRefreshMinutes * time.Minute,
		RequestTimeout:        defaultRequestTimeoutSeconds * time.Second,
		StreamIdleTimeout:     defaultStreamIdleSeconds * time.Second,
		DataDir:               defaultDataDir,
		LogRetention:          defaultLogRetention,
		LogRetentionDays:      defaultLogRetentionDays,
	}
}

// Parse reads the plugin config subtree. Invalid values fall back to the
// defaults instead of failing registration, because a broken plugin config
// should not take every Wokey credential offline.
func Parse(raw []byte) (Config, []string) {
	cfg := Default()
	var warnings []string
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}
	var parsed rawConfig
	if errUnmarshal := yaml.Unmarshal(raw, &parsed); errUnmarshal != nil {
		return cfg, []string{"invalid plugin config, using defaults: " + errUnmarshal.Error()}
	}
	if base := strings.TrimSpace(parsed.BaseURL); base != "" {
		if normalized, errBase := NormalizeBaseURL(base); errBase != nil {
			warnings = append(warnings, "ignoring base-url: "+errBase.Error())
		} else {
			cfg.BaseURL = normalized
		}
	}
	if parsed.ModelPrefix != nil {
		cfg.ModelPrefix = NormalizePrefix(*parsed.ModelPrefix)
	}
	if parsed.ModelsRefreshMinutes != nil {
		if minutes := *parsed.ModelsRefreshMinutes; minutes >= 0 {
			cfg.ModelsRefreshInterval = time.Duration(minutes) * time.Minute
		} else {
			warnings = append(warnings, "ignoring negative models-refresh-minutes")
		}
	}
	if parsed.RequestTimeoutSeconds != nil {
		if seconds := *parsed.RequestTimeoutSeconds; seconds > 0 {
			cfg.RequestTimeout = time.Duration(seconds) * time.Second
		} else {
			warnings = append(warnings, "ignoring non-positive request-timeout-seconds")
		}
	}
	if parsed.StreamIdleSeconds != nil {
		if seconds := *parsed.StreamIdleSeconds; seconds > 0 {
			cfg.StreamIdleTimeout = time.Duration(seconds) * time.Second
		} else {
			warnings = append(warnings, "ignoring non-positive stream-idle-timeout-seconds")
		}
	}
	if dir := strings.TrimSpace(parsed.DataDir); dir != "" {
		cfg.DataDir = dir
	}
	if parsed.LogRetention != nil {
		if n := *parsed.LogRetention; n >= 0 {
			cfg.LogRetention = min(n, maxLogRetention)
		} else {
			warnings = append(warnings, "ignoring negative log-retention")
		}
	}
	if parsed.LogRetentionDays != nil {
		if n := *parsed.LogRetentionDays; n >= 0 {
			cfg.LogRetentionDays = min(n, 3650)
		} else {
			warnings = append(warnings, "ignoring negative log-retention-days")
		}
	}
	return cfg, warnings
}

// NormalizeBaseURL accepts an http(s) origin, optionally with a path prefix,
// and strips a trailing slash and a trailing /v1 so endpoint paths can be
// appended uniformly.
func NormalizeBaseURL(raw string) (string, error) {
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil {
		return "", errParse
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("host is required")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.Path = strings.TrimSuffix(parsed.Path, "/v1")
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// NormalizePrefix trims whitespace and surrounding slashes so "wokey/" and
// "/wokey" both mean "wokey".
func NormalizePrefix(raw string) string {
	return strings.Trim(strings.TrimSpace(raw), "/")
}
