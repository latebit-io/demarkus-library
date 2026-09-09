#!/bin/bash
# SessionStart hook: put this repo's standards in front of every session and
# say up front whether the local lint gate can actually run.
set -uo pipefail

root="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
pin=$(grep -oE '[0-9]+\.[0-9]+\.[0-9]+' "$root/pre-commit.sh" 2>/dev/null | head -1)
have=$(golangci-lint version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
if [ -z "${pin:-}" ]; then
  lint="could not read the golangci-lint pin from pre-commit.sh"
elif [ -z "$have" ]; then
  lint="golangci-lint is NOT installed; pre-commit.sh will fail (CI pins $pin)"
elif [ "$have" != "$pin" ]; then
  lint="golangci-lint $have is installed but CI pins $pin; pre-commit.sh will refuse to run"
else
  lint="golangci-lint $have matches the CI pin"
fi

ctx=$(cat <<CTX
demarkus-library standards bootstrap (SessionStart hook).

Before writing code, read the shared rules: mark://latebit/projects/demarkus/guidelines.md
and mark://latebit/projects/demarkus/patterns.md in the knowledge system. Project
context is in the soul at /demarkus-library/ (index.md, roadmap.md tail, adr/, debt.md).
CLAUDE.md carries the same hard rules inline if MCP is unavailable.

Load-bearing rules: at most two return values unless one is error; a parameter
struct past four arguments; no duplicated logic; every error handled or wrapped;
comments one to three lines saying why; narrow interfaces; smallest correct increment.

Gate before handing work back: bash pre-commit.sh, then helm unittest deploy/helm/demarkus-library.
Local tooling: $lint

Answer style is terse: minimize text, keep technical precision, one line per
item, no recaps or restatements. Under 10 lines unless detail is required.

The user commits and pushes. Never run git commit or git push.
CTX
)

jq -n --arg c "$ctx" '{hookSpecificOutput:{hookEventName:"SessionStart",additionalContext:$c}}'
