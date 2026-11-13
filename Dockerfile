# syntax=docker/dockerfile:1
# Installs the pinned in-tree sources with locked dependencies; nothing is
# fetched from GitHub at runtime.
FROM python:3.12-slim-bookworm AS build
RUN pip install --no-cache-dir uv==0.11.23
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never \
    UV_PROJECT_ENVIRONMENT=/app/.venv
WORKDIR /src
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --extra server --no-install-project
COPY docs/upstream/README.md docs/upstream/README.md
COPY LICENSE ./
COPY src ./src
RUN uv sync --frozen --no-dev --extra server --no-editable

FROM python:3.12-slim-bookworm
RUN useradd --system --uid 10001 --no-create-home --home-dir /nonexistent app \
    && mkdir -p /data && chown 10001:10001 /data
COPY --from=build /app/.venv /app/.venv
COPY LICENSE UPSTREAM.md /usr/share/doc/openplayx-torrent-search/
ENV PATH=/app/.venv/bin:$PATH PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
USER 10001
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8790
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8790/healthz', timeout=3)"]
ENTRYPOINT ["torrent-search-server", "--config", "/config/config.json", "--secrets", "/run/secrets/torrent-search.json"]
