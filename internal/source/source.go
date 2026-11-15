// Package source fetches pages from the configured index and turns them
// into normalised items.
//
// Every request goes through the outbound policy (netguard), global pacing
// (min_interval_ms), status handling and a response size cap, so a failure
// is reported as such instead of an empty result list. Redirects are never
// followed.
package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/openplayx/mcp-piratebay/internal/apierr"
	"github.com/openplayx/mcp-piratebay/internal/config"
	"github.com/openplayx/mcp-piratebay/internal/netguard"
	"github.com/openplayx/mcp-piratebay/internal/normalize"
	"github.com/openplayx/mcp-piratebay/internal/tpb"
)

// Item is one normalised list entry (the cached form).
type Item struct {
	SourceItemID string  `json:"source_item_id"`
	Title        string  `json:"title"`
	SizeText     *string `json:"size_text"`
	SizeBytes    *int64  `json:"size_bytes"`
	Seeders      *int64  `json:"seeders"`
	Leechers     *int64  `json:"leechers"`
	UploadedText *string `json:"uploaded_text"`
	UploadedDate *string `json:"uploaded_date"`
	Category     *string `json:"category"`
	Uploader     *string `json:"uploader"`
	InfoHash     *string `json:"info_hash"`
	Magnet       *string `json:"magnet"`
}

// Page is one source page of items (the cached form).
type Page struct {
	Items      []Item `json:"items"`
	SourcePage int    `json:"source_page"`
	PageCount  int    `json:"page_count"`
}

// Detail is a normalised torrent page (the cached form).
type Detail struct {
	Title                *string           `json:"title"`
	SizeText             *string           `json:"size_text"`
	SizeBytes            *int64            `json:"size_bytes"`
	SizeExact            bool              `json:"size_exact"`
	FileCount            *int64            `json:"file_count"`
	UploadedText         *string           `json:"uploaded_text"`
	UploadedAt           *string           `json:"uploaded_at"`
	Uploader             *string           `json:"uploader"`
	Category             *string           `json:"category"`
	Seeders              *int64            `json:"seeders"`
	Leechers             *int64            `json:"leechers"`
	PageInfoHash         *string           `json:"page_info_hash"`
	InfoHash             *string           `json:"info_hash"`
	Magnet               *normalize.Magnet `json:"magnet"`
	SourceTrusted        bool              `json:"source_trusted"`
	SourceVIP            bool              `json:"source_vip"`
	Description          *string           `json:"description"`
	DescriptionTruncated bool              `json:"description_truncated"`
	ImageCount           int               `json:"image_count"`
}

type Source struct {
	cfg     config.Source
	client  *http.Client
	resolve netguard.Resolver
	mu      sync.Mutex
	next    time.Time
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
}

// New builds the fetcher. transport may be nil (default transport with the
// optional proxy); tests inject one.
func New(cfg config.Source, proxy string, transport http.RoundTripper, resolve netguard.Resolver) (*Source, error) {
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil // never from the environment
		if proxy != "" {
			u, err := url.Parse(proxy)
			if err != nil {
				return nil, fmt.Errorf("proxy: %w", err)
			}
			t.Proxy = http.ProxyURL(u)
		}
		transport = t
	}
	return &Source{
		cfg: cfg, resolve: resolve, now: time.Now, sleep: sleepCtx,
		client: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(cfg.TimeoutSeconds * float64(time.Second)),
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Source) throttle(ctx context.Context) error {
	interval := time.Duration(s.cfg.MinIntervalMS) * time.Millisecond
	if interval <= 0 {
		return nil
	}
	s.mu.Lock()
	wait := s.next.Sub(s.now())
	if wait < 0 {
		wait = 0
	}
	s.next = s.now().Add(wait + interval)
	s.mu.Unlock()
	if wait > 0 {
		return s.sleep(ctx, wait)
	}
	return nil
}

// fetch returns the status and body of one GET; 404 is returned to the
// caller, other failures are service errors.
func (s *Source) fetch(ctx context.Context, path string) (int, string, error) {
	u, err := url.Parse(s.cfg.BaseURL + path)
	if err != nil {
		return 0, "", apierr.SourceBlocked()
	}
	if aerr := netguard.CheckURL(ctx, u, s.cfg, s.resolve); aerr != nil {
		return 0, "", aerr
	}
	if err := s.throttle(ctx); err != nil {
		return 0, "", apierr.UpstreamTimeout()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, "", apierr.SourceBlocked()
	}
	req.Header.Set("User-Agent", s.cfg.UserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, "", transportError(err)
	}
	defer resp.Body.Close()
	switch st := resp.StatusCode; {
	case st == 301 || st == 302 || st == 303 || st == 307 || st == 308:
		return 0, "", apierr.UpstreamUnavailable("The source answered with a redirect, which is not followed.")
	case st == http.StatusTooManyRequests:
		return 0, "", apierr.UpstreamRateLimited()
	case st >= 400 && st != http.StatusNotFound:
		return 0, "", apierr.UpstreamUnavailable(fmt.Sprintf("The source answered HTTP %d.", st))
	}
	if resp.ContentLength > s.cfg.MaxResponseBytes {
		return 0, "", apierr.UpstreamUnavailable("The source response is too large.")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, s.cfg.MaxResponseBytes+1))
	if err != nil {
		return 0, "", transportError(err)
	}
	if int64(len(body)) > s.cfg.MaxResponseBytes {
		return 0, "", apierr.UpstreamUnavailable("The source response is too large.")
	}
	return resp.StatusCode, strings.ToValidUTF8(string(body), "�"), nil
}

func transportError(err error) *apierr.Error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout()) {
		return apierr.UpstreamTimeout()
	}
	return apierr.UpstreamUnavailable("")
}

func nonNegative(n *int64) *int64 {
	if n == nil || *n < 0 {
		return nil
	}
	return n
}

// Brief normalises a list entry; nil when it has no usable id or title.
func Brief(t tpb.BriefTorrent) *Item {
	if t.TorrentID == nil || *t.TorrentID <= 0 {
		return nil
	}
	title := normalize.Text(t.Title, 300, false)
	if title == nil {
		return nil
	}
	m := normalize.ParseMagnet(t.Magnet)
	sizeText := normalize.Text(t.Size, 40, false)
	uploaded := normalize.Text(t.Date, 40, false)
	it := &Item{
		SourceItemID: fmt.Sprint(*t.TorrentID), Title: *title,
		SizeText: sizeText, SizeBytes: normalize.SizeBytes(sizeText),
		Seeders: nonNegative(t.Seeders), Leechers: nonNegative(t.Leechers),
		UploadedText: uploaded, UploadedDate: normalize.ListDate(uploaded),
		Category: normalize.Text(t.Category, 80, false), Uploader: normalize.Text(t.Uploader, 64, false),
	}
	if m != nil {
		it.InfoHash, it.Magnet = &m.InfoHash, &m.URI
	}
	return it
}

// Full normalises a torrent page.
func Full(t tpb.FullTorrent) Detail {
	m := normalize.ParseMagnet(t.Magnet)
	sizeText := normalize.Text(t.Size, 80, false)
	exact := normalize.ExactSizeBytes(t.Size)
	size := exact
	if size == nil || *size == 0 {
		size = normalize.SizeBytes(sizeText)
	}
	var pageHash *string
	if t.InfoHash != nil {
		pageHash = normalize.InfoHash(*t.InfoHash)
	}
	desc := normalize.Text(t.Description, normalize.DescriptionMax+1, true)
	truncated := false
	if desc != nil {
		if r := []rune(*desc); len(r) > normalize.DescriptionMax {
			truncated = true
			s := string(r[:normalize.DescriptionMax])
			desc = &s
		}
	}
	d := Detail{
		Title: normalize.Text(t.Title, 300, false), SizeText: sizeText, SizeBytes: size,
		SizeExact: exact != nil, FileCount: nonNegative(t.NumFiles),
		UploadedText: normalize.Text(t.Date, 40, false), UploadedAt: normalize.DetailDatetime(t.Date),
		Uploader: normalize.Text(t.Uploader, 64, false), Category: normalize.Text(t.Category, 80, false),
		Seeders: nonNegative(t.Seeders), Leechers: nonNegative(t.Leechers),
		PageInfoHash: pageHash, Magnet: m, SourceTrusted: t.IsTrusted, SourceVIP: t.IsVIP,
		Description: desc, DescriptionTruncated: truncated, ImageCount: len(t.Images),
	}
	if m != nil {
		d.InfoHash = &m.InfoHash
	}
	return d
}

func (s *Source) page(ctx context.Context, path, pattern string, page int) (Page, error) {
	status, body, err := s.fetch(ctx, path)
	if err != nil {
		return Page{}, err
	}
	if status == http.StatusNotFound {
		return Page{}, apierr.UpstreamUnavailable("The source rejected this listing (HTTP 404).")
	}
	res, perr := tpb.ParseList(body, s.cfg.BaseURL, pattern)
	if perr != nil {
		return Page{}, apierr.ParseChanged()
	}
	items := []Item{}
	for _, t := range res.Torrents {
		if it := Brief(t); it != nil {
			items = append(items, *it)
		}
	}
	if len(items) == 0 {
		hasTable := strings.Contains(body, `id="searchResult"`)
		hasMagnets := strings.Contains(body, "magnet:?")
		noHits := strings.Contains(body, "No hits")
		if hasMagnets || !(hasTable || noHits) {
			return Page{}, apierr.ParseChanged()
		}
		return Page{Items: items, SourcePage: page, PageCount: page}, nil
	}
	return Page{Items: items, SourcePage: page, PageCount: max(res.PageCount, page)}, nil
}

func (s *Source) Search(ctx context.Context, query string, category, sort, page int) (Page, error) {
	path, err := tpb.SearchPath(query, page, sort, category)
	if err != nil {
		return Page{}, apierr.InvalidRequest("query must contain searchable characters")
	}
	return s.page(ctx, path, "search", page)
}

func (s *Source) Browse(ctx context.Context, category, sort, page int) (Page, error) {
	return s.page(ctx, tpb.BrowsePath(category, page, sort), "browse", page)
}

func (s *Source) Top(ctx context.Context, category int) (Page, error) {
	p, err := s.page(ctx, tpb.TopPath(category), "top", 1)
	p.PageCount = 1
	return p, err
}

func (s *Source) Recent(ctx context.Context, page int) (Page, error) {
	return s.page(ctx, tpb.RecentPath(page), "recent", page)
}

func (s *Source) Detail(ctx context.Context, itemID int64) (Detail, error) {
	status, body, err := s.fetch(ctx, tpb.DetailPath(itemID))
	if err != nil {
		return Detail{}, err
	}
	if status == http.StatusNotFound {
		return Detail{}, apierr.SourceItemGone()
	}
	t, perr := tpb.ParseDetail(body)
	if perr != nil || t == nil {
		return Detail{}, apierr.ParseChanged()
	}
	return Full(*t), nil
}
