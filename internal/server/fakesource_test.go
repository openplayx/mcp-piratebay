package server

// Self-written HTML in the source's page structure plus a RoundTripper
// serving it. No network, no third-party page copies.

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"sync"
)

const (
	base  = "https://tpb.test"
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type item struct {
	id                                                int
	title, hash                                       string
	size, date, seeders, leechers, uploader, category string
}

func newItem(id int, title, hash string) item {
	return item{id: id, title: title, hash: hash, size: "1.50&nbsp;GiB", date: "04-01&nbsp;2019",
		seeders: "10", leechers: "2", uploader: "alice", category: "Video &gt; HD - Movies"}
}

func esc(s string) string { return html.EscapeString(s) }

func magnetFor(it item) string {
	return "magnet:?xt=urn:btih:" + it.hash + "&amp;dn=" + esc(it.title) +
		"&amp;tr=udp%3A%2F%2Ftracker.example%3A1337%2Fannounce&amp;tr=javascript%3Aalert(1)"
}

func row(it item) string {
	magnet := ""
	if it.hash != "" {
		magnet = `<a href="` + magnetFor(it) + `" title="Download this torrent using magnet"><img src="/static/img/icon-magnet.gif" alt="Magnet link"/></a>`
	}
	return fmt.Sprintf(`<tr>
<td class="vertTh"><a href="%s/browse/207" title="More from this category">%s</a></td>
<td><a href="%s/torrent/%d/slug_%d" title="Details">%s</a>
</td>
<td>%s</td>
<td><nobr>%s</nobr></td>
<td align="right">%s</td>
<td align="right">%s</td>
<td align="right">%s</td>
<td><a href="/user/%s/" title="Browse">%s</a></td>
</tr>`, base, it.category, base, it.id, it.id, esc(it.title), it.date, magnet, it.size, it.seeders, it.leechers, it.uploader, esc(it.uploader))
}

func listPage(items []item, pattern string, pages int) string {
	pagination := ""
	if pages > 1 {
		var links []string
		for n := 2; n <= pages; n++ {
			links = append(links, fmt.Sprintf(`<a href="/%s/x/%d/99/0">%d</a>`, pattern, n, n))
		}
		pagination = `<tr><td colspan="9" style="text-align:center;"><b>1</b>&nbsp;` + strings.Join(links, "&nbsp;") + `</td></tr>`
	}
	var rows []string
	for _, it := range items {
		rows = append(rows, row(it))
	}
	return `<!DOCTYPE html><html><head><title>The Pirate Bay</title></head><body>
<table id="searchResult"><thead id="tableHead"><tr class="header">
<th>Type</th><th>Name</th><th>Uploaded</th><th>&nbsp;</th><th>Size</th><th>SE</th><th>LE</th><th>ULed by</th>
</tr></thead>
` + strings.Join(rows, "\n") + "\n" + pagination + `
</table></body></html>`
}

const noHits = `<!DOCTYPE html><html><body><h2><span>Search results: zz</span></h2>
No hits. Try adding an asterisk in you search phrase.</body></html>`

var changedLayout = `<!DOCTYPE html><html><body><div class="new-layout">
<a href="magnet:?xt=urn:btih:` + hashA + `">x</a></div></body></html>`

type detailOpts struct {
	pageHash    *string
	description string
	untrusted   bool
}

func detailPage(it item, o detailOpts) string {
	badge := `<img src="/static/img/trusted.png" alt="Trusted" title="Trusted"/>`
	if o.untrusted {
		badge = ""
	}
	magnet := ""
	if it.hash != "" {
		magnet = `<a style="background-image: url('/static/img/icons/icon-magnet.gif');" href="` + magnetFor(it) + `" title="Get this torrent">Get This Torrent</a>`
	}
	desc := o.description
	if desc == "" {
		desc = "Plain description."
	}
	ph := strings.ToUpper(it.hash)
	if o.pageHash != nil {
		ph = *o.pageHash
	}
	return fmt.Sprintf(`<!DOCTYPE html><html><body>
<div id="detailsframe">
<div id="title">%s</div>
<h1 id="title">%s</h1>
<div id="details">
<dl class="col1">
<dt>Type:</dt><dd><a href="/browse/207" title="More from this category">Video &gt; HD - Movies</a></dd>
<dt>Files:</dt><dd><a href="/ajax_details_filelist.php?id=%d">3</a></dd>
<dt>Size:</dt><dd>6.07&nbsp;GiB&nbsp;(6518974464&nbsp;Bytes)</dd>
</dl>
<dl class="col2">
<dt>Uploaded:</dt><dd>2026-04-25 16:35:39 GMT</dd>
<dt>By:</dt><dd><a href="/user/%s/" title="Browse">%s</a> %s</dd>
<dt>Seeders:</dt><dd>118</dd>
<dt>Leechers:</dt><dd>87</dd>
<dt>Comments</dt><dd>0 &nbsp; </dd>
<dt>Info Hash:</dt><dd></dd>
            %s        </dl>
<div class="download">%s</div>
<div class="nfo"><pre>%s</pre></div>
</div></div></body></html>`, esc(it.title), esc(it.title), it.id, it.uploader, it.uploader, badge, ph, magnet, esc(desc))
}

var items = func() []item {
	a := newItem(1001, "Ubuntu 26.04 Desktop amd64", hashA)
	b := newItem(1002, "Debian 13 netinst <b>bold</b>‮", hashB)
	b.size, b.date = "956.66&nbsp;MiB", "Today&nbsp;01:00"
	c := newItem(1003, "No magnet item", "")
	c.seeders, c.leechers = "0", "0"
	return []item{a, b, c}
}()

type route struct {
	status  int
	body    string
	headers map[string]string
	err     error
}

// fakeSource routes source paths to canned responses and records requests.
type fakeSource struct {
	mu       sync.Mutex
	routes   map[string]route
	requests []string
}

func (f *fakeSource) set(path string, r route) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.status == 0 && r.err == nil {
		r.status = 200
	}
	f.routes[path] = r
}

func (f *fakeSource) del(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.routes, path)
}

func (f *fakeSource) reqs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.requests...)
}

func (f *fakeSource) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.EscapedPath()
	f.mu.Lock()
	f.requests = append(f.requests, path)
	r, ok := f.routes[path]
	f.mu.Unlock()
	if !ok {
		r = route{status: 404, body: "<html><title>Not Found</title></html>"}
	}
	if r.err != nil {
		return nil, r.err
	}
	h := http.Header{}
	for k, v := range r.headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: r.status, Header: h, Body: io.NopCloser(strings.NewReader(r.body)),
		ContentLength: int64(len(r.body)), Request: req}, nil
}

func standard() *fakeSource {
	f := &fakeSource{routes: map[string]route{}}
	f.set("/search/ubuntu/1/99/0", route{body: listPage(items, "search", 3)})
	f.set("/search/zz/1/99/0", route{body: noHits})
	f.set("/top/all", route{body: listPage(items[:2], "top", 1)})
	f.set("/top/200", route{body: listPage(items[:1], "top", 1)})
	f.set("/recent/1", route{body: listPage(items[:2], "recent", 1)})
	f.set("/browse/207/1/3", route{body: listPage(items[:1], "browse", 1)})
	for _, it := range items {
		f.set(fmt.Sprintf("/torrent/%d", it.id), route{body: detailPage(it, detailOpts{})})
	}
	return f
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "slow" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var errConnect = errors.New("connection refused")
