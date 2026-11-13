"""Service category and sort vocabulary mapped onto the upstream codes.

Codes mirror ``thepiratebay_api.models`` (checked by tests). Unknown keys are
rejected, never silently widened to an all-categories query.
"""

from __future__ import annotations

from dataclasses import dataclass

from . import errors


@dataclass(frozen=True)
class Category:
    key: str
    code: int
    label: str
    adult: bool = False

    @property
    def parent(self) -> str | None:
        return self.key.split(".")[0] if "." in self.key else None


_C = Category
CATEGORIES: tuple[Category, ...] = (
    _C("all", 0, "All"),
    _C("audio", 100, "Audio"),
    _C("audio.music", 101, "Music"),
    _C("audio.audiobooks", 102, "Audio books"),
    _C("audio.flac", 104, "FLAC"),
    _C("audio.other", 105, "Other audio"),
    _C("video", 200, "Video"),
    _C("video.movies", 201, "Movies"),
    _C("video.dvdr", 202, "Movies DVDR"),
    _C("video.clips", 204, "Music videos / clips"),
    _C("video.tv", 205, "TV shows"),
    _C("video.hd_movies", 207, "HD movies"),
    _C("video.hd_tv", 208, "HD TV shows"),
    _C("video.3d", 209, "3D"),
    _C("video.uhd_movies", 211, "UHD/4K movies"),
    _C("video.uhd_tv", 212, "UHD/4K TV shows"),
    _C("video.other", 299, "Other video"),
    _C("apps", 300, "Applications"),
    _C("apps.windows", 301, "Windows"),
    _C("apps.mac", 302, "Mac"),
    _C("apps.unix", 303, "UNIX"),
    _C("apps.ios", 305, "iOS"),
    _C("apps.android", 306, "Android"),
    _C("games", 400, "Games"),
    _C("games.pc", 401, "PC"),
    _C("games.mac", 402, "Mac"),
    _C("games.psx", 403, "PSx"),
    _C("games.xbox360", 404, "XBOX360"),
    _C("games.wii", 405, "Wii"),
    _C("games.handheld", 406, "Handheld"),
    _C("games.ios", 407, "iOS"),
    _C("games.android", 408, "Android"),
    _C("games.other", 499, "Other games"),
    _C("adult", 500, "Adult", True),
    _C("adult.movies", 501, "Adult movies", True),
    _C("adult.dvdr", 502, "Adult DVDR", True),
    _C("adult.pictures", 503, "Adult pictures", True),
    _C("adult.games", 504, "Adult games", True),
    _C("adult.hd", 505, "Adult HD", True),
    _C("adult.clips", 506, "Adult clips", True),
    _C("adult.uhd", 507, "Adult UHD", True),
    _C("adult.other", 599, "Adult other", True),
    _C("other", 600, "Other"),
    _C("other.ebooks", 601, "E-books"),
    _C("other.comics", 602, "Comics"),
    _C("other.pictures", 603, "Pictures"),
    _C("other.covers", 604, "Covers"),
    _C("other.other", 699, "Other"),
)
_BY_KEY = {c.key: c for c in CATEGORIES}

# Service sort key -> upstream SortBy code.
SORTS: dict[str, int] = {
    "relevance": 99,
    "seeders_desc": 7,
    "seeders_asc": 8,
    "size_desc": 5,
    "size_asc": 6,
    "date_desc": 3,
    "date_asc": 4,
}
SEARCH_SORTS = tuple(SORTS)
# The source answers 404 for browse with the relevance order.
BROWSE_SORTS = tuple(k for k in SORTS if k != "relevance")


def available(allow_adult: bool) -> list[Category]:
    return [c for c in CATEGORIES if allow_adult or not c.adult]


def lookup(key: str, allow_adult: bool) -> Category:
    cat = _BY_KEY.get(key)
    if cat is None or (cat.adult and not allow_adult):
        raise errors.category_unsupported(key)
    return cat


def sort_code(sort: str, operation: str, allowed: tuple[str, ...]) -> int:
    if sort not in allowed:
        raise errors.sort_unsupported(sort, operation)
    return SORTS[sort]
