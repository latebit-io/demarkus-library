# Trail URLs — the shared reading-context format

A trail URL serializes a reading context: an ordered path of panes,
root → focus, exactly as the Reading Room lays it out on screen. It is the
context object humans and agents exchange (ADR 0005 decision 14): an agent
mints a trail to hand a human its reasoning chain, reconstructed pane by
pane; an agent parses a human's trail to load what they were reading. The
format is stable and constructible — nothing in it requires the room.

The canonical copy of this document is published in-universe; the repository
copy at `docs/trail-format.md` (repo `latebit-io/demarkus-library`) is its
source.

## Format

```
/t/<pane>/~/<pane>/~/<pane>?focus=<i>
```

- **Pane chunk** — the tail of the pane's standalone `/w/` route:
  - document: `<world>/d/<path>` — e.g. `soul.demarkus.io/d/adr/0005.md`
  - listing: `<world>/d/<dir>/` (trailing slash) — e.g. `root/d/plans/`
  - tag page: `<world>/tags/<tag>` — e.g. `root/tags/architecture`
  - the floor: the single segment `u` — the universe view (pane zero), no
    world part because the floor IS the whole universe. `/` redirects to
    `/t/u`.
- **`~`** — the reserved separator: a path segment that is exactly `~`
  between consecutive pane chunks.
- **`focus`** — 0-based index of the focused pane (full render + margin +
  live fetch). Omitted ⇒ the last pane. Out of range ⇒ clamped.
- **Worlds** are knowledge-system names (`root`) or demarkus hosts
  (`soul.demarkus.io`, `wiki.example.org:6310`). Paths keep raw slashes —
  no percent-encoded `/`.
- **Depth cap: 10 panes.** URLs with more are rejected (400). When a click
  would push past the cap, the room drops the oldest pane.

Example — the floor, a tag page, and a document, attention on the tag page:

```
/t/u/~/root/tags/adr/~/root/d/adr/0005.md?focus=1
```

## Semantics (what the room guarantees)

- The trail is a single reasoning path, not a browsing history. A link
  clicked in pane N discards panes right of N and appends the target,
  focusing it. A target already on the path is focused, never duplicated.
- The whole canvas renders from the URL alone — server state, no client
  state. Sharing the URL shares the exact reconstruction.
- Every link the room renders already carries its post-click trail URL, so
  fetching a trail page and following hrefs is itself a valid way for an
  agent to walk the space of next states.
- The floor pane renders the same data an agent reads via `mark_worlds`
  plus per-world `mark_lookup` with the match-all query `*` (importance
  order) — the projection adds layout, never information.

## Minting (agent → human)

Concatenate route tails with `/~/`, set `focus` if not last. Rules:

- ≤ 10 panes; each pane's world+path must read (an unreadable unfocused
  pane renders as a tombstone, the trail survives; an unreadable focused
  pane is the page's error).
- Order IS the argument: pane 0 is where the reasoning starts, focus is
  where you want the reader's attention to land. Starting at `u` hands the
  reader the map before the path.
- Don't percent-encode slashes; do percent-encode characters that are not
  valid in a URL path segment.

## Parsing (human → agent)

Split the path after `/t/` on `/~/`; each chunk is `<world>/<kind>/<value>`
(kind `d` ⇒ document path `/<value>`, kind `tags` ⇒ tag page) or the bare
`u` floor chunk (a single token, no world/kind/value — the universe view).
Read the documents over mark:// (`mark://<world>/<path>`) in order — that
ordered list, plus the focus index, is the reader's current context.
