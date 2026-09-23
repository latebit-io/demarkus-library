# Theming and branding the reading room

How an operator presents the demarkus Library as their own room: a display
name, a logo, and a stylesheet that retints every page — the reading room and
the login turnstile alike. No fork, no template edits; everything is runtime
configuration.

## The knobs

| Env var | What it does |
|---|---|
| `DEMARKUS_BRANDING` | Path to a branding manifest (YAML, below): everything in one file, including per-world overrides and the display vocabulary. |
| `DEMARKUS_BRAND` | Display name in page titles, the nav wordmark, and the login card. Default `demarkus Library`. |
| `DEMARKUS_LOGO` | Path to an image file (SVG/PNG/…), shown beside the brand name in the nav and above the login card. Served at `/theme/logo`. |
| `DEMARKUS_FAVICON` | Path to the browser-tab icon. Served at `/theme/favicon`; without one the room ships its own mark. |
| `DEMARKUS_THEME_CSS` | Path to a stylesheet, loaded **after** the built-in styles on every page. Served at `/theme/site.css`. |
| `DEMARKUS_TERM_UNIVERSE` | Display word for the whole-knowledge scope (the floor, the overlay, the dock anchor). Default `Universe`. |
| `DEMARKUS_STATIC_DIR` | Directory whose files shadow the embedded `/static/` assets by name. Drop in a whole `library.css` to replace the stock sheet; everything else still comes from the binary. |

All optional; unset keeps the stock room. The single-value env vars layer
over the manifest (env wins), so a one-off `DEMARKUS_BRAND` works beside a
manifest. Paths are stat-checked at startup — a typo'd path stops the server
loudly instead of shipping a broken logo or an unstyled room. The manifest,
logo, and theme stylesheet are read once at startup, so changing them requires
a restart; files under `DEMARKUS_STATIC_DIR` are served per request and take
effect on the next load.

## The manifest

`DEMARKUS_BRANDING` names a YAML file. Relative asset paths resolve against
the manifest's own directory, so the manifest and its assets travel together
(one ConfigMap, one directory). Unknown keys stop startup.

```yaml
name: ACME Brain            # DEMARKUS_BRAND
logo: acme.svg              # DEMARKUS_LOGO
favicon: acme.svg           # DEMARKUS_FAVICON
css: site.css               # DEMARKUS_THEME_CSS

# Design tokens: recolor the room without writing CSS (see below).
theme:
  paper: light-dark(#fbf7f0, #0e1116)
  accent: "#0f766e"

terms:
  universe: Knowledge       # DEMARKUS_TERM_UNIVERSE

# Per-world overrides: shown while that world is in focus (the focused
# trail pane, or a /w/<world>/ page). Empty fields inherit the room's.
worlds:
  soul.demarkus.io:
    name: Fritz's Soul
    logo: soul.svg
    css: soul.css
  latebit:
    name: Latebit Brain
    theme:
      accent: "#b45309"
```

A ready-to-copy manifest, stylesheet, and logo live in
[branding-example/](branding-example/); a test loads that directory on every
run, so the example cannot drift from the code.

## Design tokens: branding without CSS

`theme:` sets the room's design tokens directly, so most rebrands need no
stylesheet at all. The same block works per world. Settable tokens, which are
the `:root` custom properties in `library.css`:

| Group | Tokens |
|---|---|
| Surfaces | `paper`, `ink`, `muted`, `faint` |
| Type | `font-prose`, `font-ui`, `font-mono` |
| Signal | `ok`, `warn`, `danger`, `info`, `accent` |
| Layout | `margin-w`, `gutter` |

Use `light-dark(a, b)` for separate light and dark values; a plain value
applies to both. Values are checked: an unknown token name, or a value that
would close the rule, start another, or fetch something, is refused. In the
manifest that stops startup; in a world's own document the token block is
dropped and the rest of the branding still applies.

The tokens load after the built-in styles and before any stylesheet, so
`css:` can still override them.

Per-world assets are served at `/theme/worlds/<world>/logo` and
`/theme/worlds/<world>/site.css`. A world stylesheet loads after the room
theme, so it only needs the tokens it changes. It is linked from the page
body rather than the head: htmx keeps only the title of a boosted response's
head, so a head link would stick to the first world visited.

The floor (the whole-universe view) and the login turnstile always show the
room's identity; a world's identity appears once the reader is inside it.

## Two layers: the room from files, a world from its documents

The manifest, the env vars, and `DEMARKUS_STATIC_DIR` are the operator's
deploy-time surface for the room as a whole. A world brands itself from
inside the knowledge system (ADR 0008): documents under
`/.well-known/library/` on the world, which the library reads (cached, one
minute) ahead of the manifest's `worlds` entry.

| Document | Payload |
|---|---|
| `branding.md` | A `yaml` fenced block holding `name: …` and an optional `theme:` token block. The anchor: without it the others are not read. |
| `site.css.md` | A `css` fenced block. Loads after the room theme while the world is open. |
| `logo.md` | An `svg` fenced block, or a `base64` block whose info string names the content type (`base64 image/png`). PNG, JPEG, GIF, WebP, or SVG without scripts or event handlers; up to 256 KB. |
| `favicon.md` | Same shapes as `logo.md`. Read for the hub world only (below). |

### The hub world brands the room

The same documents on the hub world (`DEMARKUS_HUB`, `library.hub` in the
chart) brand the room as a whole: its name, logo, favicon, and stylesheet
apply to every page, the sign-in page included, and win over the manifest
and env values field by field. Nothing is uploaded to the cluster and no
pod restarts: publish the documents, or use the hub's branding desk, and
readers see the new identity within a minute. The file manifest stays as
the bootstrap and the fallback for whatever the hub leaves unset. The
`terms` vocabulary remains file-only.

Whoever may write the hub may rebrand the room, which is the same power
they already have over its index. Keep the hub's writers to the people you
would hand the ConfigMap to.

The hub's sheet links from the head as the room theme, so while the hub
itself is in focus it is not linked again from the body. When the hub is
private, the sign-in page reads it without a session; the library keeps the
last identity it resolved rather than blanking the page, so the turnstile
brands once any signed-in reader has loaded a page.

A world's own branding documents look like this:

````markdown
# Branding

How this world presents itself in the library.

```yaml
name: Fritz's Soul
```
````

Each document carries an H1 and a one-sentence summary above the fence, so
the style gate stays quiet and the document reads sensibly in any client.
Assets serve at `/theme/worlds/<world>/logo`, `/theme/worlds/<world>/favicon`,
and `/theme/worlds/<world>/site.css`
with `X-Content-Type-Options: nosniff` and a sandboxing Content-Security-Policy,
so a world's logo can never run as a page on the library's origin. A logo whose
bytes do not match its declared type, or an SVG carrying `<script>`, event
handlers, or external references, is ignored.

A stylesheet from a document may not reach another origin: `@import`, any
`://` or `//` reference, `expression()`, `behavior:` and `-moz-binding` are
refused at the desk and ignored when read from a world (the name and logo
still apply). CSS cannot run script, but a selector on an attribute value
plus a background URL can leak that value to whoever hosts the URL; relative,
same-origin, and `data:` references are fine. Every page also carries a
Content-Security-Policy that limits fonts, `@import`, and XHR to the
library's own origin and forbids inline or eval'd script, so the same rule
holds even for a stylesheet from the file manifest.

### The branding desk

Whoever the world already lets write may brand it. There is no separate admin
role: the gate is a broker session whose bearer carries write scope, or a
configured token in direct QUIC mode. Branding documents are ordinary
documents, so the world's own write authorization is the right gate, and a
second library-specific role would only be able to disagree with it.

The desk is at `/w/<world>/branding`, linked from the world map beside "new
document" wherever the write affordances show. It offers a name field, a logo
upload or pasted SVG, the design tokens as plain form fields, and a stylesheet
box for anything the tokens cannot express. On the hub world it also takes a
favicon and says that its fields brand the whole room.

In broker mode the link appears once you are signed in. In QUIC mode the write
affordances stay hidden, as they do for editing, so reach the desk by URL; it
works when the configured token grants writes. Saving publishes the
documents above with tags and a low importance; history and revert come
with the world. Empty fields leave a document alone; the remove boxes clear
one.

### Replacing the stock stylesheet without a Go build

`DEMARKUS_THEME_CSS` overrides; `DEMARKUS_STATIC_DIR` replaces. A directory
holding your own `library.css` (start from the served `/static/library.css`)
swaps the whole sheet; `islands.js` and the vendored libraries stay embedded
unless you shadow them too. The supported "custom build" is a derived image:

```dockerfile
FROM ghcr.io/latebit-io/demarkus-library:0.27.0
COPY brand/ /etc/demarkus-library/branding/
ENV DEMARKUS_BRANDING=/etc/demarkus-library/branding/branding.yaml \
    DEMARKUS_STATIC_DIR=/etc/demarkus-library/branding/static
```

Class names and the `:root` tokens in `library.css` are the contract an
overlay relies on across upgrades.

### Vocabulary

`terms.universe` renames what readers call the whole knowledge scope:
"Universe" is the stock word; an operator whose readers say "Knowledge" or
"Brain" sets it here. It changes the floor pane's title, the overlay's
heading, the dock's left anchor, the palette's recent-row label, and the
librarian's context description. Routes (`/u`, `/t/u`) and internal scope
keys do not change, so trails and agent-minted URLs stay valid.

## Writing a theme

The built-in styles live in one stylesheet
(`internal/adapter/inbound/web/static/library.css`, served at
`/static/library.css` on any running instance) and route every color and
font through CSS custom properties on `:root`. Overriding those tokens is
the intended theming surface — a few lines rebrand the whole room, light and
dark schemes both:

```css
:root {
  /* Surfaces */
  --paper: light-dark(#f7f3ea, #101418);   /* page background */
  --ink:   light-dark(#1d2733, #d8e1ea);   /* text */
  --muted: light-dark(#1d273399, #d8e1ea99);
  --faint: light-dark(#1d273322, #d8e1ea2a);

  /* Type */
  --font-prose: Palatino, Georgia, serif;   /* reading text */
  --font-ui:    system-ui, sans-serif;      /* chrome: nav, dock, badges */
  --font-mono:  ui-monospace, monospace;    /* code, addresses */

  /* Signal colors: status badges, callouts, graph trust cues */
  --ok: #1a7f37;      /* accepted */
  --warn: #9a6700;    /* wip */
  --danger: #cf222e;  /* errors, caution callouts */
  --info: #0969da;    /* note callouts, inbound graph edges */
  --accent: #8250df;  /* important callouts */

  /* Layout */
  --margin-w: 250px;  /* sidenote margin width */
  --gutter: 2.5rem;
}
```

Use `light-dark(a, b)` to give a token separate light/dark values; a plain
value applies to both. To pin the room to a single scheme regardless of the
reader's OS preference, add `:root { color-scheme: only light; }` (or
`only dark`).

The theme file loads last in the cascade, so it is not limited to tokens —
any rule at equal specificity wins over the built-ins:

```css
nav a.brand { color: #d92662; }
```

Verify a running theme at `/theme/site.css` and `/theme/logo`.

## Single host (binary, no Kubernetes)

Put the files anywhere on disk and set the env vars on the process:

```sh
DEMARKUS_BRAND="Acme Knowledge" \
DEMARKUS_LOGO=/etc/demarkus-library/logo.svg \
DEMARKUS_THEME_CSS=/etc/demarkus-library/site.css \
demarkus-library
```

Under systemd, a drop-in keeps the branding beside the unit:

```ini
# /etc/systemd/system/demarkus-library.service.d/branding.conf
[Service]
Environment="DEMARKUS_BRAND=Acme Knowledge"
Environment=DEMARKUS_THEME_CSS=/etc/demarkus-library/site.css
Environment=DEMARKUS_LOGO=/etc/demarkus-library/logo.svg
```

```sh
systemctl daemon-reload && systemctl restart demarkus-library
```

### Docker

Mount the assets and pass the same env vars:

```sh
docker run -p 8080:8080 \
  -v ./brand:/brand:ro \
  -e DEMARKUS_BRAND="Acme Knowledge" \
  -e DEMARKUS_THEME_CSS=/brand/site.css \
  -e DEMARKUS_LOGO=/brand/logo.svg \
  ghcr.io/latebit-io/demarkus-library
```

## Kubernetes (Helm chart)

The chart (`deploy/helm/demarkus-library`) wires branding from an
operator-managed ConfigMap so assets never live in values files.

1. Create the ConfigMap from your files (the manifest and every asset it
   names, side by side — ConfigMap keys are flat, which is why the manifest
   resolves relative paths against its own directory):

   ```sh
   kubectl create configmap library-branding \
     --from-file=branding.yaml --from-file=site.css --from-file=logo.svg \
     --from-file=soul.css --from-file=soul.svg
   ```

2. Point `library.branding` at it:

   ```yaml
   library:
     branding:
       configMap: library-branding   # mounted read-only at /etc/demarkus-library/branding
       manifestKey: branding.yaml    # DEMARKUS_BRANDING; "" for the single-value keys only
       name: Acme Knowledge          # DEMARKUS_BRAND (optional beside a manifest; env wins)
       themeCSSKey: site.css         # DEMARKUS_THEME_CSS; "" skips
       logoKey: logo.svg             # DEMARKUS_LOGO; "" (default) skips
       faviconKey: logo.svg          # DEMARKUS_FAVICON; "" (default) skips
       universeTerm: Knowledge       # DEMARKUS_TERM_UNIVERSE
   ```

The chart mounts the ConfigMap at `/etc/demarkus-library/branding` and sets
`DEMARKUS_BRANDING` / `DEMARKUS_THEME_CSS` / `DEMARKUS_LOGO` /
`DEMARKUS_FAVICON` to the named keys. `manifestKey`, `logoKey` and
`faviconKey` fail the render when set without `configMap`, rather than being
silently ignored. `themeCSSKey` is the exception: it ships a non-empty default
(`site.css`), so a set value cannot be told from the default, and it is simply
unused until a `configMap` exists. `name` alone works without any ConfigMap.

Rolling a rebrand is operator-driven, matching the
chart's Secret-rotation posture: update the ConfigMap, then restart the
deployment (`kubectl rollout restart deploy/<release>-demarkus-library`) —
assets are read at startup.
