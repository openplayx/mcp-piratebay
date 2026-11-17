// Package apierr is the service error vocabulary.
//
// Every failure has a stable machine code. "No results" is a successful
// empty page, never one of these, and these are never turned into an empty
// page.
package apierr

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/openplayx/mcp-piratebay/src/internal/ojson"
)

type Error struct {
	Code      string
	Status    int
	Message   string
	Retryable bool
	Headers   map[string]string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Body is {"error": {code, message, retryable}[, "request_id"]}.
func (e *Error) Body(requestID string) *ojson.Object {
	o := ojson.New("error", ojson.New("code", e.Code, "message", e.Message, "retryable", e.Retryable))
	if requestID != "" {
		o.Set("request_id", requestID)
	}
	return o
}

func New(code string, status int, message string) *Error {
	return &Error{Code: code, Status: status, Message: message}
}

func retry(e *Error) *Error { e.Retryable = true; return e }

func InvalidRequest(msg string) *Error { return New("invalid_request", http.StatusBadRequest, msg) }

// Validation is a schema violation (HTTP 422, as the API has always used).
func Validation(msg string) *Error {
	return New("invalid_request", http.StatusUnprocessableEntity, msg)
}

func CategoryUnsupported(key string) *Error {
	return New("category_unsupported", http.StatusBadRequest, fmt.Sprintf("Category %s is not supported.", pyRepr(key)))
}

func SortUnsupported(sort, operation string) *Error {
	return New("sort_unsupported", http.StatusBadRequest, fmt.Sprintf("Sort %s is not supported for %s.", pyRepr(sort), operation))
}

func Unauthorized() *Error {
	e := New("unauthorized", http.StatusUnauthorized, "A valid bearer token is required.")
	e.Headers = map[string]string{"WWW-Authenticate": "Bearer"}
	return e
}

func Forbidden(scope string) *Error {
	return New("forbidden", http.StatusForbidden, fmt.Sprintf("The token lacks the %s scope.", pyRepr(scope)))
}

func NotFound() *Error { return New("not_found", http.StatusNotFound, "Unknown result reference.") }

func ResultExpired() *Error {
	return New("result_expired", http.StatusGone, "The result reference has expired. Run the search again explicitly.")
}

func CandidateChanged() *Error {
	return New("candidate_changed", http.StatusConflict, "The source item changed since it was listed; it was not resolved.")
}

func SourceItemGone() *Error {
	return New("source_item_gone", http.StatusNotFound, "The source no longer has this item.")
}

func NoMagnet() *Error {
	return New("no_valid_magnet", http.StatusUnprocessableEntity, "The source item has no valid magnet link.")
}

func RateLimited(retryAfter int) *Error {
	e := retry(New("rate_limited", http.StatusTooManyRequests, "Too many requests for this token."))
	e.Headers = map[string]string{"Retry-After": strconv.Itoa(retryAfter)}
	return e
}

func UpstreamUnavailable(msg string) *Error {
	if msg == "" {
		msg = "The source is unreachable."
	}
	return retry(New("upstream_unavailable", http.StatusBadGateway, msg))
}

func UpstreamRateLimited() *Error {
	e := retry(New("upstream_rate_limited", http.StatusServiceUnavailable, "The source is rate limiting requests."))
	e.Headers = map[string]string{"Retry-After": "60"}
	return e
}

func UpstreamTimeout() *Error {
	return retry(New("upstream_timeout", http.StatusGatewayTimeout, "The source did not answer in time."))
}

func ParseChanged() *Error {
	return New("parse_changed", http.StatusBadGateway,
		"The source page structure is not recognised; the parser may need an update.")
}

func SourceBlocked() *Error {
	return New("source_blocked", http.StatusBadGateway,
		"The source address is not permitted by the service configuration.")
}

func Internal() *Error {
	return retry(New("internal_error", http.StatusInternalServerError, "Unexpected server error."))
}

// pyRepr quotes like the earlier implementation's messages ('value').
func pyRepr(s string) string { return "'" + s + "'" }
