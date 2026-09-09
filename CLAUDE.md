# CLAUDE.md

Project context (architecture, patterns, roadmap, journals) lives in the
demarkus-soul MCP server under `/demarkus-library/`. Shared, org-wide
standards live in the knowledge system. This file carries only what must be
in context every session.

## Preflight

A SessionStart hook (`.claude/session-start.sh`) prints this list at startup and
reports whether the local golangci-lint matches the version CI pins.

1. `mark_fetch /demarkus-library/index.md` (soul) — the hub.
2. `mark_fetch /demarkus-library/roadmap.md` (soul) — living status; the tail
   is current, the early sections are frozen history.
3. `mark_fetch mark://latebit/projects/demarkus/guidelines.md` (knowledge) —
   hard code-quality rules, summarized below. Read before writing code.
4. `mark_fetch mark://latebit/projects/demarkus/patterns.md` (knowledge) —
   engineering patterns shared across the latebit Go repos.
5. As needed: `/demarkus-library/adr/`, `/debugging.md`, `/debt.md`,
   `/patterns.md`, `/plans/`.

## Hard rules (from the demarkus coding guidelines)

- Never return more than two values unless one is `error`. Several values that
  belong together get a result struct with named fields.
- More than four parameters means a parameter struct.
- Do not duplicate logic. Extract to the package that owns it, update every
  caller, delete the copies, test once at the owning layer.
- Handle every error where it happens or return it wrapped with the operation
  and identifier. A deliberate best-effort path says so in a comment.
- Names state role and meaning. Single letters only in short loops and
  closures.
- Comments are one to three lines and say why, not what. Tighten verbose
  comments in any block you touch.
- Smallest correct increment. No abstraction for a single hypothetical use.

## SOLID in this codebase

- **Single responsibility**: one concern per file. The fence codec, logo
  validation, and the branding cache are separate files even though they
  serve one feature.
- **Open/closed**: extend through the ports and the manifest, not by widening
  a handler's switch. Adding a branding surface should not edit the renderer.
- **Liskov**: a wrapper must behave like what it wraps. The static overlay
  falls back to the embedded FS on every error the embedded FS would not
  raise.
- **Interface segregation**: depend on the narrowest interface that does the
  job. `WorldBrands` takes a `rawReader`, not the whole `port.Reader`.
- **Dependency inversion**: adapters depend on ports; the composition root in
  `cmd/` is the only place concrete types meet. See ADR 0002.

## Architecture

Hexagonal (ADR 0002): `internal/core/{domain,port,service}` knows nothing of
Echo, QUIC, or goldmark; adapters live under `internal/adapter/{inbound,outbound}`;
`cmd/demarkus-library` wires them. SSR-first, htmx-hard, no JSON (ADR 0003).
Branding layers: files for the room, in-world documents for a world (ADR 0008).

## Build and test

```sh
bash pre-commit.sh                       # fmt, vet, golangci-lint, build, test
go test ./...
helm unittest deploy/helm/demarkus-library
```

Run `pre-commit.sh` after every implementation. Never `go build ./cmd/...`
without `-o`; it drops a binary in the working tree.

## Working agreement

- The user commits and pushes. Never run `git commit` or `git push`.
- Work on a feature branch, and roll related work into one branch and one
  release rather than a string of small PRs.
- Record decisions as ADRs in the soul under `/demarkus-library/adr/`, and
  session notes in `/demarkus-library/journal/<YYYY-MM-DD>.md`.
