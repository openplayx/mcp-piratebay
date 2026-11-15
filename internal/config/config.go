// Package config loads config.json and secrets.json.
//
// config.json holds no credentials; secrets.json holds API tokens and proxy
// URLs and is never committed. Source addresses and proxies are an
// administrator decision only; nothing here is reachable through the API.
// Both files are decoded strictly: unknown keys are an error.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Server struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	DocsPublic bool   `json:"docs_public"`
	LogLevel   string `json:"log_level"`
}

type Source struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	// Hosts requests may go to. Always includes the base_url host.
	AllowedHosts []string `json:"allowed_hosts"`
	// Test fixtures on a LAN only; production keeps public addresses only.
	AllowPrivateAddresses bool `json:"allow_private_addresses"`
	AllowHTTP             bool `json:"allow_http"`
	// Key into secrets.json "proxies"; the URL may carry credentials.
	ProxyRef         *string `json:"proxy_ref"`
	TimeoutSeconds   float64 `json:"timeout_seconds"`
	MaxConcurrency   int     `json:"max_concurrency"`
	MinIntervalMS    int     `json:"min_interval_ms"`
	MaxResponseBytes int64   `json:"max_response_bytes"`
	UserAgent        string  `json:"user_agent"`
}

// Scheme, Host and Port of base_url (valid after Parse).
func (s Source) parsed() *url.URL { u, _ := url.Parse(s.BaseURL); return u }
func (s Source) Scheme() string   { return s.parsed().Scheme }
func (s Source) Host() string     { return strings.ToLower(s.parsed().Hostname()) }
func (s Source) Port() int {
	u := s.parsed()
	if p, err := strconv.Atoi(u.Port()); err == nil {
		return p
	}
	if u.Scheme == "https" {
		return 443
	}
	return 80
}

type Cache struct {
	Path             string `json:"path"`
	SearchTTLSeconds int    `json:"search_ttl_seconds"`
	DetailTTLSeconds int    `json:"detail_ttl_seconds"`
	ResultTTLSeconds int    `json:"result_ttl_seconds"`
	MaxEntries       int    `json:"max_entries"`
}

type Limits struct {
	QueryMaxLength int `json:"query_max_length"`
	MaxPage        int `json:"max_page"`
	// Upper bound of items returned for one source page (the source decides
	// the page size: ~30 for lists, 100 for top). Exceeding it is reported.
	MaxResults        int `json:"max_results"`
	RequestsPerMinute int `json:"requests_per_minute"`
}

type Config struct {
	SchemaVersion        int    `json:"schema_version"`
	Server               Server `json:"server"`
	Source               Source `json:"source"`
	Cache                Cache  `json:"cache"`
	Limits               Limits `json:"limits"`
	AllowAdultCategories bool   `json:"allow_adult_categories"`
}

func Default() Config {
	return Config{
		SchemaVersion: 1,
		Server:        Server{Host: "127.0.0.1", Port: 8790, DocsPublic: true, LogLevel: "INFO"},
		Source: Source{
			ID: "tpb", Name: "The Pirate Bay (mirror)", BaseURL: "https://thepiratebay10.info",
			AllowedHosts: []string{}, TimeoutSeconds: 10, MaxConcurrency: 2, MinIntervalMS: 500,
			MaxResponseBytes: 4 << 20, UserAgent: "Mozilla/5.0 (compatible; OpenPlayX-TorrentSearch/0.1)",
		},
		Cache: Cache{Path: "data/torrent-search.sqlite3", SearchTTLSeconds: 300, DetailTTLSeconds: 600,
			ResultTTLSeconds: 3600, MaxEntries: 5000},
		Limits: Limits{QueryMaxLength: 120, MaxPage: 50, MaxResults: 100, RequestsPerMinute: 60},
	}
}

var sourceID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// normalize validates the configuration and fills derived values.
func (c *Config) normalize() error {
	var errs []error
	check := func(ok bool, format string, a ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, a...))
		}
	}
	check(c.SchemaVersion == 1, "schema_version must be 1")
	check(c.Server.Port >= 1 && c.Server.Port <= 65535, "server.port must be 1-65535")
	check(slices.Contains([]string{"DEBUG", "INFO", "WARNING", "ERROR"}, c.Server.LogLevel),
		"server.log_level must be DEBUG, INFO, WARNING or ERROR")
	s := &c.Source
	check(sourceID.MatchString(s.ID), "source.id is invalid")
	check(utf8.RuneCountInString(s.Name) >= 1 && utf8.RuneCountInString(s.Name) <= 80, "source.name must be 1-80 characters")
	u, err := url.Parse(s.BaseURL)
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "":
		errs = append(errs, errors.New("source.base_url must be an absolute http(s) URL"))
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		errs = append(errs, errors.New("source.base_url must not contain a path, query or fragment"))
	case u.User != nil:
		errs = append(errs, errors.New("source.base_url must not contain credentials"))
	case u.Scheme == "http" && !s.AllowHTTP:
		errs = append(errs, errors.New("http base_url requires allow_http"))
	default:
		s.BaseURL = strings.TrimRight(s.BaseURL, "/")
		hosts := map[string]bool{strings.ToLower(u.Hostname()): true}
		for _, h := range s.AllowedHosts {
			hosts[strings.ToLower(h)] = true
		}
		s.AllowedHosts = s.AllowedHosts[:0]
		for h := range hosts {
			s.AllowedHosts = append(s.AllowedHosts, h)
		}
		sort.Strings(s.AllowedHosts)
	}
	check(s.TimeoutSeconds > 0 && s.TimeoutSeconds <= 60, "source.timeout_seconds must be in (0, 60]")
	check(s.MaxConcurrency >= 1 && s.MaxConcurrency <= 16, "source.max_concurrency must be 1-16")
	check(s.MinIntervalMS >= 0 && s.MinIntervalMS <= 60000, "source.min_interval_ms must be 0-60000")
	check(s.MaxResponseBytes >= 64<<10 && s.MaxResponseBytes <= 32<<20, "source.max_response_bytes must be 64 KiB-32 MiB")
	check(len(s.UserAgent) <= 200, "source.user_agent must be at most 200 characters")
	k := c.Cache
	check(k.SearchTTLSeconds >= 0 && k.SearchTTLSeconds <= 86400, "cache.search_ttl_seconds must be 0-86400")
	check(k.DetailTTLSeconds >= 0 && k.DetailTTLSeconds <= 86400, "cache.detail_ttl_seconds must be 0-86400")
	check(k.ResultTTLSeconds >= 60 && k.ResultTTLSeconds <= 7*86400, "cache.result_ttl_seconds must be 60-604800")
	check(k.MaxEntries >= 100 && k.MaxEntries <= 1_000_000, "cache.max_entries must be 100-1000000")
	l := c.Limits
	check(l.QueryMaxLength >= 1 && l.QueryMaxLength <= 500, "limits.query_max_length must be 1-500")
	check(l.MaxPage >= 1 && l.MaxPage <= 1000, "limits.max_page must be 1-1000")
	check(l.MaxResults >= 1 && l.MaxResults <= 500, "limits.max_results must be 1-500")
	check(l.RequestsPerMinute >= 1 && l.RequestsPerMinute <= 10000, "limits.requests_per_minute must be 1-10000")
	return errors.Join(errs...)
}

type Token struct {
	Name        string   `json:"name"`
	Token       *string  `json:"token"`
	TokenSHA256 *string  `json:"token_sha256"`
	Scopes      []string `json:"scopes"`
}

func (t Token) Digest() string {
	if t.TokenSHA256 != nil {
		return *t.TokenSHA256
	}
	sum := sha256.Sum256([]byte(*t.Token))
	return hex.EncodeToString(sum[:])
}

type Secrets struct {
	Tokens  []Token           `json:"tokens"`
	Proxies map[string]string `json:"proxies"`
}

var (
	tokenName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	hex64     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	scopes    = []string{"search", "resolve"}
)

func (s *Secrets) normalize() error {
	var errs []error
	seen := map[string]bool{}
	for i := range s.Tokens {
		t := &s.Tokens[i]
		if !tokenName.MatchString(t.Name) {
			errs = append(errs, fmt.Errorf("tokens[%d].name is invalid", i))
		}
		if seen[t.Name] {
			errs = append(errs, errors.New("token names must be unique"))
		}
		seen[t.Name] = true
		if (t.Token == nil) == (t.TokenSHA256 == nil) {
			errs = append(errs, fmt.Errorf("tokens[%d]: set exactly one of token / token_sha256", i))
		}
		if t.Token != nil && (len(*t.Token) < 24 || len(*t.Token) > 512) {
			errs = append(errs, fmt.Errorf("tokens[%d].token must be 24-512 characters", i))
		}
		if t.TokenSHA256 != nil && !hex64.MatchString(*t.TokenSHA256) {
			errs = append(errs, fmt.Errorf("tokens[%d].token_sha256 must be 64 lowercase hex characters", i))
		}
		if t.Scopes == nil {
			t.Scopes = []string{"search"}
		}
		for _, sc := range t.Scopes {
			if !slices.Contains(scopes, sc) {
				errs = append(errs, fmt.Errorf("tokens[%d]: unknown scope %q", i, sc))
			}
		}
	}
	if s.Proxies == nil {
		s.Proxies = map[string]string{}
	}
	return errors.Join(errs...)
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	return nil
}

func Parse(data []byte) (Config, error) {
	c := Default()
	if err := decodeStrict(data, &c); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if c.Source.AllowedHosts == nil {
		c.Source.AllowedHosts = []string{}
	}
	if err := c.normalize(); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

func Load(path string) (Config, error) {
	if path == "" {
		c := Default()
		return c, c.normalize()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(data)
}

func ParseSecrets(data []byte) (Secrets, error) {
	var s Secrets
	if err := decodeStrict(data, &s); err != nil {
		return s, fmt.Errorf("secrets: %w", err)
	}
	if err := s.normalize(); err != nil {
		return s, fmt.Errorf("secrets: %w", err)
	}
	return s, nil
}

func LoadSecrets(path string) (Secrets, error) {
	if path == "" {
		return Secrets{Proxies: map[string]string{}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Secrets{}, err
	}
	return ParseSecrets(data)
}

// ResolveProxy returns the proxy URL named by source.proxy_ref, if any.
func ResolveProxy(c Config, s Secrets) (string, error) {
	if c.Source.ProxyRef == nil {
		return "", nil
	}
	p, ok := s.Proxies[*c.Source.ProxyRef]
	if !ok {
		return "", fmt.Errorf("proxy_ref %q is not defined in secrets", *c.Source.ProxyRef)
	}
	return p, nil
}
