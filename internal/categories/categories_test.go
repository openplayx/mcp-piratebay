package categories

import "testing"

// Codes of thepiratebay-api's Category / SortBy enums.
var upstreamCodes = []int{0, 100, 101, 102, 104, 105, 200, 201, 202, 204, 205, 207, 208, 209, 211, 212, 299,
	300, 301, 302, 303, 305, 306, 400, 401, 402, 403, 404, 405, 406, 407, 408, 499,
	500, 501, 502, 503, 504, 505, 506, 507, 599, 600, 601, 602, 603, 604, 699}

func TestCodesMatchUpstream(t *testing.T) {
	codes, keys := map[int]bool{}, map[string]bool{}
	for _, c := range All {
		codes[c.Code], keys[c.Key] = true, true
	}
	if len(codes) != len(upstreamCodes) || len(keys) != len(All) {
		t.Fatal(len(codes), len(keys))
	}
	for _, c := range upstreamCodes {
		if !codes[c] {
			t.Error(c)
		}
	}
	sorts := map[int]bool{}
	for _, s := range Sorts {
		sorts[s.Code] = true
	}
	for _, c := range []int{99, 8, 7, 6, 5, 4, 3} {
		if !sorts[c] {
			t.Error("sort", c)
		}
	}
}

func TestLookup(t *testing.T) {
	if c, err := Lookup("video.hd_movies", false); err != nil || c.Code != 207 {
		t.Fatal(c, err)
	}
	if _, err := Lookup("adult", false); err == nil {
		t.Fatal("adult hidden")
	}
	if c, _ := Lookup("adult", true); c.Code != 500 {
		t.Fatal(c)
	}
	if _, err := Lookup("VIDEO", false); err == nil {
		t.Fatal("keys are case-sensitive")
	}
	if _, err := SortCode("relevance", "browse", BrowseSorts); err == nil || err.Code != "sort_unsupported" {
		t.Fatal(err)
	}
}
