package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefsAndCache(t *testing.T) {
	s, err := Open(":memory:", 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := 1000.0
	s.Now = func() float64 { return now }
	ha, hb := strings.Repeat("a", 40), strings.Repeat("b", 40)
	a, _ := s.Issue("p", "tpb", "1", &ha, "T", 60)
	if b, _ := s.Issue("p", "tpb", "1", &ha, "T", 60); b.Ref != a.Ref {
		t.Fatal("same revision, same ref")
	}
	if c, _ := s.Issue("p", "tpb", "1", &hb, "T", 60); c.Ref == a.Ref {
		t.Fatal("new revision, new ref")
	}
	if c, _ := s.Lookup(a.Ref, "other"); c != nil {
		t.Fatal("bound to principal")
	}
	now += 61
	if c, _ := s.Lookup(a.Ref, "p"); c == nil || !s.Expired(*c) {
		t.Fatal("expired")
	}
	if d, _ := s.Issue("p", "tpb", "1", &ha, "T", 60); d.Ref == a.Ref {
		t.Fatal("expired ref revived")
	}
	s.CachePut("k", map[string]int{"v": 1}, 10)
	if e, _ := s.CacheGet("k"); e == nil || string(e.Value) != `{"v":1}` {
		t.Fatal(e)
	}
	now += 11
	if e, _ := s.CacheGet("k"); e != nil {
		t.Fatal("cache expired")
	}
	s.CachePut("z", map[string]int{"v": 1}, 0)
	if e, _ := s.CacheGet("z"); e != nil {
		t.Fatal("ttl 0 is not stored")
	}
	if err := s.Ping(); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.CacheGet("__ping__"); e != nil {
		t.Fatal("ping left a row")
	}
}

// Revision must equal what earlier releases stored, so live references in
// an existing database keep matching: sha256(json.dumps([...]))[:16].
func TestRevisionCompat(t *testing.T) {
	h := strings.Repeat("a", 40)
	if got := Revision("tpb", "1001", &h, `Ubuntu "26.04" 桌面 \ x`); got != "9bdcdc9746ce8d7d" {
		t.Fatal(got)
	}
	if Revision("tpb", "1", nil, "T") == Revision("tpb", "1", &h, "T") {
		t.Fatal("hash is part of the revision")
	}
}

func TestFileDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "c.sqlite3")
	s, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	s.CachePut("k", json.RawMessage(`{"a":1}`), 60)
	s.Close()
	s, _ = Open(path, 100)
	defer s.Close()
	if e, _ := s.CacheGet("k"); e == nil {
		t.Fatal("persisted")
	}
}
