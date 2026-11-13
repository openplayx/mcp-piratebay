"""Service error vocabulary.

Every failure has a stable machine code. "No results" is a successful empty
page, never one of these, and these are never turned into an empty page.
"""

from __future__ import annotations


class ApiError(Exception):
    def __init__(
        self,
        code: str,
        status: int,
        message: str,
        *,
        retryable: bool = False,
        headers: dict[str, str] | None = None,
    ) -> None:
        super().__init__(message)
        self.code = code
        self.status = status
        self.message = message
        self.retryable = retryable
        self.headers = headers or {}

    def body(self, request_id: str | None = None) -> dict:
        out = {
            "error": {
                "code": self.code,
                "message": self.message,
                "retryable": self.retryable,
            }
        }
        if request_id:
            out["request_id"] = request_id
        return out


def invalid_request(message: str) -> ApiError:
    return ApiError("invalid_request", 400, message)


def category_unsupported(key: str) -> ApiError:
    return ApiError("category_unsupported", 400, f"Category {key!r} is not supported.")


def sort_unsupported(sort: str, operation: str) -> ApiError:
    return ApiError(
        "sort_unsupported", 400, f"Sort {sort!r} is not supported for {operation}."
    )


def unauthorized() -> ApiError:
    return ApiError(
        "unauthorized",
        401,
        "A valid bearer token is required.",
        headers={"WWW-Authenticate": "Bearer"},
    )


def forbidden(scope: str) -> ApiError:
    return ApiError("forbidden", 403, f"The token lacks the {scope!r} scope.")


def not_found() -> ApiError:
    return ApiError("not_found", 404, "Unknown result reference.")


def result_expired() -> ApiError:
    return ApiError(
        "result_expired",
        410,
        "The result reference has expired. Run the search again explicitly.",
    )


def candidate_changed() -> ApiError:
    return ApiError(
        "candidate_changed",
        409,
        "The source item changed since it was listed; it was not resolved.",
    )


def source_item_gone() -> ApiError:
    return ApiError("source_item_gone", 404, "The source no longer has this item.")


def no_magnet() -> ApiError:
    return ApiError("no_valid_magnet", 422, "The source item has no valid magnet link.")


def rate_limited(retry_after: int) -> ApiError:
    return ApiError(
        "rate_limited",
        429,
        "Too many requests for this token.",
        retryable=True,
        headers={"Retry-After": str(retry_after)},
    )


def upstream_unavailable(message: str = "The source is unreachable.") -> ApiError:
    return ApiError("upstream_unavailable", 502, message, retryable=True)


def upstream_rate_limited() -> ApiError:
    return ApiError(
        "upstream_rate_limited",
        503,
        "The source is rate limiting requests.",
        retryable=True,
        headers={"Retry-After": "60"},
    )


def upstream_timeout() -> ApiError:
    return ApiError(
        "upstream_timeout", 504, "The source did not answer in time.", retryable=True
    )


def parse_changed() -> ApiError:
    return ApiError(
        "parse_changed",
        502,
        "The source page structure is not recognised; the parser may need an update.",
    )


def source_blocked() -> ApiError:
    return ApiError(
        "source_blocked",
        502,
        "The source address is not permitted by the service configuration.",
    )
