# Theming and branding the reading room

How an operator presents the demarkus Library as their own room: a display
name, a logo, and a stylesheet that retints every page — the reading room and
the login turnstile alike. No fork, no template edits; everything is runtime
configuration.

## The three knobs

| Env var | What it does |
|---|---|
| `DEMARKUS_BRAND` | Display name in page titles, the nav wordmark, and the login card. Default `demarkus Library`. |
| `DEMARKUS_LOGO` | Path to an image file (SVG/PNG/…), shown beside the brand name in the nav and above the login card. Served at `/theme/logo`. |
| `DEMARKUS_THEME_CSS` | Path to a stylesheet, loaded **after** the built-in styles on every page. Served at `/theme/site.css`. |

All optional; unset keeps the stock room. Asset paths are stat-checked at
startup — a typo'd path stops the server loudly instead of shipping a broken
logo or an unstyled room. Assets are read once at startup: changing a file
requires a restart.

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

1. Create the ConfigMap from your files:

   ```sh
   kubectl create configmap library-branding \
     --from-file=site.css --from-file=logo.svg
   ```

2. Point `library.branding` at it:

   ```yaml
   library:
     branding:
       name: Acme Knowledge          # DEMARKUS_BRAND
       configMap: library-branding   # mounted read-only at /etc/demarkus-library/branding
       themeCSSKey: site.css         # default; "" skips the stylesheet
       logoKey: logo.svg             # "" (default) skips the logo
   ```

The chart mounts the ConfigMap at `/etc/demarkus-library/branding` and sets
`DEMARKUS_THEME_CSS` / `DEMARKUS_LOGO` to the named keys. `name` alone works
without any ConfigMap. Rolling a rebrand is operator-driven, matching the
chart's Secret-rotation posture: update the ConfigMap, then restart the
deployment (`kubectl rollout restart deploy/<release>-demarkus-library`) —
assets are read at startup.
