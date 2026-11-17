package tpb

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The golden files were produced by the original Python parsers
// (thepiratebay-api 0.1.0) from the same upstream HTML fixtures.

func golden(t *testing.T, name string) any {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func asJSON(t *testing.T, v any) any {
	t.Helper()
	data, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}

func val[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestParseListMatchesUpstream(t *testing.T) {
	page, _ := os.ReadFile("testdata/search_results.html")
	r, err := ParseList(string(page), "https://tpb.test", "search")
	if err != nil {
		t.Fatal(err)
	}
	torrents := []any{}
	for _, b := range r.Torrents {
		torrents = append(torrents, map[string]any{
			"title": val(b.Title), "category": val(b.Category), "size": val(b.Size),
			"seeders": val(b.Seeders), "leechers": val(b.Leechers), "magnet_link": val(b.Magnet),
			"uploader": val(b.Uploader), "date": val(b.Date), "torrent_id": val(b.TorrentID), "url": val(b.URL),
		})
	}
	got := asJSON(t, map[string]any{"torrents": torrents, "current_page": r.CurrentPage, "page_count": r.PageCount})
	want := golden(t, "search_results")
	if !reflect.DeepEqual(got, want) {
		gl, wl := got.(map[string]any)["torrents"].([]any), want.(map[string]any)["torrents"].([]any)
		for i := range min(len(gl), len(wl)) {
			if !reflect.DeepEqual(gl[i], wl[i]) {
				t.Fatalf("torrent %d:\n got %v\nwant %v", i, gl[i], wl[i])
			}
		}
		t.Fatalf("got %d torrents (page %v/%v), want %d", len(gl), got.(map[string]any)["current_page"],
			got.(map[string]any)["page_count"], len(wl))
	}
}

func TestParseDetailMatchesUpstream(t *testing.T) {
	for _, name := range []string{"torrent_page_vip", "torrent_page_regular"} {
		page, _ := os.ReadFile("testdata/" + name + ".html")
		d, err := ParseDetail(string(page))
		if err != nil || d == nil {
			t.Fatal(name, err)
		}
		var imgs any
		if len(d.Images) > 0 {
			imgs = d.Images
		}
		got := asJSON(t, map[string]any{
			"title": val(d.Title), "category": val(d.Category), "size": val(d.Size),
			"seeders": val(d.Seeders), "leechers": val(d.Leechers), "magnet_link": val(d.Magnet),
			"uploader": val(d.Uploader), "date": val(d.Date), "description": val(d.Description),
			"images": imgs, "info_hash": val(d.InfoHash), "num_files": val(d.NumFiles),
			"is_vip": d.IsVIP, "is_trusted": d.IsTrusted, "additional_info": d.AdditionalInfo,
		}).(map[string]any)
		want := golden(t, name).(map[string]any)
		for k, w := range want {
			if !reflect.DeepEqual(got[k], w) {
				t.Errorf("%s.%s:\n got %#v\nwant %#v", name, k, got[k], w)
			}
		}
	}
}

func TestParseEdgeCases(t *testing.T) {
	r, err := ParseList("<html><body>No hits.</body></html>", "https://x", "search")
	if err != nil || len(r.Torrents) != 0 || r.PageCount != 1 {
		t.Fatal(r, err)
	}
	bad := `<table id="searchResult"><tr><td><a>c</a></td><td><a href="/torrent/1/x">t</a></td><td>d</td><td></td><td>1 B</td><td>many</td><td>1</td><td>u</td></tr></table>`
	if _, err := ParseList(bad, "https://x", "search"); err != ErrParse {
		t.Fatal("non-numeric seeders must be a parse error", err)
	}
	if d, err := ParseDetail("<html><title>Not Found</title></html>"); d != nil || err != nil {
		t.Fatal(d, err)
	}
	p, _ := SearchPath(" ac/dc + 1 ", 2, 7, 207)
	if p != "/search/ac%20dc%20%2B%201/2/7/207" {
		t.Fatal(p)
	}
	if TopPath(0) != "/top/all" || TopPath(200) != "/top/200" || DetailPath(5) != "/torrent/5" {
		t.Fatal("paths")
	}
}
