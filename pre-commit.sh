#!/bin/bash
set -e

# Adapted from demarkus/pre-commit.sh. demarkus is multi-module and drives the
# checks through `make`; demarkus-library is a single module, so the steps are
# inlined here. Run before wrapping up a phase or task.

echo "Running code formatting and linting..."
go fmt ./...
go vet ./...

# Pin to the version CI runs (.github/workflows/ci.yml: golangci-lint-action
# `version:`). Lint rules and output differ between releases, so a mismatched
# local binary can pass this hook while CI fails — defeating the gate.
GOLANGCI_LINT_VERSION="2.9.0"

if ! command -v golangci-lint &>/dev/null; then
  echo "Error: golangci-lint is not installed."
  echo "Install v${GOLANGCI_LINT_VERSION}: https://golangci-lint.run/welcome/install/"
  exit 1
fi

have=$(golangci-lint version | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)
if [ "$have" != "$GOLANGCI_LINT_VERSION" ]; then
  echo "Error: golangci-lint $have found, but CI pins v${GOLANGCI_LINT_VERSION}."
  echo "Install the matching version: https://golangci-lint.run/welcome/install/"
  exit 1
fi

echo "Linting..."
golangci-lint run ./...

echo "Building..."
go build ./...

echo "Testing..."
go test ./...

echo "✓ All checks passed"
