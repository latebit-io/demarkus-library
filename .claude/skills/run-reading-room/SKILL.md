---
name: run-reading-room
description: Launch and drive the demarkus-library reading room locally, against a demarkus world, and screenshot it. Use when asked to run, start, or screenshot the app, or to confirm a change works in the real app rather than only in tests.
---

# Run the reading room

The app is an SSR web server that reads documents from a demarkus world over
QUIC. It needs a world to read; it renders nothing on its own.

## Launch

Against the local soul, on a port that will not collide with anything the user
is already running:

```sh
PORT=8099 \
DEMARKUS_HOST=localhost:6309 \
DEMARKUS_INSECURE=true \
DEMARKUS_COOKIE_SECURE=false \
go run ./cmd/demarkus-library
```

Run it in the background and wait for the socket rather than sleeping:

```sh
for i in $(seq 1 40); do curl -sf -o /dev/null http://localhost:8099/ && break; sleep 1; done
```

Why each variable:

| variable | reason |
|---|---|
| `DEMARKUS_HOST` | defaults to `soul.demarkus.io`; the local world is on 6309 |
| `DEMARKUS_INSECURE` | the local world serves a self-signed certificate |
| `DEMARKUS_COOKIE_SECURE` | `false` only for plain-HTTP localhost |
| `PORT` | 8080 is the default and often taken |

Do not use `DEMARKUS_TRANSPORT=broker` for local work. Broker mode puts the
whole room behind an OAuth turnstile.

If no local world is listening, check for `demarkus-server` on 6309 before
assuming the app is broken. Its content root is `~/.demarkus/content`.

The librarian enables itself when it finds an API key and disables itself
quietly otherwise. Either is fine for rendering work.

## Find a URL worth opening

`/` redirects to `/t/u`, the universe floor. Trails are `/t/<world>/d/<path>`,
and panes chain with `/~/`:

```sh
curl -sS http://localhost:8099/t/u | grep -o 'href="/t/[^"]*"' | sort -u
```

Two overlays hang off query parameters on any trail: `?reader=<pane>` and
`?meta=<pane>`.

Not every link resolves. The world lists archived documents but refuses to serve
them, so a focused read of one answers 410 saying "archived"; a genuinely
missing document is still 404 "not found". An archived document behind the
focus renders as a gone tombstone rather than failing the trail.

## Rooms

`DEMARKUS_PANE_SCROLL` selects which room renders. It changes where the margin
lives, which matters when the margin is what you are trying to see.

- Default (`true`): the margin is summoned, so the escape row with the source,
  graph and map links sits behind `?meta=<pane>`.
- `false`: the margin docks beside the document, so the escape row renders
  inline with no extra click.

## Screenshot

Prefer the Chrome extension tools when the extension is connected. When it is
not, headless Chrome works:

```sh
timeout 60 "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --headless --disable-gpu --hide-scrollbars \
  --force-device-scale-factor=2 --window-size=1280,900 \
  --screenshot=out.png --virtual-time-budget=5000 \
  --user-data-dir=./chrome-profile \
  'http://localhost:8099/t/localhost:6309/d/index.md'
```

Three things that will bite:

- The process does not always exit after writing the file. Wrap it in
  `timeout` and check the file exists rather than trusting the exit code.
- `--user-data-dir` is required, and a fresh one per invocation avoids a lock
  fight with a previous run.
- Size the window to the region you want. Cropping afterwards is fiddlier.

To crop with `sips`, the offset must come before `-c` or the crop is taken
from the centre:

```sh
sips --cropOffset <y> <x> -c <height> <width> in.png --out out.png
```

**Look at the image.** A blank or half-rendered frame means the page did not
finish, not that the change is wrong.

## Gate and clean up

```sh
bash pre-commit.sh
helm unittest deploy/helm/demarkus-library
```

Never run `go build ./cmd/...` without `-o`; it drops a binary in the working
tree.

Stop the server when finished, and say so. Leaving it running serves stale
code after the next edit.
