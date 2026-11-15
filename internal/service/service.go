// Package service is the search service: cache, in-flight de-duplication,
// bounded concurrency towards the source, result references and the
// detail/resolve consistency checks.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/cases"

	"github.com/openplayx/mcp-piratebay/internal/apierr"
	"github.com/openplayx/mcp-piratebay/internal/auth"
	"github.com/openplayx/mcp-piratebay/internal/categories"
	"github.com/openplayx/mcp-piratebay/internal/config"
	"github.com/openplayx/mcp-piratebay/internal/ojson"
	"github.com/openplayx/mcp-piratebay/internal/source"
	"github.com/openplayx/mcp-piratebay/internal/store"
)

// ParserRevision is part of every cache key. Bump it whenever parsing or
// normalisation output changes, so pages cached by an older revision are
// never served. 2: the Go parser (cache entries of the Python release are
// ignored, references stay valid).
const ParserRevision = "2"

// Fetcher is what the service needs from the source (tests replace it).
type Fetcher interface {
	Search(ctx context.Context, query string, category, sort, page int) (source.Page, error)
	Browse(ctx context.Context, category, sort, page int) (source.Page, error)
	Top(ctx context.Context, category int) (source.Page, error)
	Recent(ctx context.Context, page int) (source.Page, error)
	Detail(ctx context.Context, itemID int64) (source.Detail, error)
}

type flight struct {
	done  chan struct{}
	entry store.CacheEntry
	err   error
}

type Service struct {
	Config config.Config
	Store  *store.Store
	src    Fetcher
	log    *slog.Logger
	slots  chan struct{}
	mu     sync.Mutex
	flying map[string]*flight
}

func New(cfg config.Config, src Fetcher, st *store.Store, log *slog.Logger) *Service {
	return &Service{Config: cfg, Store: st, src: src, log: log,
		slots: make(chan struct{}, cfg.Source.MaxConcurrency), flying: map[string]*flight{}}
}

func (s *Service) sourceID() string { return s.Config.Source.ID }

func (s *Service) key(parts ...any) string {
	raw, _ := json.Marshal(append([]any{ParserRevision, s.sourceID()}, parts...))
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func Iso(ts float64) string {
	return time.Unix(int64(ts), 0).UTC().Format("2006-01-02T15:04:05Z")
}

func cacheInfo(e store.CacheEntry, hit bool) *ojson.Object {
	return ojson.New("hit", hit, "fetched_at", Iso(e.FetchedAt), "expires_at", Iso(e.ExpiresAt))
}

func asAPI(err error) *apierr.Error {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e
	}
	return apierr.Internal()
}

// limited runs fn within the source concurrency limit.
func (s *Service) limited(ctx context.Context, fn func(context.Context) (any, error)) (any, error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, apierr.UpstreamTimeout()
	}
	started := time.Now()
	v, err := fn(ctx)
	if err != nil {
		s.log.Warn("source fetch failed", "source", s.sourceID(), "error", asAPI(err).Code)
	} else {
		s.log.Info("source fetch ok", "source", s.sourceID(), "ms", time.Since(started).Milliseconds())
	}
	return v, err
}

// cached serves key from the cache or fetches it once, sharing the fetch
// with identical concurrent requests. Failures are never cached.
func (s *Service) cached(ctx context.Context, key string, ttl int, fetch func(context.Context) (any, error)) (json.RawMessage, *ojson.Object, error) {
	if e, err := s.Store.CacheGet(key); err != nil {
		return nil, nil, err
	} else if e != nil {
		return e.Value, cacheInfo(*e, true), nil
	}
	s.mu.Lock()
	if f, ok := s.flying[key]; ok {
		s.mu.Unlock()
		select {
		case <-f.done:
		case <-ctx.Done():
			return nil, nil, apierr.UpstreamTimeout()
		}
		if f.err != nil {
			return nil, nil, f.err
		}
		return f.entry.Value, cacheInfo(f.entry, false), nil
	}
	f := &flight{done: make(chan struct{})}
	s.flying[key] = f
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.flying, key)
		s.mu.Unlock()
		close(f.done)
	}()
	// The fetch is shared: it must not end because the first caller left.
	v, err := s.limited(context.WithoutCancel(ctx), fetch)
	if err != nil {
		f.err = err
		return nil, nil, err
	}
	f.entry, f.err = s.Store.CachePut(key, v, ttl)
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.entry.Value, cacheInfo(f.entry, false), nil
}

func (s *Service) pageResponse(who auth.Principal, operation string, raw json.RawMessage, cache *ojson.Object) (*ojson.Object, error) {
	var page source.Page
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, err
	}
	limit := s.Config.Limits.MaxResults
	out := []any{}
	for i, it := range page.Items {
		if i >= limit {
			break
		}
		c, err := s.Store.Issue(who.Name, s.sourceID(), it.SourceItemID, it.InfoHash, it.Title, s.Config.Cache.ResultTTLSeconds)
		if err != nil {
			return nil, err
		}
		out = append(out, ojson.New(
			"result_ref", c.Ref,
			"source_id", s.sourceID(),
			"source_item_id", it.SourceItemID,
			"title", it.Title,
			"size_bytes", it.SizeBytes,
			"size_text", it.SizeText,
			"seeders", it.Seeders,
			"leechers", it.Leechers,
			"uploaded_at", nil,
			"uploaded_date", it.UploadedDate,
			"uploaded_text", it.UploadedText,
			"category", it.Category,
			"uploader", it.Uploader,
			"info_hash", it.InfoHash,
			"has_magnet", it.Magnet != nil,
			"expires_at", Iso(c.ExpiresAt),
		))
	}
	return ojson.New(
		"operation", operation,
		"source_id", s.sourceID(),
		"source_page", page.SourcePage,
		"page_count", page.PageCount,
		"has_next_page", page.SourcePage < page.PageCount,
		"result_count", len(out),
		"truncated", len(page.Items) > limit,
		"items", out,
		"cache", cache,
		"notice", Notice,
	), nil
}

// Notice accompanies every page and detail.
const Notice = "External index data. Titles, sizes and peer counts are declared by the " +
	"source and unverified; this is not a playable resource or download."

func (s *Service) pageArg(page int) *apierr.Error {
	if page > s.Config.Limits.MaxPage {
		return apierr.InvalidRequest(fmt.Sprintf("page must be <= %d.", s.Config.Limits.MaxPage))
	}
	return nil
}

var fold = cases.Fold()

func (s *Service) list(ctx context.Context, who auth.Principal, operation, key string, fetch func(context.Context) (any, error)) (*ojson.Object, *apierr.Error) {
	raw, cache, err := s.cached(ctx, key, s.Config.Cache.SearchTTLSeconds, fetch)
	if err != nil {
		return nil, asAPI(err)
	}
	out, err := s.pageResponse(who, operation, raw, cache)
	if err != nil {
		s.log.Error("page response failed", "err", err)
		return nil, apierr.Internal()
	}
	return out, nil
}

func (s *Service) Search(ctx context.Context, who auth.Principal, query, category, sort string, page int) (*ojson.Object, *apierr.Error) {
	if e := who.Require("search"); e != nil {
		return nil, e
	}
	if max := s.Config.Limits.QueryMaxLength; utf8.RuneCountInString(query) > max {
		return nil, apierr.InvalidRequest(fmt.Sprintf("query must be at most %d characters.", max))
	}
	cat, e := categories.Lookup(category, s.Config.AllowAdultCategories)
	if e != nil {
		return nil, e
	}
	code, e := categories.SortCode(sort, "search", categories.SearchSorts)
	if e != nil {
		return nil, e
	}
	if e := s.pageArg(page); e != nil {
		return nil, e
	}
	norm := fold.String(strings.Join(strings.Fields(query), " "))
	return s.list(ctx, who, "search", s.key("search", norm, cat.Code, code, page), func(ctx context.Context) (any, error) {
		return s.src.Search(ctx, norm, cat.Code, code, page)
	})
}

func (s *Service) Browse(ctx context.Context, who auth.Principal, category, sort string, page int) (*ojson.Object, *apierr.Error) {
	if e := who.Require("search"); e != nil {
		return nil, e
	}
	cat, e := categories.Lookup(category, s.Config.AllowAdultCategories)
	if e != nil {
		return nil, e
	}
	if cat.Code == 0 {
		return nil, apierr.InvalidRequest("browse requires a specific category.")
	}
	code, e := categories.SortCode(sort, "browse", categories.BrowseSorts)
	if e != nil {
		return nil, e
	}
	if e := s.pageArg(page); e != nil {
		return nil, e
	}
	return s.list(ctx, who, "browse", s.key("browse", cat.Code, code, page), func(ctx context.Context) (any, error) {
		return s.src.Browse(ctx, cat.Code, code, page)
	})
}

func (s *Service) Top(ctx context.Context, who auth.Principal, category string) (*ojson.Object, *apierr.Error) {
	if e := who.Require("search"); e != nil {
		return nil, e
	}
	cat, e := categories.Lookup(category, s.Config.AllowAdultCategories)
	if e != nil {
		return nil, e
	}
	return s.list(ctx, who, "top", s.key("top", cat.Code), func(ctx context.Context) (any, error) {
		return s.src.Top(ctx, cat.Code)
	})
}

func (s *Service) Recent(ctx context.Context, who auth.Principal, page int) (*ojson.Object, *apierr.Error) {
	if e := who.Require("search"); e != nil {
		return nil, e
	}
	if e := s.pageArg(page); e != nil {
		return nil, e
	}
	return s.list(ctx, who, "recent", s.key("recent", page), func(ctx context.Context) (any, error) {
		return s.src.Recent(ctx, page)
	})
}

// ---- candidates ----------------------------------------------------------------

func (s *Service) candidate(who auth.Principal, ref string) (*store.Candidate, *apierr.Error) {
	if !strings.HasPrefix(ref, "tsr_") || len(ref) > 64 {
		return nil, apierr.NotFound()
	}
	c, err := s.Store.Lookup(ref, who.Name)
	if err != nil {
		return nil, apierr.Internal()
	}
	if c == nil || c.SourceID != s.sourceID() {
		return nil, apierr.NotFound()
	}
	if s.Store.Expired(*c) {
		return nil, apierr.ResultExpired()
	}
	return c, nil
}

func (s *Service) detail(ctx context.Context, c *store.Candidate, fresh bool) (source.Detail, *ojson.Object, *apierr.Error) {
	id, err := strconv.ParseInt(c.ItemID, 10, 64)
	if err != nil {
		return source.Detail{}, nil, apierr.NotFound()
	}
	key := s.key("detail", c.ItemID)
	fetch := func(ctx context.Context) (any, error) { return s.src.Detail(ctx, id) }
	var raw json.RawMessage
	var cache *ojson.Object
	if fresh {
		v, err := s.limited(ctx, fetch)
		if err != nil {
			return source.Detail{}, nil, asAPI(err)
		}
		e, err := s.Store.CachePut(key, v, s.Config.Cache.DetailTTLSeconds)
		if err != nil {
			return source.Detail{}, nil, apierr.Internal()
		}
		raw, cache = e.Value, cacheInfo(e, false)
	} else {
		raw, cache, err = s.cached(ctx, key, s.Config.Cache.DetailTTLSeconds, fetch)
		if err != nil {
			return source.Detail{}, nil, asAPI(err)
		}
	}
	var d source.Detail
	if err := json.Unmarshal(raw, &d); err != nil {
		return source.Detail{}, nil, apierr.Internal()
	}
	return d, cache, nil
}

func changed(c *store.Candidate, d source.Detail) bool {
	hashes := map[string]bool{}
	for _, h := range []*string{d.InfoHash, d.PageInfoHash} {
		if h != nil && *h != "" {
			hashes[*h] = true
		}
	}
	if len(hashes) > 1 {
		return true
	}
	return c.InfoHash != nil && len(hashes) > 0 && !hashes[*c.InfoHash]
}

func (s *Service) Detail(ctx context.Context, who auth.Principal, ref string) (*ojson.Object, *apierr.Error) {
	if e := who.Require("search"); e != nil {
		return nil, e
	}
	c, e := s.candidate(who, ref)
	if e != nil {
		return nil, e
	}
	d, cache, e := s.detail(ctx, c, false)
	if e != nil {
		return nil, e
	}
	hash := d.InfoHash
	if hash == nil {
		hash = d.PageInfoHash
	}
	return ojson.New(
		"result_ref", c.Ref,
		"source_id", c.SourceID,
		"source_item_id", c.ItemID,
		"title", d.Title,
		"size_bytes", d.SizeBytes,
		"size_exact", d.SizeExact,
		"size_text", d.SizeText,
		"file_count", d.FileCount,
		"uploaded_at", d.UploadedAt,
		"uploaded_text", d.UploadedText,
		"uploader", d.Uploader,
		"category", d.Category,
		"seeders", d.Seeders,
		"leechers", d.Leechers,
		"info_hash", hash,
		"has_magnet", d.Magnet != nil,
		"changed", changed(c, d),
		"source_trusted", d.SourceTrusted,
		"source_vip", d.SourceVIP,
		"description", d.Description,
		"description_truncated", d.DescriptionTruncated,
		"image_count", d.ImageCount,
		"cache", cache,
		"notice", Notice,
	), nil
}

func (s *Service) Resolve(ctx context.Context, who auth.Principal, ref string) (*ojson.Object, *apierr.Error) {
	if e := who.Require("resolve"); e != nil {
		return nil, e
	}
	c, e := s.candidate(who, ref)
	if e != nil {
		return nil, e
	}
	// Always re-read the page: a download input must match the source now.
	d, _, e := s.detail(ctx, c, true)
	if e != nil {
		return nil, e
	}
	if changed(c, d) {
		return nil, apierr.CandidateChanged()
	}
	m := d.Magnet
	if m == nil {
		return nil, apierr.NoMagnet()
	}
	if c.InfoHash == nil && d.PageInfoHash != nil && *d.PageInfoHash != m.InfoHash {
		return nil, apierr.CandidateChanged()
	}
	s.log.Info("resolved", "source", c.SourceID, "item", c.ItemID, "principal", who.Name)
	return ojson.New(
		"result_ref", c.Ref,
		"source_id", c.SourceID,
		"source_item_id", c.ItemID,
		"title", c.Title,
		"info_hash", m.InfoHash,
		"magnet", m.URI,
		"display_name", m.DisplayName,
		"tracker_count", len(m.Trackers),
		"resolved_at", Iso(s.Store.Now()),
	), nil
}
