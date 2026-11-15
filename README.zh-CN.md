# mcp-piratebay

[English](README.md) | 简体中文

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

[OpenPlayX](https://github.com/openplayx/openplayx) 的种子搜索服务，提供 MCP 服务和 HTTP API（原名 *torrent-search*）。它是只读服务，用来搜索管理员配置的种子索引站，为 OpenPlayX App 及其 AI 工具而做。服务是一个 Go 程序，页面解析器移植自上游的 [`thepiratebay-api`](https://github.com/9BitDaniel/thepiratebay-api) 库，见 [UPSTREAM.md](UPSTREAM.md)（英文）。

本服务只负责搜索，不运行下载引擎，不创建下载，也不存储媒体。在 OpenPlayX 中，App 展示搜索候选；用户选中一个并要求下载后，App 在这里解析出磁力链接，再交给 [mcp-qbittorrent](https://github.com/openplayx/mcp-qbittorrent) 的计划/添加流程。

搜索结果是外部索引站的数据。标题、大小和做种人数都是来源自己声明的，没有经过验证。来源标记（如 "trusted"）不代表安全检查。

## API

| 端点 | 用途 |
|---|---|
| `GET /healthz` | 存活检查（不访问来源站） |
| `GET /readyz` | token 已配置、缓存存储可写 |
| `GET /v1/capabilities` | 当前 token 可用的操作、排序方式、限制、版本 |
| `GET /v1/categories` | 分类 key（成人分类默认关闭） |
| `POST /v1/search` | `{"query", "category", "sort", "page"}`，返回来源站的一页结果 |
| `GET /v1/browse?category=&sort=&page=` | 浏览某个具体分类 |
| `GET /v1/top?category=` | 来源站的排行榜 |
| `GET /v1/recent?page=` | 最新条目 |
| `GET /v1/search-results/{result_ref}` | 列表中某个条目的规范化详情 |
| `POST /v1/search-results/{result_ref}/resolve` | 为选中的条目返回经过校验的磁力链接（需要 `resolve` 权限） |
| `GET /openapi.json`、`GET /docs` | 接口契约和可浏览的文档（[api/openapi.json](api/openapi.json)） |

所有 `/v1` 请求都需要 `Authorization: Bearer <token>`。更多细节见 [docs/api.md](docs/api.md)（英文）。

## MCP

`POST /mcp` 提供 `search`、`get_details`、`resolve_candidate` 和 `list_categories`；`tools/list` 只列出 token 权限允许的工具（`resolve_candidate` 需要 `resolve`）。搜索永远不会触发下载。

支持 Streamable HTTP 的一个子集：通过 `POST` 发送单条 JSON-RPC 2.0 消息，以 `application/json` 应答（不用 SSE，服务器不主动发消息）；无状态（不发 `Mcp-Session-Id`）；支持 `initialize`、`ping`、`tools/list`、`tools/call` 和通知；协议版本 2025-11-25、2025-06-18 和 2025-03-26。每个请求都需要与 HTTP API 相同的 Bearer token；来自外部 `Origin` 的请求会被拒绝。业务失败返回 `isError: true` 的工具结果，错误码与 HTTP API 相同。MCP 层调用的是与 HTTP 处理器相同的服务代码（没有第二份状态，也不回环请求自己）。

## 快速开始

依赖：Docker；本地开发需要 Go 1.26。

1. 生成 token 和配置：

   ```bash
   cd deploy
   cp config.example.json config.json      # 把 source.base_url 设为你要用的索引站
   cp secrets.example.json secrets.json
   TOKEN=$(openssl rand -hex 32)
   printf '%s' "$TOKEN" | shasum -a 256    # 把摘要填进 secrets.json 的 token_sha256
   ```

   也可以不用摘要，直接写 `"token": "<至少 24 个字符>"`。

2. 构建并启动：

   ```bash
   docker build -t openplayx/torrent-search:local ..
   TORRENT_SEARCH_IMAGE=openplayx/torrent-search:local docker compose up -d
   ```

   服务监听 `127.0.0.1:18790`（用 `BIND` 和 `PORT` 修改）。镜像里只有一个静态二进制，以非 root 用户运行，根文件系统只读，缓存保存在命名卷中。

3. 检查：

   ```bash
   curl -s http://127.0.0.1:18790/readyz
   curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:18790/v1/capabilities
   ```

## 配置

| 文件 | 内容 |
|---|---|
| `config.json`（[示例](deploy/config.example.json)） | 来源站（`base_url`、允许的主机、代理、超时、请求间隔）、缓存 TTL、限制、`allow_adult_categories` |
| `secrets.json`（[示例](deploy/secrets.example.json)） | token（名称、`token` 或 `token_sha256`、权限范围 `search` / `resolve`）、可选的代理地址 |

两个文件都已被 git 忽略。没有 `resolve` 权限的 token 只能搜索，拿不到磁力链接。

## 在 MCP 客户端中使用

```json
{
  "mcpServers": {
    "piratebay": {
      "type": "http",
      "url": "http://127.0.0.1:18790/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

在 OpenPlayX 中，到 **AI → 外部能力** 添加，填服务根地址和 token。

## 开发

```bash
scripts/check.sh                       # gofmt、go vet、全部离线测试、OpenAPI 契约检查
go run ./cmd/mcp-piratebay --config deploy/config.local.json --secrets deploy/secrets.json
```

`config.local.json` 和 `secrets.json` 已被 git 忽略。跟进上游解析器改动的步骤见 [UPSTREAM.md](UPSTREAM.md)（英文）。

## 参与贡献

欢迎提 issue 和 PR，见 [CONTRIBUTING.md](CONTRIBUTING.md)（英文）。安全问题请按 [SECURITY.md](SECURITY.md)（英文）私下报告。

## 声明

本项目不托管、不索引、不分发任何内容。它查询运营者配置的第三方索引站，原样返回该站列出的内容，不做验证。搜索和下载什么由你自己负责：只下载你有权下载的内容，并遵守适用于你的法律。本项目与 The Pirate Bay 及任何索引站都没有关联。

## 许可证

[MIT](LICENSE)。`internal/tpb/` 中的解析器移植自上游库，版权归 © 2026 Daniel；其余部分版权归 © 2026 OpenPlayX contributors，采用同一许可证。
