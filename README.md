# demarkus-library

The web front-end ("Universe Library") for a demarkus universe: a server-rendered
Go + htmx reading room over a knowledge system. Renders the library metaphor
demarkus already has (bookshelf server, librarian agent, LOOKUP card catalog,
worlds-as-collections) into a human-facing web app.

Plans and ADRs live in the soul world (source of truth:
`mark://soul.demarkus.io/plans/universe-library.md`).

## The reading room

- **Trail canvas** (`/t/*`) — a fixed-viewport canvas of reading columns, root →
  focus; each pane scrolls internally and the margin (trust signals, backlinks,
  properties) is summoned per-pane via the `?meta=` lens. A trail URL serializes
  the whole reading context and is the shareable object humans and agents
  exchange — format spec in [docs/trail-format.md](docs/trail-format.md).
- **Documents** (`/w/:world/d/*`) — goldmark-rendered, bluemonday-sanitized
  markdown with syntax highlighting (chroma), alert callouts, emoji, and
  lazy-loaded mermaid + KaTeX islands. Tag pages, full-text search, edition
  history (`/w/:world/versions/*`), and raw source (`/w/:world/raw/*`).
- **Hover preview cards** — in-app document links load a tiny server fragment on
  `mouseenter` (htmx + CSS anchor positioning, no custom JS) showing title,
  status, and opening line, served from the rendered-document cache.
- **The floor** (`/u`) — the universe view over the hub's published topology
  (falls back to `mark_worlds` + observed links); per-world map at
  `/w/:world/u` and a link-graph canvas at `/w/:world/g/*`.
- **Cataloging desk** — create (`/w/:world/new`), edit (`/w/:world/edit/*`),
  and append (`/w/:world/append/*`) with a side-by-side live preview rendered
  by the same pipeline the reader uses. Writes are version-guarded: a stale
  save comes back as a merge candidate to review, never a silent overwrite.
  Agent-facing style rules in
  [docs/authoring-contract.md](docs/authoring-contract.md).
- **AI librarian** (`/a`) — a nib-backed agent over the core's read-only ports,
  answering as an SSE-streamed pane on the canvas. Feature-dark unless an LLM
  provider is configured (nib `llm.json`, `LLM_API_KEY`/`LLM_BASE_URL`/
  `LLM_MODEL`, or the nib key store); without one the pane reads "not on duty".
- **Federation** — `mark://` links to demarkus hosts outside the home
  world/knowledge system resolve as direct, anonymous, tokenless QUIC reads
  (default on; `DEMARKUS_FEDERATION=false` pins readers to home).

## Architecture

Server-rendered Go + [Echo](https://echo.labstack.com) v5 + htmx; no JSON tier,
no SPA. **Hexagonal (ports & adapters)** — dependencies point inward (adapters →
ports → core); the core knows nothing of Echo, QUIC, or goldmark. Echo idioms and
file naming follow `latebit-io/bulwarkauth`.

```text
cmd/demarkus-library/        composition root — config, transport selection, wiring
internal/core/
  domain/                    entities + domain errors (no external deps)
  port/                      inbound + outbound port interfaces
  service/                   application core (the hexagon): reading, editing,
                             trails, floor/graph, caches
internal/adapter/
  inbound/web/               Echo handlers/routes/views/templates (driving adapter)
    session/                 in-memory session + pending-login stores (broker mode)
  librarian/                 nib agent → Librarian port
  outbound/world/            direct demarkus QUIC fetch → WorldGateway port
  outbound/broker/           broker MCP gateway → WorldGateway port
  outbound/federated/        routes mark:// refs: home vs external hosts
  outbound/oauth/            broker OAuth client (code + PKCE, confidential)
  outbound/markdown/         goldmark + bluemonday → Renderer port
  outbound/cache/            in-memory LRU → DocumentCache port
```

### Transports

`DEMARKUS_TRANSPORT` selects the outbound world adapter; core and web handlers
are identical in both modes.

- **`quic`** (default) — one world read directly over the demarkus QUIC fetch
  client, no login. The demo/dev path.
- **`broker`** — reads go through the broker's MCP gateway with the reader's
  bearer; the reading room sits behind the org-login turnstile (OAuth code +
  PKCE as a registered confidential web client, tokens server-side, opaque
  session cookie, CSRF on every state-changing request).

### Front-end philosophy (ADR 0003)

SSR-first, **htmx-hard, no JSON**. The server renders all HTML; htmx is the only
interaction layer (returning server-rendered fragments); there is no JSON API
and no client-side state. All assets are vendored and served from the binary —
no CDN: htmx + its SSE extension, and the two rendering islands (mermaid,
KaTeX), which lazy-load only when a page contains something to render and
degrade to readable source without JS. Each island is recorded as a concession
in ADR 0003.

## Run

```sh
go run ./cmd/demarkus-library
# open http://localhost:8080
```

Configuration (environment):

| Var | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `DEMARKUS_TRANSPORT` | `quic` | `quic` (direct world) or `broker` (MCP gateway + login) |
| `DEMARKUS_FEDERATION` | `true` | follow `mark://` links to external hosts (anonymous QUIC reads) |
| `DEMARKUS_HUB` | home world (quic) / _(empty)_ | world publishing the universe topology for the floor |
| `DEMARKUS_PANE_SCROLL` | `true` | pane-scroll room (ADR 0007); `false` = legacy page-scroll room |
| `DEMARKUS_LLM_KEYSTORE` | `true` | let the librarian read nib's on-disk key store; `false` = env-only |
| `DEMARKUS_TLS_CERT` / `DEMARKUS_TLS_KEY` | _(empty)_ | serve HTTPS directly (local dev; the cluster ingress terminates TLS) |
| `DEMARKUS_BRAND` | `demarkus Library` | display name in titles, nav, and the login card |
| `DEMARKUS_LOGO` | _(empty)_ | path to a logo image, shown beside the brand name (served at `/theme/logo`) |
| `DEMARKUS_THEME_CSS` | _(empty)_ | path to an override stylesheet, loaded after the built-in styles (served at `/theme/site.css`) |

Theming: the built-in styles live in one stylesheet
(`internal/adapter/inbound/web/static/library.css`, served at
`/static/library.css`) and route every color and font through CSS custom
properties on `:root` — `--paper`, `--ink`, `--muted`, `--faint` (surfaces),
`--font-prose`, `--font-ui`, `--font-mono` (type), `--ok`, `--warn`,
`--danger`, `--info`, `--accent` (signal colors), `--margin-w`, `--gutter`
(layout). A `DEMARKUS_THEME_CSS` stylesheet that overrides only those tokens
rebrands the whole room (both light and dark via `light-dark()`); it loads
last, so any further rule wins the cascade too.

[docs/theming.md](docs/theming.md) is the full guide — the token reference,
example themes, and per-deployment instructions (binary/systemd, Docker, and
the Helm chart's `library.branding` ConfigMap wiring).

Direct-QUIC mode (`quic`):

| Var | Default | Meaning |
|---|---|---|
| `DEMARKUS_HOST` | `soul.demarkus.io` | demarkus world host (`host[:port]`) |
| `DEMARKUS_DEFAULT_DOC` | `/index.md` | document served at `/` |
| `DEMARKUS_AUTH` | _(empty)_ | read token for private paths |
| `DEMARKUS_INSECURE` | `true` | skip TLS verification (dev worlds use self-signed certs) |

Broker mode (`broker`) — all of `DEMARKUS_BROKER_URL`, `DEMARKUS_CLIENT_ID`,
`DEMARKUS_CLIENT_SECRET`, `DEMARKUS_REDIRECT_URI`, and `DEMARKUS_WORLD` are
required; startup fails loudly on a missing one.

| Var | Default | Meaning |
|---|---|---|
| `DEMARKUS_BROKER_URL` | — | broker origin, e.g. `https://broker.example.org` |
| `DEMARKUS_CLIENT_ID` / `DEMARKUS_CLIENT_SECRET` | — | webClients registry entry |
| `DEMARKUS_REDIRECT_URI` | — | must exactly match a registered redirect URI |
| `DEMARKUS_WORLD` | — | world name for `mark://<world>/<path>` reads |
| `DEMARKUS_SCOPES` | `mark.read` | OAuth scopes (space-separated) |
| `DEMARKUS_SESSION_TTL` | `720h` | absolute session lifetime |
| `DEMARKUS_COOKIE_SECURE` | `true` | Secure flag on the session cookie (`false` only for localhost dev) |

## Deploy

`Dockerfile` + Helm chart at `deploy/helm/demarkus-library`. Sessions and the
rendered-document cache are in-memory and per-pod — the chart's
`replicaCount: 1` + `Recreate` posture assumes single-pod state.

## Docs

- [docs/trail-format.md](docs/trail-format.md) — trail URL format (the shared reading-context object)
- [docs/authoring-contract.md](docs/authoring-contract.md) — how agents write markdown the room renders well
- ADRs and phase plans: soul world (`mark://soul.demarkus.io/plans/universe-library.md` and the ADRs it links)
