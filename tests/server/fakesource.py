"""Self-written HTML in the source's page structure plus an httpx
MockTransport serving it. No network, no third-party page copies."""

from __future__ import annotations

import html
from dataclasses import dataclass, field

import httpx

BASE = "https://tpb.test"
HASH_A = "a" * 40
HASH_B = "b" * 40
HASH_C = "c" * 40


@dataclass
class Item:
    id: int
    title: str
    info_hash: str | None
    size: str = "1.50&nbsp;GiB"
    date: str = "04-01&nbsp;2019"
    seeders: str = "10"
    leechers: str = "2"
    uploader: str = "alice"
    category: str = "Video &gt; HD - Movies"


def magnet_for(item: Item) -> str:
    return (
        f"magnet:?xt=urn:btih:{item.info_hash}&amp;dn={html.escape(item.title)}"
        "&amp;tr=udp%3A%2F%2Ftracker.example%3A1337%2Fannounce"
        "&amp;tr=javascript%3Aalert(1)"
    )


def row(item: Item) -> str:
    magnet_cell = (
        f'<a href="{magnet_for(item)}" title="Download this torrent using magnet">'
        '<img src="/static/img/icon-magnet.gif" alt="Magnet link"/></a>'
        if item.info_hash
        else ""
    )
    return f"""<tr>
<td class="vertTh"><a href="{BASE}/browse/207" title="More from this category">{item.category}</a></td>
<td><a href="{BASE}/torrent/{item.id}/slug_{item.id}" title="Details">{html.escape(item.title)}</a>
</td>
<td>{item.date}</td>
<td><nobr>{magnet_cell}</nobr></td>
<td align="right">{item.size}</td>
<td align="right">{item.seeders}</td>
<td align="right">{item.leechers}</td>
<td><a href="/user/{item.uploader}/" title="Browse">{html.escape(item.uploader)}</a></td>
</tr>"""


def list_page(items: list[Item], *, pattern: str = "search", pages: int = 1) -> str:
    pagination = ""
    if pages > 1:
        links = "&nbsp;".join(
            f'<a href="/{pattern}/x/{n}/99/0">{n}</a>' for n in range(2, pages + 1)
        )
        pagination = f'<tr><td colspan="9" style="text-align:center;"><b>1</b>&nbsp;{links}</td></tr>'
    body = "\n".join(row(i) for i in items)
    return f"""<!DOCTYPE html><html><head><title>The Pirate Bay</title></head><body>
<table id="searchResult"><thead id="tableHead"><tr class="header">
<th>Type</th><th>Name</th><th>Uploaded</th><th>&nbsp;</th><th>Size</th><th>SE</th><th>LE</th><th>ULed by</th>
</tr></thead>
{body}
{pagination}
</table></body></html>"""


NO_HITS = """<!DOCTYPE html><html><body><h2><span>Search results: zz</span></h2>
No hits. Try adding an asterisk in you search phrase.</body></html>"""

CHANGED_LAYOUT = (
    """<!DOCTYPE html><html><body><div class="new-layout">
<a href="magnet:?xt=urn:btih:%s">x</a></div></body></html>"""
    % HASH_A
)


def detail_page(
    item: Item,
    *,
    page_hash: str | None = None,
    description: str = "Plain description.",
    trusted: bool = True,
) -> str:
    badge = (
        '<img src="/static/img/trusted.png" alt="Trusted" title="Trusted"/>'
        if trusted
        else ""
    )
    magnet = (
        f"<a style=\"background-image: url('/static/img/icons/icon-magnet.gif');\" "
        f'href="{magnet_for(item)}" title="Get this torrent">Get This Torrent</a>'
        if item.info_hash
        else ""
    )
    return f"""<!DOCTYPE html><html><body>
<div id="detailsframe">
<div id="title">{html.escape(item.title)}</div>
<h1 id="title">{html.escape(item.title)}</h1>
<div id="details">
<dl class="col1">
<dt>Type:</dt><dd><a href="/browse/207" title="More from this category">Video &gt; HD - Movies</a></dd>
<dt>Files:</dt><dd><a href="/ajax_details_filelist.php?id={item.id}">3</a></dd>
<dt>Size:</dt><dd>6.07&nbsp;GiB&nbsp;(6518974464&nbsp;Bytes)</dd>
</dl>
<dl class="col2">
<dt>Uploaded:</dt><dd>2026-04-25 16:35:39 GMT</dd>
<dt>By:</dt><dd><a href="/user/{item.uploader}/" title="Browse">{item.uploader}</a> {badge}</dd>
<dt>Seeders:</dt><dd>118</dd>
<dt>Leechers:</dt><dd>87</dd>
<dt>Comments</dt><dd>0 &nbsp; </dd>
<dt>Info Hash:</dt><dd></dd>
            {page_hash if page_hash is not None else (item.info_hash or "").upper()}        </dl>
<div class="download">{magnet}</div>
<div class="nfo"><pre>{html.escape(description)}</pre></div>
</div></div></body></html>"""


ITEMS = [
    Item(1001, "Ubuntu 26.04 Desktop amd64", HASH_A),
    Item(
        1002,
        "Debian 13 netinst <b>bold</b>\u202e",
        HASH_B,
        size="956.66&nbsp;MiB",
        date="Today&nbsp;01:00",
    ),
    Item(1003, "No magnet item", None, seeders="0", leechers="0"),
]


@dataclass
class FakeSource:
    """Routes source paths to canned responses and records requests."""

    routes: dict[str, httpx.Response | Exception] = field(default_factory=dict)
    requests: list[str] = field(default_factory=list)

    def set(
        self, path: str, body: str | int | Exception, status: int = 200, headers=None
    ):
        if isinstance(body, Exception):
            self.routes[path] = body
        elif isinstance(body, int):
            self.routes[path] = httpx.Response(body, headers=headers or {}, text="")
        else:
            self.routes[path] = httpx.Response(status, headers=headers or {}, text=body)

    def handler(self, request: httpx.Request) -> httpx.Response:
        path = request.url.raw_path.decode()
        self.requests.append(path)
        hit = self.routes.get(path)
        if hit is None:
            return httpx.Response(404, text="<html><title>Not Found</title></html>")
        if isinstance(hit, Exception):
            raise hit
        return httpx.Response(hit.status_code, headers=hit.headers, content=hit.content)

    @property
    def transport(self) -> httpx.MockTransport:
        return httpx.MockTransport(self.handler)


def standard() -> FakeSource:
    src = FakeSource()
    src.set("/search/ubuntu/1/99/0", list_page(ITEMS, pages=3))
    src.set("/search/zz/1/99/0", NO_HITS)
    src.set("/top/all", list_page(ITEMS[:2], pattern="top"))
    src.set("/top/200", list_page(ITEMS[:1], pattern="top"))
    src.set("/recent/1", list_page(ITEMS[:2], pattern="recent"))
    src.set("/browse/207/1/3", list_page(ITEMS[:1], pattern="browse"))
    for item in ITEMS:
        src.set(f"/torrent/{item.id}", detail_page(item))
    return src
