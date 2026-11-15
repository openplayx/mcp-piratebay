package normalize

import (
	"math"
	"strings"
	"testing"
)

func p(s string) *string { return &s }

func TestSizeBytes(t *testing.T) {
	cases := map[string]int64{
		"2.99 GiB":   int64(math.RoundToEven(2.99 * (1 << 30))),
		"956.66 MiB": int64(math.RoundToEven(956.66 * (1 << 20))),
		"12 B":       12,
		"1 TiB":      1 << 40,
	}
	for in, want := range cases {
		if got := SizeBytes(p(in)); got == nil || *got != want {
			t.Errorf("%q: %v", in, got)
		}
	}
	for _, in := range []string{"", "huge", "1.5 GB"} {
		if got := SizeBytes(p(in)); got != nil {
			t.Errorf("%q: %v", in, *got)
		}
	}
	if n := ExactSizeBytes(p("6.07 GiB (6518974464 Bytes)")); n == nil || *n != 6518974464 {
		t.Fatal(n)
	}
	if ExactSizeBytes(p("6.07 GiB")) != nil {
		t.Fatal("no exact size")
	}
}

func TestDates(t *testing.T) {
	ok := map[string]string{"04-01 2019": "2019-04-01", "12-15 2018": "2018-12-15"}
	for in, want := range ok {
		if got := ListDate(p(in)); got == nil || *got != want {
			t.Errorf("%q: %v", in, got)
		}
	}
	for _, in := range []string{"02-30 2020", "11-11 09:55", "Today 01:00", "Y-day 10:00", "3 mins ago"} {
		if got := ListDate(p(in)); got != nil {
			t.Errorf("%q: %v", in, *got)
		}
	}
	if ListDate(nil) != nil {
		t.Fatal("nil")
	}
	if got := DetailDatetime(p("2026-04-25 16:35:39 GMT")); got == nil || *got != "2026-04-25T16:35:39Z" {
		t.Fatal(got)
	}
	if DetailDatetime(p("2026-04-25 16:35:39")) != nil || DetailDatetime(p("yesterday")) != nil {
		t.Fatal("invalid detail dates")
	}
}

func TestInfoHash(t *testing.T) {
	if h := InfoHash(strings.Repeat("A", 40)); *h != strings.Repeat("a", 40) {
		t.Fatal(*h)
	}
	if h := InfoHash(strings.Repeat("A", 32)); *h != strings.Repeat("00", 20) {
		t.Fatal(*h)
	}
	if InfoHash("xyz") != nil || InfoHash(strings.Repeat("g", 40)) != nil {
		t.Fatal("invalid hashes")
	}
}

func TestMagnetRebuiltFromValidatedParts(t *testing.T) {
	m := ParseMagnet(p("magnet:?xt=urn:btih:" + strings.Repeat("AB", 20) +
		"&dn=My+File%0A&tr=udp%3A%2F%2Ft.example%3A80&tr=file%3A%2F%2F%2Fetc%2Fpasswd" +
		"&xs=http%3A%2F%2Fevil%2Fx.torrent&as=http%3A%2F%2Fevil"))
	if m == nil || m.InfoHash != strings.Repeat("ab", 20) || *m.DisplayName != "My File" ||
		len(m.Trackers) != 1 || m.Trackers[0] != "udp://t.example:80" {
		t.Fatalf("%+v", m)
	}
	if m.URI != "magnet:?xt=urn:btih:"+strings.Repeat("ab", 20)+"&dn=My%20File&tr=udp%3A%2F%2Ft.example%3A80" || strings.Contains(m.URI, "evil") {
		t.Fatal(m.URI)
	}
	for _, in := range []*string{nil, p(""), p("http://example.com/x.torrent"), p("magnet:?dn=no-hash"),
		p("magnet:?xt=urn:btih:short"), p("magnet:?xt=urn:btih:" + strings.Repeat("a", 40) + "&xt=urn:btih:" + strings.Repeat("b", 40)),
		p("magnet:?xt=urn:btmh:1220" + strings.Repeat("a", 64)), p("magnet:?xt=urn:btih:" + strings.Repeat("a", 40) + "&tr=" + strings.Repeat("u", 9000))} {
		if ParseMagnet(in) != nil {
			t.Errorf("accepted %v", in)
		}
	}
	var trs strings.Builder
	for i := range 50 {
		trs.WriteString("&tr=udp%3A%2F%2Ft" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".example%3A80")
	}
	if m := ParseMagnet(p("magnet:?xt=urn:btih:" + strings.Repeat("a", 40) + trs.String())); len(m.Trackers) != MaxTrackers {
		t.Fatal(len(m.Trackers))
	}
}

func TestText(t *testing.T) {
	if got := Text(p(" a‮b\x00c\n d "), 50, false); *got != "abc d" {
		t.Fatalf("%q", *got)
	}
	if got := Text(p("line1\r\nline2\x1b[31m"), 50, true); *got != "line1\nline2[31m" {
		t.Fatalf("%q", *got)
	}
	if Text(p("   "), 10, false) != nil {
		t.Fatal("blank")
	}
	if got := Text(p(strings.Repeat("x", 20)), 5, false); *got != "xxxxx" {
		t.Fatal(*got)
	}
	if got := Text(p("é"), 5, false); *got != "é" {
		t.Fatal("NFC")
	}
}
