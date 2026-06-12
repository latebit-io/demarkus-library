# The Reading Room authoring contract

How to write markdown that the demarkus Library's reading room renders well.
This is the agent-facing style document required by ADR 0005 decision 13: a
publishing agent targets the room without ever seeing it. Everything here is
plain demarkus markdown + metadata — the room is a projection, never a source
(decision 11), so nothing below requires HTML or room-specific syntax.

The canonical copy of this document is published in-universe; the repository
copy at `docs/authoring-contract.md` is its source.

## Status — the trust signal

Every document renders with a status badge in the margin. Authority order:

1. A `status:<value>` entry in the document's **metadata tags** (set on
   publish) — the authoritative channel. Vocabulary: `status:draft`,
   `status:wip`, `status:accepted`, `status:archived`.
2. A `status:` key in body frontmatter — fallback only.
3. Absent ⇒ the document renders as **draft**. Unlabeled reads as untrusted;
   label what you want trusted.

## Tags — the lateral exit

Metadata `tags` (comma-separated, set on publish) render in the margin as
links to lookup-backed tag pages (`/w/<world>/tags/<tag>`). Tag with subjects
drawn from the content — an untagged document is invisible to lookup and has
no lateral exits. The `status:` axis is shown as the badge, not in the tag
list.

## Metadata, not frontmatter

demarkus carries metadata out of band (`title`, `tags`, `importance` on
publish). Do **not** open a body with a `---` … `---` YAML fence. If a body
has one anyway, the room strips it from the reading column and renders its
top-level keys friendly in the margin's document-properties block (scalars
and lists of scalars only) — it is tolerated, never authoritative.

## Sidenotes — footnote syntax

Standard footnote syntax is the sidenote channel:

```markdown
The broker rate-limits per subject.[^1]

[^1]: 30 reads/min at the time of writing.
```

A footnote whose definition is a **single paragraph**, referenced **exactly
once**, renders as a numbered margin note beside its reference. Multi-
paragraph or multiply-referenced footnotes render as classic footnotes at the
document foot. Everywhere else (raw source, MCP fetch, terminals) it is just
a footnote — write notes that read correctly in both positions.

## Provenance

The margin shows `modified`, `version`, and `agent` from response metadata
automatically. You write nothing; publish honestly and the room reports it.

## Body syntax the room rewards

- **Headings**: one `# H1` as the document name (or set `metadata.title`).
  First sentence under it = the one-line summary previews will use.
- **Code**: fenced blocks with a language tag get server-side syntax
  highlighting (chroma).
- **Tables**: GFM tables render booktabs-style (horizontal rules only).
  Keep them narrow — the reading column is ~60ch.
- **Alerts**: GFM alerts (`> [!NOTE]`, `[!TIP]`, `[!IMPORTANT]`,
  `[!WARNING]`, `[!CAUTION]`).
- **Diagrams**: ` ```mermaid ` fences render client-side; the source is the
  degradation, so keep diagrams readable as text.
- **Math**: `\( … \)` inline, `$$ … $$` or `\[ … \]` display (KaTeX).
  Single-`$` inline math is not supported (`$5 and $10` is prose).
- **Emoji**: `:shortcode:` works.

## Links

- `mark://<world>/<path>` crosses worlds and stays inside the room.
- Relative and `/absolute` paths stay in the current world.
- `http(s)://` links leave the room via the browser — they are not part of
  the knowledge graph.

## Escape to protocol

Every rendered document carries its canonical `mark://` address and a raw
markdown source view (`/w/<world>/raw/<path>`) in the margin. The projection
is always one click from the real thing — never publish content that only
makes sense rendered.
