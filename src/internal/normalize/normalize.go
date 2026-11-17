// Package normalize turns parsed source data into the service's normalised,
// untrusted-safe form.
//
// Rules: a value that cannot be parsed reliably becomes nil (never 0 or
// "now"); raw source strings are kept beside parsed values; every free text
// field is plain text with control characters removed and a length cap.
package normalize

import (
	"encoding/base32"
	"encoding/hex"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var (
	units      = map[string]int{"B": 0, "KIB": 1, "MIB": 2, "GIB": 3, "TIB": 4, "PIB": 5}
	sizeRe     = regexp.MustCompile(`(?i)^\s*(\d+(?:\.\d+)?)\s*([KMGTP]?I?B)\s*$`)
	exactRe    = regexp.MustCompile(`(?i)\((\d{1,20})\s*Bytes\)`)
	listDateRe = regexp.MustCompile(`^(\d{2})-(\d{2})\s+(\d{4})$`)
	detailRe   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) GMT$`)
	hex40      = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	b32        = regexp.MustCompile(`^[A-Za-z2-7]{32}$`)
)

var trackerSchemes = []string{"udp", "http", "https", "ws", "wss"}

const (
	MaxTrackers    = 20
	DescriptionMax = 4000
)

// IsControl matches control, zero-width and bidi override characters.
func IsControl(r rune) bool {
	return (r <= 0x08) || (r >= 0x0b && r <= 0x1f) || (r >= 0x7f && r <= 0x9f) ||
		(r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// isSpace is Python's str.isspace / regex \s for Unicode text.
func isSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// Text returns plain text: NFC, no control/bidi characters, collapsed
// whitespace (unless multiline), at most max characters; nil when empty.
func Text(value *string, max int, multiline bool) *string {
	if value == nil {
		return nil
	}
	s := norm.NFC.String(*value)
	s = strings.ReplaceAll(s, " ", " ")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if !multiline {
		s = strings.NewReplacer("\n", " ", "\t", " ").Replace(s)
	}
	s = strings.Map(func(r rune) rune {
		if IsControl(r) {
			return -1
		}
		return r
	}, s)
	if !multiline {
		s = strings.Join(strings.FieldsFunc(s, isSpace), " ")
	}
	s = strings.TrimFunc(s, isSpace)
	if s == "" {
		return nil
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return &s
}

// SizeBytes is the approximate size of a display size such as "2.99 GiB".
func SizeBytes(display *string) *int64 {
	if display == nil || *display == "" {
		return nil
	}
	m := sizeRe.FindStringSubmatch(strings.ReplaceAll(*display, " ", " "))
	if m == nil {
		return nil
	}
	exp, ok := units[strings.ToUpper(m[2])]
	if !ok {
		return nil
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil
	}
	n := int64(math.RoundToEven(f * math.Pow(1024, float64(exp))))
	return &n
}

// ExactSizeBytes reads "6.07 GiB (6518974464 Bytes)".
func ExactSizeBytes(display *string) *int64 {
	if display == nil {
		return nil
	}
	m := exactRe.FindStringSubmatch(strings.ReplaceAll(*display, " ", " "))
	if m == nil {
		return nil
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// ListDate turns "MM-DD YYYY" into "YYYY-MM-DD". Forms without a year or
// with a source-local clock time ("Today 01:00", "11-11 09:55") have no
// reliable date and give nil.
func ListDate(raw *string) *string {
	if raw == nil {
		return nil
	}
	m := listDateRe.FindStringSubmatch(strings.TrimFunc(strings.ReplaceAll(*raw, " ", " "), isSpace))
	if m == nil {
		return nil
	}
	t, err := time.Parse("2006-01-02", m[3]+"-"+m[1]+"-"+m[2])
	if err != nil {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}

// DetailDatetime turns "2026-04-25 16:35:39 GMT" into "2026-04-25T16:35:39Z".
func DetailDatetime(raw *string) *string {
	if raw == nil {
		return nil
	}
	m := detailRe.FindStringSubmatch(strings.TrimFunc(strings.ReplaceAll(*raw, " ", " "), isSpace))
	if m == nil {
		return nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", m[1])
	if err != nil {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05Z")
	return &s
}

// InfoHash is the lowercase hex v1 info hash from 40 hex or 32 base32
// characters.
func InfoHash(value string) *string {
	v := strings.TrimFunc(value, isSpace)
	if hex40.MatchString(v) {
		s := strings.ToLower(v)
		return &s
	}
	if b32.MatchString(v) {
		b, err := base32.StdEncoding.DecodeString(strings.ToUpper(v))
		if err != nil {
			return nil
		}
		s := hex.EncodeToString(b)
		return &s
	}
	return nil
}

// Magnet is a validated magnet link rebuilt from its parts.
type Magnet struct {
	InfoHash    string   `json:"info_hash"`
	DisplayName *string  `json:"display_name"`
	Trackers    []string `json:"trackers"`
	URI         string   `json:"uri"`
}

// unquotePlus decodes like Python's unquote_plus: '+' is a space, invalid
// escapes stay as they are, invalid UTF-8 becomes U+FFFD.
func unquotePlus(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b = append(b, byte(v))
				i += 2
				continue
			}
		}
		b = append(b, s[i])
	}
	return strings.ToValidUTF8(string(b), "�")
}

// parseQS mirrors Python's parse_qs(strict_parsing=False): '&'-separated
// pairs, pairs without '=' and empty values are skipped.
func parseQS(q string) map[string][]string {
	out := map[string][]string{}
	for _, pair := range strings.Split(q, "&") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || v == "" {
			continue
		}
		key := unquotePlus(k)
		out[key] = append(out[key], unquotePlus(v))
	}
	return out
}

// Quote percent-encodes everything except unreserved characters
// (Python's quote(s, safe="")).
func Quote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

// ParseMagnet validates a magnet URI and rebuilds a canonical one. Only
// xt=urn:btih: (v1), dn and tracker URLs with known schemes survive;
// anything else in the source string is dropped.
func ParseMagnet(uri *string) *Magnet {
	if uri == nil || len(*uri) > 8192 || !strings.HasPrefix(*uri, "magnet:?") {
		return nil
	}
	params := parseQS((*uri)[len("magnet:?"):])
	hashes := map[string]bool{}
	for _, xt := range params["xt"] {
		if len(xt) >= 9 && strings.EqualFold(xt[:9], "urn:btih:") {
			if h := InfoHash(xt[9:]); h != nil {
				hashes[*h] = true
			}
		}
	}
	if len(hashes) != 1 {
		return nil
	}
	var ih string
	for h := range hashes {
		ih = h
	}
	var name *string
	if dn := params["dn"]; len(dn) > 0 {
		name = Text(&dn[0], 300, false)
	}
	trackers := []string{}
	for _, tr := range params["tr"] {
		tr = strings.TrimFunc(tr, isSpace)
		u, err := url.Parse(tr)
		if err != nil {
			continue
		}
		if slices.Contains(trackerSchemes, u.Scheme) && u.Hostname() != "" && len([]rune(tr)) <= 300 &&
			strings.IndexFunc(tr, IsControl) < 0 && !slices.Contains(trackers, tr) {
			trackers = append(trackers, tr)
		}
		if len(trackers) >= MaxTrackers {
			break
		}
	}
	canonical := "magnet:?xt=urn:btih:" + ih
	if name != nil {
		canonical += "&dn=" + Quote(*name)
	}
	for _, tr := range trackers {
		canonical += "&tr=" + Quote(tr)
	}
	return &Magnet{InfoHash: ih, DisplayName: name, Trackers: trackers, URI: canonical}
}
