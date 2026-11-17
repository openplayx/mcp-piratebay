// Package store is the SQLite store: a short-TTL source cache and result
// references.
//
// A result reference is an opaque random id bound to the calling token,
// the source, the source item, its info hash and title revision, with an
// expiry. References survive restarts until they expire; an unknown or
// expired id is reported as such and never re-pointed at a different item.
// The schema is unchanged from earlier releases, so existing databases
// (and their live references) keep working.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS cache (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    fetched_at REAL NOT NULL,
    expires_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS cache_expires ON cache(expires_at);
CREATE TABLE IF NOT EXISTS candidates (
    ref TEXT PRIMARY KEY,
    principal TEXT NOT NULL,
    source_id TEXT NOT NULL,
    item_id TEXT NOT NULL,
    info_hash TEXT,
    title TEXT NOT NULL,
    revision TEXT NOT NULL,
    created_at REAL NOT NULL,
    expires_at REAL NOT NULL,
    UNIQUE (principal, source_id, item_id, revision)
);
CREATE INDEX IF NOT EXISTS candidates_expires ON candidates(expires_at);
`

type Candidate struct {
	Ref, Principal, SourceID, ItemID string
	InfoHash                         *string
	Title, Revision                  string
	ExpiresAt                        float64
}

type CacheEntry struct {
	Value     json.RawMessage
	FetchedAt float64
	ExpiresAt float64
}

// pyJSONString encodes like Python's json.dumps(ensure_ascii=False), so
// revisions match the ones earlier releases stored.
func pyJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Revision identifies one listed version of an item.
func Revision(sourceID, itemID string, infoHash *string, title string) string {
	h := "null"
	if infoHash != nil {
		h = pyJSONString(*infoHash)
	}
	raw := "[" + pyJSONString(sourceID) + ", " + pyJSONString(itemID) + ", " + h + ", " + pyJSONString(title) + "]"
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:16]
}

type Store struct {
	db     *sql.DB
	mu     sync.Mutex
	max    int
	writes int
	Now    func() float64
}

func Open(path string, maxEntries int) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		dsn = "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, max: maxEntries, Now: func() float64 { return float64(time.Now().UnixNano()) / 1e9 }}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Exec runs a statement directly (tests).
func (s *Store) Exec(query string, args ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(query, args...)
	return err
}

// Ping checks the database is readable and writable.
func (s *Store) Ping() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO cache(key, value, fetched_at, expires_at) VALUES ('__ping__', '{}', 0, 0)")
	return err
}

func (s *Store) maintain() {
	s.writes++
	if s.writes%100 != 0 {
		return
	}
	now := s.Now()
	_, _ = s.db.Exec("DELETE FROM cache WHERE expires_at < ?", now)
	_, _ = s.db.Exec("DELETE FROM candidates WHERE expires_at < ?", now-86400)
	for _, t := range [][2]string{{"cache", "fetched_at"}, {"candidates", "created_at"}} {
		var count int
		if s.db.QueryRow("SELECT COUNT(*) FROM "+t[0]).Scan(&count) == nil && count > s.max {
			_, _ = s.db.Exec("DELETE FROM "+t[0]+" WHERE rowid IN (SELECT rowid FROM "+t[0]+
				" ORDER BY "+t[1]+" LIMIT ?)", count-s.max)
		}
	}
}

func (s *Store) CacheGet(key string) (*CacheEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var e CacheEntry
	var value string
	err := s.db.QueryRow("SELECT value, fetched_at, expires_at FROM cache WHERE key=?", key).
		Scan(&value, &e.FetchedAt, &e.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && e.ExpiresAt <= s.Now()) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.Value = json.RawMessage(value)
	return &e, nil
}

func (s *Store) CachePut(key string, value any, ttl int) (CacheEntry, error) {
	now := s.Now()
	data, err := json.Marshal(value)
	if err != nil {
		return CacheEntry{}, err
	}
	e := CacheEntry{Value: data, FetchedAt: now, ExpiresAt: now + float64(ttl)}
	if ttl <= 0 {
		return e, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec("INSERT OR REPLACE INTO cache(key, value, fetched_at, expires_at) VALUES (?, ?, ?, ?)",
		key, string(data), e.FetchedAt, e.ExpiresAt); err != nil {
		return e, err
	}
	s.maintain()
	return e, nil
}

func newRef() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return "tsr_" + base64.RawURLEncoding.EncodeToString(b)
}

// Issue returns the live reference for this exact item revision or mints
// one; either way its expiry is extended to now + ttl.
func (s *Store) Issue(principal, sourceID, itemID string, infoHash *string, title string, ttl int) (Candidate, error) {
	rev := Revision(sourceID, itemID, infoHash, title)
	now := s.Now()
	c := Candidate{Principal: principal, SourceID: sourceID, ItemID: itemID, InfoHash: infoHash,
		Title: title, Revision: rev, ExpiresAt: now + float64(ttl)}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ref string
	var exp float64
	err := s.db.QueryRow("SELECT ref, expires_at FROM candidates WHERE principal=? AND source_id=? AND item_id=? AND revision=?",
		principal, sourceID, itemID, rev).Scan(&ref, &exp)
	switch {
	case err == nil && exp > now:
		c.Ref = ref
		_, err = s.db.Exec("UPDATE candidates SET expires_at=? WHERE ref=?", c.ExpiresAt, ref)
		return c, err
	case err == nil:
		if _, err := s.db.Exec("DELETE FROM candidates WHERE ref=?", ref); err != nil {
			return c, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return c, err
	}
	c.Ref = newRef()
	if _, err := s.db.Exec(`INSERT INTO candidates(ref, principal, source_id, item_id, info_hash, title, revision,
		created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Ref, principal, sourceID, itemID, infoHash, title, rev, now, c.ExpiresAt); err != nil {
		return c, err
	}
	s.maintain()
	return c, nil
}

// Lookup returns the candidate even when expired (callers distinguish
// expired from unknown); nil for unknown refs or refs of another token.
func (s *Store) Lookup(ref, principal string) (*Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Candidate
	var hash sql.NullString
	err := s.db.QueryRow(`SELECT ref, principal, source_id, item_id, info_hash, title, revision, expires_at
		FROM candidates WHERE ref=?`, ref).Scan(&c.Ref, &c.Principal, &c.SourceID, &c.ItemID, &hash, &c.Title, &c.Revision, &c.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c.Principal != principal {
		return nil, nil
	}
	if hash.Valid {
		c.InfoHash = &hash.String
	}
	return &c, nil
}

func (s *Store) Expired(c Candidate) bool { return c.ExpiresAt <= s.Now() }
