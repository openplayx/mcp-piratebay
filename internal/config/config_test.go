package config

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestExamplesAreValid(t *testing.T) {
	data, _ := os.ReadFile("../../deploy/config.example.json")
	if _, err := Parse(data); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile("../../deploy/secrets.example.json")
	if _, err := ParseSecrets(data); err != nil {
		t.Fatal(err)
	}
}

func TestValidation(t *testing.T) {
	for _, bad := range []string{
		`{"source": {"base_url": "http://tpb.test"}}`,
		`{"source": {"base_url": "https://tpb.test/path"}}`,
		`{"source": {"base_url": "https://user:pw@tpb.test"}}`,
		`{"unknown": 1}`,
		`{"cache": {"result_ttl_seconds": 10}}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	c, err := Parse([]byte(`{"source": {"base_url": "http://tpb.test", "allow_http": true}}`))
	if err != nil || c.Source.Port() != 80 {
		t.Fatal(c.Source.Port(), err)
	}
	c, _ = Parse([]byte(`{"source": {"base_url": "https://TPB.test/"}}`))
	if !slices.Equal(c.Source.AllowedHosts, []string{"tpb.test"}) || c.Source.BaseURL != "https://TPB.test" {
		t.Fatal(c.Source.AllowedHosts, c.Source.BaseURL)
	}
	for _, bad := range []string{
		`{"tokens": [{"name": "a", "scopes": ["search"]}]}`,
		`{"tokens": [{"name": "a", "token": "short"}]}`,
		`{"tokens": [{"name": "a", "token": "` + strings.Repeat("x", 30) + `", "scopes": ["admin"]}]}`,
	} {
		if _, err := ParseSecrets([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	cfg, _ := Parse([]byte(`{"source": {"proxy_ref": "home"}}`))
	if _, err := ResolveProxy(cfg, Secrets{}); err == nil {
		t.Fatal("undefined proxy_ref")
	}
	if p, _ := ResolveProxy(cfg, Secrets{Proxies: map[string]string{"home": "socks5://127.0.0.1:1080"}}); p != "socks5://127.0.0.1:1080" {
		t.Fatal(p)
	}
}
