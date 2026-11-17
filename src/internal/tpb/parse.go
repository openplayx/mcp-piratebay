// Package tpb parses the source's list and detail pages.
//
// It is a Go port of the parsers in thepiratebay-api 0.1.0
// (https://github.com/9BitDaniel/thepiratebay-api, MIT, commit 2580c7f;
// see UPSTREAM.md) and keeps their behaviour: same selectors, same text
// extraction, and a value that does not parse as the expected type makes
// the whole page unreadable (ErrParse) rather than silently wrong.
package tpb

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/PuerkitoBio/goquery"
	"github.com/openplayx/mcp-piratebay/src/internal/normalize"
	"golang.org/x/net/html"
)

// ErrParse means the page has the expected structure but a value in it
// could not be read; callers report it as parse_changed.
var ErrParse = errors.New("tpb: unreadable value")

type BriefTorrent struct {
	TorrentID *int64
	Title     *string
	URL       *string
	Category  *string
	Date      *string
	Magnet    *string
	Size      *string
	Seeders   *int64
	Leechers  *int64
	Uploader  *string
}

type SearchResult struct {
	Torrents    []BriefTorrent
	CurrentPage int
	PageCount   int
}

type FullTorrent struct {
	Title          *string
	Category       *string
	Size           *string
	Date           *string
	Uploader       *string
	Seeders        *int64
	Leechers       *int64
	Magnet         *string
	Description    *string
	Images         []string
	InfoHash       *string
	NumFiles       *int64
	IsVIP          bool
	IsTrusted      bool
	AdditionalInfo map[string]string
}

var (
	torrentHref = regexp.MustCompile(`/torrent/`)
	torrentID   = regexp.MustCompile(`/torrent/(\d+)/`)
	magnetHref  = regexp.MustCompile(`^magnet:`)
	directImage = regexp.MustCompile(`(?i)https?://[^\s<>"']+\.(?:jpg|jpeg|png|gif|webp|bmp|tiff|avif|jfif)(?:\?[^\s<>"']*)?`)
	imagePath   = regexp.MustCompile(`(?i)https?://[^\s<>"']*(?:/image|/img|/photo|/pic|/upload|/media|/i)/[^\s<>"']+`)
)

func isSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

func strip(s string) string { return strings.TrimFunc(s, isSpace) }

func ptr[T any](v T) *T { return &v }

// texts returns the text nodes under the selection, in document order.
func texts(sel *goquery.Selection) []string {
	var out []string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			out = append(out, n.Data)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range sel.Nodes {
		walk(n)
	}
	return out
}

// getText is BeautifulSoup's get_text(separator, strip=True): every text
// node stripped, empty ones dropped, joined with sep.
func getText(sel *goquery.Selection, sep string) string {
	var parts []string
	for _, t := range texts(sel) {
		if t = strip(t); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, sep)
}

// rawText is get_text() without stripping.
func rawText(sel *goquery.Selection) string { return strings.Join(texts(sel), "") }

// firstWithHref is find("a", href=re) inside sel.
func firstWithHref(sel *goquery.Selection, re *regexp.Regexp) *goquery.Selection {
	return sel.Find("a[href]").FilterFunction(func(_ int, a *goquery.Selection) bool {
		h, _ := a.Attr("href")
		return re.MatchString(h)
	}).First()
}

// pyInt parses like int(str) on stripped input; ok is false otherwise.
func pyInt(s string) (*int64, bool) {
	n, err := strconv.ParseInt(strings.ReplaceAll(strip(s), "_", ""), 10, 64)
	if err != nil {
		return nil, false
	}
	return &n, true
}

func parsePagination(table *goquery.Selection, pattern string) (int, int) {
	td := table.Find("td[colspan]").First()
	if td.Length() == 0 {
		return 1, 1
	}
	current := 1
	if b := td.Find("b").First(); b.Length() > 0 {
		if n, err := strconv.Atoi(getText(b, "")); err == nil {
			current = n
		}
	}
	re := regexp.MustCompile("/" + regexp.QuoteMeta(pattern) + "/")
	count := 0
	td.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		h, _ := a.Attr("href")
		if !re.MatchString(h) {
			return
		}
		if n, err := strconv.Atoi(getText(a, "")); err == nil && n > count {
			count = n
		}
	})
	if count == 0 {
		count = current
	}
	return current, count
}

func parseRow(row *goquery.Selection, baseURL string) (*BriefTorrent, error) {
	cells := row.Find("td")
	if cells.Length() < 8 {
		return nil, nil
	}
	cell := func(i int) *goquery.Selection { return cells.Eq(i) }
	var category *string
	if a := cell(0).Find("a").First(); a.Length() > 0 {
		category = ptr(getText(a, ""))
	}
	name := firstWithHref(cell(1), torrentHref)
	if name.Length() == 0 {
		return nil, nil
	}
	href, _ := name.Attr("href")
	link := href
	if strings.HasPrefix(href, "/") {
		link = baseURL + href
	}
	t := &BriefTorrent{Title: ptr(strip(getText(name, ""))), URL: &link, Category: category}
	if m := torrentID.FindStringSubmatch(link); m != nil {
		t.TorrentID, _ = pyInt(m[1])
	}
	t.Date = ptr(getText(cell(2), ""))
	if a := firstWithHref(cell(3), magnetHref); a.Length() > 0 {
		h, _ := a.Attr("href")
		t.Magnet = &h
	}
	t.Size = ptr(getText(cell(4), ""))
	var ok bool
	if t.Seeders, ok = pyInt(getText(cell(5), "")); !ok {
		return nil, ErrParse
	}
	if t.Leechers, ok = pyInt(getText(cell(6), "")); !ok {
		return nil, ErrParse
	}
	uploader := "Anonymous"
	if a := cell(7).Find("a").First(); a.Length() > 0 {
		uploader = getText(a, "")
	}
	t.Uploader = &uploader
	return t, nil
}

// ParseList parses a search, browse, top or recent page. pattern is the
// path segment of pagination links ("search", "browse", ...). A page
// without the results table gives an empty result.
func ParseList(page, baseURL, pattern string) (SearchResult, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return SearchResult{}, ErrParse
	}
	table := doc.Find("table#searchResult").First()
	if table.Length() == 0 {
		return SearchResult{Torrents: []BriefTorrent{}, CurrentPage: 1, PageCount: 1}, nil
	}
	res := SearchResult{Torrents: []BriefTorrent{}}
	res.CurrentPage, res.PageCount = parsePagination(table, pattern)
	var rowErr error
	table.Find("tr").Each(func(_ int, tr *goquery.Selection) {
		if rowErr != nil || tr.Find("th").Length() > 0 || tr.Find("td[colspan]").Length() > 0 {
			return
		}
		t, err := parseRow(tr, baseURL)
		if err != nil {
			rowErr = err
			return
		}
		if t != nil {
			res.Torrents = append(res.Torrents, *t)
		}
	})
	if rowErr != nil {
		return SearchResult{}, rowErr
	}
	return res, nil
}

// parseDL turns a <dl> into label/value pairs.
func parseDL(dl *goquery.Selection, into map[string]string) {
	dl.Find("dt").Each(func(_ int, dt *goquery.Selection) {
		key := strings.TrimRight(getText(dt, ""), ":")
		dd := dt.NextAllFiltered("dd").First()
		if dd.Length() == 0 {
			return
		}
		value := getText(dd, " ")
		if key == "Info Hash" && value == "" {
			if next := dd.Nodes[0].NextSibling; next != nil && next.Type == html.TextNode {
				value = strip(next.Data)
			}
		}
		into[key] = value
	})
}

// onlyString is BeautifulSoup's .string: the text of an element whose
// content is a single string (possibly nested in single children).
func onlyString(n *html.Node) (string, bool) {
	for {
		if n.FirstChild == nil || n.FirstChild != n.LastChild {
			return "", false
		}
		c := n.FirstChild
		if c.Type == html.TextNode {
			return c.Data, true
		}
		if c.Type != html.ElementNode {
			return "", false
		}
		n = c
	}
}

func images(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, re := range []*regexp.Regexp{directImage, imagePath} {
		for _, u := range re.FindAllString(text, -1) {
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out
}

// ParseDetail parses a torrent page; nil when the page has no details
// frame (not a torrent page).
func ParseDetail(page string) (*FullTorrent, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return nil, ErrParse
	}
	frame := doc.Find("div#detailsframe").First()
	if frame.Length() == 0 {
		return nil, nil
	}
	t := &FullTorrent{}
	if h1 := frame.Find("h1#title").First(); h1.Length() > 0 {
		t.Title = ptr(strip(getText(h1, "")))
	}
	col1 := frame.Find("dl.col1").First()
	col2 := frame.Find("dl.col2").First()
	meta := map[string]string{}
	parseDL(col1, meta)
	parseDL(col2, meta)
	col2.Find("dt").EachWithBreak(func(_ int, dt *goquery.Selection) bool {
		s, ok := onlyString(dt.Nodes[0])
		if !ok || !strings.Contains(s, "By") {
			return true
		}
		dd := dt.NextAllFiltered("dd").First()
		t.IsVIP = dd.Find(`img[alt="VIP"]`).Length() > 0
		t.IsTrusted = dd.Find(`img[alt="Trusted"]`).Length() > 0
		return false
	})
	if a := firstWithHref(frame, magnetHref); a.Length() > 0 {
		h, _ := a.Attr("href")
		t.Magnet = &h
	}
	if pre := frame.Find("div.nfo").First().Find("pre").First(); pre.Length() > 0 {
		text := rawText(pre)
		if d := strip(text); d != "" {
			t.Description = &d
		}
		t.Images = images(text)
	}
	pop := func(k string) *string {
		v, ok := meta[k]
		if !ok {
			return nil
		}
		delete(meta, k)
		s := strip(v)
		return &s
	}
	t.Category = pop("Type")
	t.Size = pop("Size")
	t.Date = pop("Uploaded")
	t.Uploader = pop("By")
	var ok bool
	if s := pop("Seeders"); s != nil {
		if t.Seeders, ok = pyInt(*s); !ok {
			return nil, ErrParse
		}
	}
	if s := pop("Leechers"); s != nil {
		if t.Leechers, ok = pyInt(*s); !ok {
			return nil, ErrParse
		}
	}
	t.InfoHash = pop("Info Hash")
	if s := pop("Files"); s != nil {
		if t.NumFiles, ok = pyInt(*s); !ok {
			return nil, ErrParse
		}
	}
	delete(meta, "Comments")
	t.AdditionalInfo = meta
	return t, nil
}

// SearchPath is /search/<query>/<page>/<sort>/<category>; the query has
// "/" replaced by a space and is percent-encoded completely.
func SearchPath(query string, page, sort, category int) (string, error) {
	q := strip(query)
	if q == "" {
		return "", errors.New("search query cannot be empty")
	}
	q = strings.ReplaceAll(q, "/", " ")
	return "/search/" + normalize.Quote(q) + "/" + strconv.Itoa(page) + "/" + strconv.Itoa(sort) + "/" + strconv.Itoa(category), nil
}

func BrowsePath(category, page, sort int) string {
	return "/browse/" + strconv.Itoa(category) + "/" + strconv.Itoa(page) + "/" + strconv.Itoa(sort)
}

// TopPath uses "/top/all" for all categories; "/top/0" is a 404 at the source.
func TopPath(category int) string {
	if category == 0 {
		return "/top/all"
	}
	return "/top/" + strconv.Itoa(category)
}

func RecentPath(page int) string { return "/recent/" + strconv.Itoa(page) }

func DetailPath(id int64) string { return "/torrent/" + strconv.FormatInt(id, 10) }
