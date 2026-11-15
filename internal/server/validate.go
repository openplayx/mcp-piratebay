package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/openplayx/mcp-piratebay/internal/categories"
)

// fields validates a JSON object strictly, like the earlier pydantic
// models: unknown fields, wrong types and out-of-range values are
// collected as "loc: message" without echoing inputs.
type fields struct {
	prefix string
	raw    map[string]json.RawMessage
	used   map[string]bool
	errs   []string
}

func newFields(prefix string, raw json.RawMessage) (*fields, bool) {
	f := &fields{prefix: prefix, raw: map[string]json.RawMessage{}, used: map[string]bool{}}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return f, true
	}
	if t[0] != '{' || json.Unmarshal(t, &f.raw) != nil {
		return nil, false
	}
	return f, true
}

func (f *fields) loc(name string) string {
	if f.prefix == "" {
		return name
	}
	return f.prefix + "." + name
}

func (f *fields) fail(name, msg string) { f.errs = append(f.errs, f.loc(name)+": "+msg) }

// str reads a string field, stripped of surrounding whitespace.
func (f *fields) str(name string, required bool, def string, minLen, maxLen int) (string, bool) {
	f.used[name] = true
	v, ok := f.raw[name]
	if !ok {
		if required {
			f.fail(name, "Field required")
			return "", false
		}
		return def, true
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		f.fail(name, "Input should be a valid string")
		return "", false
	}
	s = strings.TrimFunc(s, unicode.IsSpace)
	return s, f.length(name, s, minLen, maxLen)
}

func (f *fields) length(name, s string, minLen, maxLen int) bool {
	n := utf8.RuneCountInString(s)
	if n < minLen {
		f.fail(name, fmt.Sprintf("String should have at least %d character", minLen))
		return false
	}
	if maxLen > 0 && n > maxLen {
		f.fail(name, fmt.Sprintf("String should have at most %d characters", maxLen))
		return false
	}
	return true
}

// integer accepts JSON integers and integral numeric strings.
func (f *fields) integer(name string, def, min int) int {
	f.used[name] = true
	v, ok := f.raw[name]
	if !ok {
		return def
	}
	n, ok := parseInt(v)
	if !ok {
		f.fail(name, "Input should be a valid integer")
		return def
	}
	if n < min {
		f.fail(name, fmt.Sprintf("Input should be greater than or equal to %d", min))
	}
	return n
}

func parseInt(v json.RawMessage) (int, bool) {
	var num float64
	if json.Unmarshal(v, &num) == nil {
		if num != math.Trunc(num) || math.Abs(num) > 1e15 {
			return 0, false
		}
		return int(num), true
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		return n, err == nil
	}
	return 0, false
}

func (f *fields) done() string {
	var extra []string
	for k := range f.raw {
		if !f.used[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		f.fail(k, "Extra inputs are not permitted")
	}
	if len(f.errs) == 0 {
		return ""
	}
	if len(f.errs) > 5 {
		f.errs = f.errs[:5]
	}
	msg := strings.Join(f.errs, "; ")
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return msg
}

type searchRequest struct {
	Query, Category, Sort string
	Page                  int
}

// parseSearch validates a search request (HTTP body or MCP arguments).
func parseSearch(prefix string, raw json.RawMessage) (searchRequest, string) {
	f, ok := newFields(prefix, raw)
	if !ok {
		return searchRequest{}, prefixed(prefix, "Input should be a valid dictionary or object")
	}
	r := searchRequest{}
	if q, ok := f.str("query", true, "", 1, 500); ok {
		switch {
		case strings.IndexFunc(q, func(c rune) bool { return c < 32 || c == 127 }) >= 0:
			f.fail("query", "Value error, query must not contain control characters")
		case strings.TrimFunc(strings.NewReplacer(".", "", "/", "").Replace(q), unicode.IsSpace) == "":
			f.fail("query", "Value error, query must contain searchable characters")
		}
		r.Query = q
	}
	r.Category, _ = f.str("category", false, "all", 0, 40)
	if s, ok := f.str("sort", false, "relevance", 0, 0); ok {
		if !slices.Contains(categories.SearchSorts, s) {
			f.fail("sort", "Input should be "+quoteList(categories.SearchSorts))
		}
		r.Sort = s
	}
	r.Page = f.integer("page", 1, 1)
	return r, f.done()
}

func prefixed(prefix, msg string) string {
	if prefix == "" {
		return msg
	}
	return prefix + ": " + msg
}

func quoteList(values []string) string {
	q := make([]string, len(values))
	for i, v := range values {
		q[i] = "'" + v + "'"
	}
	return strings.Join(q[:len(q)-1], ", ") + " or " + q[len(q)-1]
}
