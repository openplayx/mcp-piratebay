package netguard

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/openplayx/mcp-piratebay/src/internal/config"
)

func src(t *testing.T, extra string) config.Source {
	c, err := config.Parse([]byte(`{"source": {"base_url": "https://tpb.test"` + extra + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	return c.Source
}

func ok(t *testing.T, raw string, s config.Source, addrs ...string) bool {
	if len(addrs) == 0 {
		addrs = []string{"93.184.216.34"}
	}
	u, _ := url.Parse(raw)
	err := CheckURL(context.Background(), u, s, func(context.Context, string, int) ([]string, error) { return addrs, nil })
	if err != nil && err.Code != "source_blocked" && err.Code != "upstream_unavailable" {
		t.Fatal(err.Code)
	}
	return err == nil
}

func TestNetguard(t *testing.T) {
	s := src(t, "")
	if !ok(t, "https://tpb.test/search/x/1/99/0", s) {
		t.Fatal("allowed host")
	}
	for _, u := range []string{"http://tpb.test/search", "https://evil.test/", "https://tpb.test:8443/"} {
		if ok(t, u, s) {
			t.Error(u)
		}
	}
	for _, a := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.211", "169.254.169.254", "::1", "fc00::1", "::ffff:127.0.0.1", "0.0.0.0"} {
		if ok(t, "https://tpb.test/", s, a) {
			t.Error(a)
		}
	}
	if ok(t, "https://tpb.test/", s, "93.184.216.34", "10.0.0.1") {
		t.Fatal("one bad address is enough to refuse")
	}
	if !ok(t, "https://tpb.test/", src(t, `, "allow_private_addresses": true`), "192.168.1.211") {
		t.Fatal("private allowed")
	}
	if !ok(t, "https://mirror.test/", src(t, `, "allowed_hosts": ["mirror.test"]`)) {
		t.Fatal("extra allowed host")
	}
	u, _ := url.Parse("https://tpb.test/")
	err := CheckURL(context.Background(), u, s, func(context.Context, string, int) ([]string, error) { return nil, errors.New("nxdomain") })
	if err == nil || err.Code != "upstream_unavailable" {
		t.Fatal(err)
	}
}
