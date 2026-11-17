// Package categories maps the service category and sort vocabulary onto
// the source's numeric codes. Unknown keys are rejected, never silently
// widened to an all-categories query.
package categories

import (
	"slices"
	"strings"

	"github.com/openplayx/mcp-piratebay/src/internal/apierr"
)

type Category struct {
	Key   string
	Code  int
	Label string
	Adult bool
}

// Parent is the key before the dot ("video" for "video.tv"), or "".
func (c Category) Parent() string {
	if p, _, ok := strings.Cut(c.Key, "."); ok {
		return p
	}
	return ""
}

var All = []Category{
	{"all", 0, "All", false},
	{"audio", 100, "Audio", false},
	{"audio.music", 101, "Music", false},
	{"audio.audiobooks", 102, "Audio books", false},
	{"audio.flac", 104, "FLAC", false},
	{"audio.other", 105, "Other audio", false},
	{"video", 200, "Video", false},
	{"video.movies", 201, "Movies", false},
	{"video.dvdr", 202, "Movies DVDR", false},
	{"video.clips", 204, "Music videos / clips", false},
	{"video.tv", 205, "TV shows", false},
	{"video.hd_movies", 207, "HD movies", false},
	{"video.hd_tv", 208, "HD TV shows", false},
	{"video.3d", 209, "3D", false},
	{"video.uhd_movies", 211, "UHD/4K movies", false},
	{"video.uhd_tv", 212, "UHD/4K TV shows", false},
	{"video.other", 299, "Other video", false},
	{"apps", 300, "Applications", false},
	{"apps.windows", 301, "Windows", false},
	{"apps.mac", 302, "Mac", false},
	{"apps.unix", 303, "UNIX", false},
	{"apps.ios", 305, "iOS", false},
	{"apps.android", 306, "Android", false},
	{"games", 400, "Games", false},
	{"games.pc", 401, "PC", false},
	{"games.mac", 402, "Mac", false},
	{"games.psx", 403, "PSx", false},
	{"games.xbox360", 404, "XBOX360", false},
	{"games.wii", 405, "Wii", false},
	{"games.handheld", 406, "Handheld", false},
	{"games.ios", 407, "iOS", false},
	{"games.android", 408, "Android", false},
	{"games.other", 499, "Other games", false},
	{"adult", 500, "Adult", true},
	{"adult.movies", 501, "Adult movies", true},
	{"adult.dvdr", 502, "Adult DVDR", true},
	{"adult.pictures", 503, "Adult pictures", true},
	{"adult.games", 504, "Adult games", true},
	{"adult.hd", 505, "Adult HD", true},
	{"adult.clips", 506, "Adult clips", true},
	{"adult.uhd", 507, "Adult UHD", true},
	{"adult.other", 599, "Adult other", true},
	{"other", 600, "Other", false},
	{"other.ebooks", 601, "E-books", false},
	{"other.comics", 602, "Comics", false},
	{"other.pictures", 603, "Pictures", false},
	{"other.covers", 604, "Covers", false},
	{"other.other", 699, "Other", false},
}

// Sort keys and their source codes, in display order.
var Sorts = []struct {
	Key  string
	Code int
}{
	{"relevance", 99}, {"seeders_desc", 7}, {"seeders_asc", 8}, {"size_desc", 5},
	{"size_asc", 6}, {"date_desc", 3}, {"date_asc", 4},
}

func sortKeys(skip string) []string {
	var out []string
	for _, s := range Sorts {
		if s.Key != skip {
			out = append(out, s.Key)
		}
	}
	return out
}

var (
	SearchSorts = sortKeys("")
	// The source answers 404 for browse with the relevance order.
	BrowseSorts = sortKeys("relevance")
)

func Available(allowAdult bool) []Category {
	var out []Category
	for _, c := range All {
		if allowAdult || !c.Adult {
			out = append(out, c)
		}
	}
	return out
}

func Lookup(key string, allowAdult bool) (Category, *apierr.Error) {
	i := slices.IndexFunc(All, func(c Category) bool { return c.Key == key })
	if i < 0 || (All[i].Adult && !allowAdult) {
		return Category{}, apierr.CategoryUnsupported(key)
	}
	return All[i], nil
}

func SortCode(sort, operation string, allowed []string) (int, *apierr.Error) {
	if !slices.Contains(allowed, sort) {
		return 0, apierr.SortUnsupported(sort, operation)
	}
	for _, s := range Sorts {
		if s.Key == sort {
			return s.Code, nil
		}
	}
	return 0, apierr.SortUnsupported(sort, operation)
}
