# demarkus-library

The web front-end ("Universe Library") for a demarkus universe: a server-rendered
Go + htmx reading room over a broker-fronted knowledge system. Renders the library
metaphor demarkus already has (bookshelf server, librarian agent, LOOKUP card
catalog, worlds-as-collections) into a human-facing web app.

Plan (source of truth): `mark://soul.demarkus.io/plans/universe-library.md`.

## Status — Phase 0 (foundation spike)

FETCH a demarkus document and render it server-side through goldmark + bluemonday,
served as HTML over Echo. Talks to a demarkus world directly over QUIC (the real
fetch client); the broker MCP path and org OAuth land in Phase 1.

## Architecture

Server-rendered Go + [Echo](https://echo.labstack.com) v5 + htmx; no JSON tier,
no SPA. **Hexagonal (ports & adapters)** — dependencies point inward (adapters →
ports → core); the core knows nothing of Echo, QUIC, or goldmark. Echo idioms and
file naming follow `latebit-io/bulwarkauth`.

```
cmd/demarkus-library/        composition root — wires adapters into the core
internal/core/
  domain/                    entities + domain errors (no external deps)
  port/                      inbound + outbound port interfaces
  service/                   application core (the hexagon)
internal/adapter/
  inbound/web/               Echo handlers/routes/view  (driving adapter)
  outbound/world/            demarkus QUIC fetch → WorldGateway port
  outbound/markdown/         goldmark + bluemonday → Renderer port
```

Ports:

- **Inbound (driving):** `port.ReadingService` — the use cases the web adapter drives.
- **Outbound (driven):** `port.WorldGateway` (fetch a raw doc from a world),
  `port.Renderer` (markdown → sanitized HTML).

Phase 1 swaps the `world` adapter for a broker MCP-gateway adapter and adds an
OAuth session — the core and web adapter are untouched.

### Front-end philosophy (ADR 0003)

SSR-first, **htmx-hard, no JSON**. The server renders all HTML; htmx is the only
interaction layer (returning server-rendered fragments); there is no JSON API and
no client-side state. htmx is **vendored** (`web/static/htmx.min.js`) and served
from the binary — no CDN. JS islands (CodeMirror editor, graph canvas) are a
last resort, each recorded as a concession in ADR 0003.

## Run

```sh
go run ./cmd/demarkus-library
# open http://localhost:8080
```

Configuration (environment):

| Var | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `DEMARKUS_HOST` | `soul.demarkus.io` | demarkus world host (`host[:port]`) |
| `DEMARKUS_DEFAULT_DOC` | `/index.md` | document served at `/` |
| `DEMARKUS_AUTH` | _(empty)_ | read token for private paths |
| `DEMARKUS_INSECURE` | `true` | skip TLS verification (dev worlds use self-signed certs) |

`/d/<path>` reads any document in the world.

## Notes

Phase 0 uses `replace` directives pointing the demarkus client/protocol modules
at a local checkout (`../demarkus`). Swap for tagged versions once published.
