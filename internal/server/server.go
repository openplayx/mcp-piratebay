// Package server serves the /v1 HTTP API, the MCP endpoint and the ops
// endpoints. Only these operations are exposed; mirror discovery, source
// switching, proxies and HTTP client options are not.
//
//	GET  /healthz, /readyz                          no token
//	GET  /v1/capabilities, /v1/categories
//	POST /v1/search
//	GET  /v1/browse, /v1/top, /v1/recent
//	GET  /v1/search-results/{result_ref}
//	POST /v1/search-results/{result_ref}/resolve    "resolve" scope
//	POST /mcp                                       MCP (Streamable HTTP, JSON only)
//	GET  /openapi.json, /docs                       when server.docs_public
package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/openplayx/mcp-piratebay/api"
	"github.com/openplayx/mcp-piratebay/internal/apierr"
	"github.com/openplayx/mcp-piratebay/internal/auth"
	"github.com/openplayx/mcp-piratebay/internal/categories"
	"github.com/openplayx/mcp-piratebay/internal/ojson"
	"github.com/openplayx/mcp-piratebay/internal/service"
)

// Upstream is the project the source parser was ported from (UPSTREAM.md).
var Upstream = ojson.New(
	"package", "thepiratebay-api",
	"version", "0.1.0",
	"repository", "https://github.com/9BitDaniel/thepiratebay-api",
	"commit", "2580c7f339aed26fb790d694cd3a19aa08f9c826",
)

const maxBody = 64 << 10

var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type Server struct {
	svc     *service.Service
	auth    *auth.Authenticator
	log     *slog.Logger
	version string
	mux     *http.ServeMux
}

func New(svc *service.Service, a *auth.Authenticator, log *slog.Logger, version string) *Server {
	s := &Server{svc: svc, auth: a, log: log, version: version, mux: http.NewServeMux()}
	m := s.mux
	m.HandleFunc("GET /healthz", s.healthz)
	m.HandleFunc("GET /readyz", s.readyz)
	m.HandleFunc("GET /v1/capabilities", s.authed(s.capabilities))
	m.HandleFunc("GET /v1/categories", s.authed(s.categories))
	m.HandleFunc("POST /v1/search", s.authed(s.search))
	m.HandleFunc("GET /v1/browse", s.authed(s.browse))
	m.HandleFunc("GET /v1/top", s.authed(s.top))
	m.HandleFunc("GET /v1/recent", s.authed(s.recent))
	m.HandleFunc("GET /v1/search-results/{ref}", s.authed(s.detail))
	m.HandleFunc("POST /v1/search-results/{ref}/resolve", s.authed(s.resolve))
	m.HandleFunc("POST /mcp", s.mcp)
	m.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	if svc.Config.Server.DocsPublic {
		m.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(api.OpenAPI)
		})
		m.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, docsPage)
		})
	}
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, r, apierr.New("not_found", http.StatusNotFound, "No such route."))
	})
	return s
}

const docsPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>OpenPlayX Torrent Search API</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css"></head>
<body><div id="swagger-ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url: "/openapi.json", dom_id: "#swagger-ui"});</script>
</body></html>
`

// ---- request context -----------------------------------------------------------

type reqState struct {
	id, principal, tool, errorCode string
}

type stateWriter struct {
	http.ResponseWriter
	status int
	st     *reqState
}

func (w *stateWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

func st(w http.ResponseWriter) *reqState {
	if sw, ok := w.(*stateWriter); ok {
		return sw.st
	}
	return &reqState{}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-ID")
	if !requestIDRe.MatchString(rid) {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		rid = hex.EncodeToString(b)
	}
	sw := &stateWriter{ResponseWriter: w, status: http.StatusOK, st: &reqState{id: rid, principal: "-", tool: "-", errorCode: "-"}}
	w.Header().Set("X-Request-ID", rid)
	started := time.Now()
	s.mux.ServeHTTP(sw, r)
	// Paths carry no search terms (search is a POST body); never log bodies.
	s.log.Info("request", "id", rid, "method", r.Method, "path", r.URL.Path, "status", sw.status,
		"ms", time.Since(started).Milliseconds(), "principal", sw.st.principal, "tool", sw.st.tool,
		"error", sw.st.errorCode)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, e *apierr.Error) {
	state := st(w)
	state.errorCode = e.Code
	for k, v := range e.Headers {
		w.Header().Set(k, v)
	}
	writeJSON(w, e.Status, e.Body(state.id))
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request, v any, e *apierr.Error) {
	if e != nil {
		s.fail(w, r, e)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type authedFunc func(w http.ResponseWriter, r *http.Request, who auth.Principal)

func (s *Server) authed(h authedFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		who, e := s.auth.Authenticate(r.Header.Get("Authorization"))
		if e != nil {
			s.fail(w, r, e)
			return
		}
		st(w).principal = who.Name
		h(w, r, who)
	}
}

// ---- ops and meta ------------------------------------------------------------------

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ojson.New("status", "ok"))
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if !s.auth.Configured() {
		s.fail(w, r, apierr.New("not_ready", http.StatusServiceUnavailable, "No API tokens are configured."))
		return
	}
	if err := s.svc.Store.Ping(); err != nil {
		s.fail(w, r, apierr.New("not_ready", http.StatusServiceUnavailable, "The cache store is not writable."))
		return
	}
	writeJSON(w, http.StatusOK, ojson.New("status", "ready"))
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	cfg := s.svc.Config
	search := who.Has("search")
	writeJSON(w, http.StatusOK, ojson.New(
		"service", ojson.New("name", "openplayx-torrent-search", "version", s.version, "api", "v1"),
		"upstream", Upstream,
		"sources", []any{ojson.New("source_id", cfg.Source.ID, "name", cfg.Source.Name)},
		"operations", ojson.New("search", search, "browse", search, "top", search, "recent", search,
			"detail", search, "resolve", who.Has("resolve")),
		"sorts", ojson.New("search", categories.SearchSorts, "browse", categories.BrowseSorts,
			"top", []string{}, "recent", []string{}),
		"limits", ojson.New(
			"query_max_length", cfg.Limits.QueryMaxLength,
			"max_page", cfg.Limits.MaxPage,
			"max_results", cfg.Limits.MaxResults,
			"page_size", "source-defined (about 30; top lists 100)",
			"requests_per_minute", cfg.Limits.RequestsPerMinute,
			"result_ttl_seconds", cfg.Cache.ResultTTLSeconds,
		),
		"adult_categories", cfg.AllowAdultCategories,
	))
}

func categoryList(allowAdult, withCode bool) *ojson.Object {
	list := []any{}
	for _, c := range categories.Available(allowAdult) {
		var parent any
		if p := c.Parent(); p != "" {
			parent = p
		}
		o := ojson.New("key", c.Key, "label", c.Label, "parent", parent)
		if withCode {
			o.Set("source_code", c.Code)
		}
		list = append(list, o)
	}
	return ojson.New("categories", list)
}

func (s *Server) categories(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	writeJSON(w, http.StatusOK, categoryList(s.svc.Config.AllowAdultCategories, true))
}

// ---- search ------------------------------------------------------------------------

func (s *Server) search(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		s.fail(w, r, apierr.Validation("body: request body too large"))
		return
	}
	req, msg := parseSearch("body", body)
	if msg != "" {
		s.fail(w, r, apierr.Validation(msg))
		return
	}
	out, e := s.svc.Search(r.Context(), who, req.Query, req.Category, req.Sort, req.Page)
	s.reply(w, r, out, e)
}

// query reads the URL query like the earlier API: strings capped at 40
// characters, page >= 1; violations are 422.
type query struct {
	r    *http.Request
	errs []string
}

func (q *query) str(name string, required bool, def string) string {
	vals, ok := q.r.URL.Query()[name]
	if !ok || len(vals) == 0 {
		if required {
			q.errs = append(q.errs, "query."+name+": Field required")
		}
		return def
	}
	if len([]rune(vals[0])) > 40 && name == "category" {
		q.errs = append(q.errs, "query."+name+": String should have at most 40 characters")
	}
	return vals[0]
}

func (q *query) page() int {
	v := q.r.URL.Query().Get("page")
	if v == "" {
		return 1
	}
	n, ok := parseInt(json.RawMessage(`"` + strings.ReplaceAll(v, `"`, "") + `"`))
	if !ok {
		q.errs = append(q.errs, "query.page: Input should be a valid integer")
		return 1
	}
	if n < 1 {
		q.errs = append(q.errs, "query.page: Input should be greater than or equal to 1")
	}
	return n
}

func (q *query) failed() *apierr.Error {
	if len(q.errs) == 0 {
		return nil
	}
	return apierr.Validation(strings.Join(q.errs, "; "))
}

func (s *Server) browse(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	q := &query{r: r}
	cat := q.str("category", true, "")
	sort := q.str("sort", false, "date_desc")
	page := q.page()
	if e := q.failed(); e != nil {
		s.fail(w, r, e)
		return
	}
	out, e := s.svc.Browse(r.Context(), who, cat, sort, page)
	s.reply(w, r, out, e)
}

func (s *Server) top(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	q := &query{r: r}
	cat := q.str("category", false, "all")
	if e := q.failed(); e != nil {
		s.fail(w, r, e)
		return
	}
	out, e := s.svc.Top(r.Context(), who, cat)
	s.reply(w, r, out, e)
}

func (s *Server) recent(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	q := &query{r: r}
	page := q.page()
	if e := q.failed(); e != nil {
		s.fail(w, r, e)
		return
	}
	out, e := s.svc.Recent(r.Context(), who, page)
	s.reply(w, r, out, e)
}

func (s *Server) detail(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	out, e := s.svc.Detail(r.Context(), who, r.PathValue("ref"))
	s.reply(w, r, out, e)
}

func (s *Server) resolve(w http.ResponseWriter, r *http.Request, who auth.Principal) {
	out, e := s.svc.Resolve(r.Context(), who, r.PathValue("ref"))
	s.reply(w, r, out, e)
}

// ---- MCP -----------------------------------------------------------------------------
//
// A thin protocol layer over the same service, authentication and rate
// limit as the /v1 API: no second business state, no loopback HTTP.
// Supported subset: JSON-RPC 2.0 single messages over POST with JSON
// responses (no SSE, no server-initiated messages), stateless (no
// Mcp-Session-Id), initialize, ping, tools/list, tools/call and
// notifications. Business failures are tool results with isError=true and
// the same error codes as the HTTP API.

var ProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

const (
	parseError     = -32700
	invalidRequest = -32600
	methodNotFound = -32601
	invalidParams  = -32602
	instructions   = "Read-only search over one administrator-configured torrent index. Results " +
		"are unverified external index data. Searching never downloads anything; " +
		"resolve_candidate returns a magnet that the host hands to its own download " +
		"planning, where the user confirms."
)

type tool struct {
	name, title, description, scope string
	schema                          *ojson.Object
	run                             func(s *Server, r *http.Request, who auth.Principal, args json.RawMessage) (any, *apierr.Error)
}

func (t tool) describe() *ojson.Object {
	return ojson.New("name", t.name, "title", t.title, "description", t.description, "inputSchema", t.schema,
		"annotations", ojson.New("title", t.title, "readOnlyHint", true, "destructiveHint", false,
			"idempotentHint", true, "openWorldHint", true))
}

var refSchema = ojson.New(
	"type", "object",
	"properties", ojson.New("result_ref", ojson.New("type", "string", "maxLength", 64,
		"description", "result_ref from a search result.")),
	"required", []string{"result_ref"},
	"additionalProperties", false,
)

func refArg(args json.RawMessage) (string, *apierr.Error) {
	f, ok := newFields("", args)
	if !ok {
		return "", apierr.InvalidRequest("arguments: Input should be a valid dictionary or object")
	}
	ref, _ := f.str("result_ref", true, "", 1, 64)
	if msg := f.done(); msg != "" {
		return "", apierr.InvalidRequest(msg)
	}
	return ref, nil
}

var tools = []tool{
	{
		name: "search", title: "Search torrents", scope: "search",
		description: "One source page of results for keywords. Read-only; never downloads. " +
			"Each item has a result_ref for get_details / resolve_candidate. " +
			"Titles, sizes and peer counts are declared by the source and unverified.",
		schema: ojson.New(
			"type", "object",
			"properties", ojson.New(
				"query", ojson.New("type", "string", "minLength", 1, "maxLength", 500, "description", "Search keywords."),
				"category", ojson.New("type", "string", "maxLength", 40, "default", "all", "description", "Key from list_categories."),
				"sort", ojson.New("type", "string", "enum", categories.SearchSorts, "default", "relevance"),
				"page", ojson.New("type", "integer", "minimum", 1, "default", 1, "description", "1-based source page."),
			),
			"required", []string{"query"},
			"additionalProperties", false,
		),
		run: func(s *Server, r *http.Request, who auth.Principal, args json.RawMessage) (any, *apierr.Error) {
			req, msg := parseSearch("", args)
			if msg != "" {
				return nil, apierr.InvalidRequest(msg)
			}
			return s.svc.Search(r.Context(), who, req.Query, req.Category, req.Sort, req.Page)
		},
	},
	{
		name: "get_details", title: "Torrent details", scope: "search",
		description: "Normalised detail for a result_ref issued to this token: exact size, " +
			"file count, uploader, description and whether the item changed.",
		schema: refSchema,
		run: func(s *Server, r *http.Request, who auth.Principal, args json.RawMessage) (any, *apierr.Error) {
			ref, e := refArg(args)
			if e != nil {
				return nil, e
			}
			return s.svc.Detail(r.Context(), who, ref)
		},
	},
	{
		name: "resolve_candidate", title: "Resolve candidate", scope: "resolve",
		description: "Re-reads the source page and returns a validated magnet for a chosen " +
			"candidate. Does not start a download. Fails with candidate_changed " +
			"if the item no longer matches what was listed.",
		schema: refSchema,
		run: func(s *Server, r *http.Request, who auth.Principal, args json.RawMessage) (any, *apierr.Error) {
			ref, e := refArg(args)
			if e != nil {
				return nil, e
			}
			return s.svc.Resolve(r.Context(), who, ref)
		},
	},
	{
		name: "list_categories", title: "List categories", scope: "search",
		description: "Category keys accepted by search.",
		schema:      ojson.New("type", "object", "properties", &ojson.Object{}, "additionalProperties", false),
		run: func(s *Server, r *http.Request, who auth.Principal, args json.RawMessage) (any, *apierr.Error) {
			f, ok := newFields("", args)
			if !ok {
				return nil, apierr.InvalidRequest("arguments: Input should be a valid dictionary or object")
			}
			if msg := f.done(); msg != "" {
				return nil, apierr.InvalidRequest(msg)
			}
			return categoryList(s.svc.Config.AllowAdultCategories, false), nil
		},
	},
}

func rpcError(id any, code int, msg string) *ojson.Object {
	return ojson.New("jsonrpc", "2.0", "id", id, "error", ojson.New("code", code, "message", msg))
}

func result(id any, res any) *ojson.Object {
	return ojson.New("jsonrpc", "2.0", "id", id, "result", res)
}

func toolResult(payload any, isError bool) *ojson.Object {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
	return ojson.New(
		"content", []any{ojson.New("type", "text", "text", strings.TrimRight(buf.String(), "\n"))},
		"structuredContent", payload,
		"isError", isError,
	)
}

func originAllowed(r *http.Request) bool {
	// DNS-rebinding guard: a browser page may only call us from our own origin.
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if _, rest, ok := strings.Cut(origin, "://"); ok {
		origin = rest
	}
	return strings.TrimRight(origin, "/") == r.Host
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	if !originAllowed(r) {
		s.fail(w, r, apierr.New("forbidden", http.StatusForbidden, "Origin not allowed."))
		return
	}
	accept := r.Header.Get("Accept")
	if accept == "" {
		accept = "*/*"
	}
	if !strings.Contains(accept, "application/json") && !strings.Contains(accept, "*/*") {
		w.WriteHeader(http.StatusNotAcceptable)
		return
	}
	if v := r.Header.Get("Mcp-Protocol-Version"); v != "" && !slices.Contains(ProtocolVersions, v) {
		writeJSON(w, http.StatusBadRequest, rpcError(nil, invalidRequest, "Unsupported protocol version "+v+"."))
		return
	}
	// Same token, scopes and request budget as the HTTP API.
	who, e := s.auth.Authenticate(r.Header.Get("Authorization"))
	if e != nil {
		s.fail(w, r, e)
		return
	}
	st(w).principal = who.Name
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, rpcError(nil, invalidRequest, "Message too large."))
		return
	}
	var any_ any
	if json.Unmarshal(body, &any_) != nil {
		writeJSON(w, http.StatusBadRequest, rpcError(nil, parseError, "Parse error."))
		return
	}
	var msg map[string]json.RawMessage
	if _, isObj := any_.(map[string]any); !isObj || json.Unmarshal(body, &msg) != nil || string(msg["jsonrpc"]) != `"2.0"` {
		writeJSON(w, http.StatusBadRequest, rpcError(nil, invalidRequest, "Expected one JSON-RPC 2.0 message."))
		return
	}
	idRaw, hasID := msg["id"]
	if !hasID {
		// Notifications and client responses: nothing to answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var id any = idRaw
	if string(bytes.TrimSpace(idRaw)) == "null" {
		id = nil
	}
	var method string
	if json.Unmarshal(msg["method"], &method) != nil {
		writeJSON(w, http.StatusOK, rpcError(id, invalidRequest, "method is required."))
		return
	}
	switch method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg["params"], &p)
		version := ProtocolVersions[0]
		if slices.Contains(ProtocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		writeJSON(w, http.StatusOK, result(id, ojson.New(
			"protocolVersion", version,
			"capabilities", ojson.New("tools", ojson.New("listChanged", false)),
			"serverInfo", ojson.New("name", "openplayx-torrent-search", "title", "OpenPlayX Torrent Search", "version", s.version),
			"instructions", instructions,
		)))
	case "ping":
		writeJSON(w, http.StatusOK, result(id, &ojson.Object{}))
	case "tools/list":
		list := []any{}
		for _, t := range tools {
			if who.Has(t.scope) {
				list = append(list, t.describe())
			}
		}
		writeJSON(w, http.StatusOK, result(id, ojson.New("tools", list)))
	case "tools/call":
		var p map[string]json.RawMessage
		var name string
		if json.Unmarshal(msg["params"], &p) != nil || p == nil || json.Unmarshal(p["name"], &name) != nil {
			writeJSON(w, http.StatusOK, rpcError(id, invalidParams, "params.name is required."))
			return
		}
		i := slices.IndexFunc(tools, func(t tool) bool { return t.name == name })
		if i < 0 || !who.Has(tools[i].scope) {
			writeJSON(w, http.StatusOK, rpcError(id, invalidParams, "Unknown tool: "+name))
			return
		}
		args := bytes.TrimSpace(p["arguments"])
		if len(args) > 0 && string(args) != "null" && args[0] != '{' {
			writeJSON(w, http.StatusOK, rpcError(id, invalidParams, "params.arguments must be an object."))
			return
		}
		st(w).tool = name
		out, e := tools[i].run(s, r, who, args)
		if e != nil {
			st(w).errorCode = e.Code
			writeJSON(w, http.StatusOK, result(id, toolResult(e.Body(""), true)))
			return
		}
		writeJSON(w, http.StatusOK, result(id, toolResult(out, false)))
	default:
		writeJSON(w, http.StatusOK, rpcError(id, methodNotFound, "Method not found: "+method))
	}
}
