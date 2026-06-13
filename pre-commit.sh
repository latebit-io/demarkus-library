#!/bin/bash
set -e

# Adapted from demarkus/pre-commit.sh. demarkus is multi-module and drives the
# checks through `make`; demarkus-library is a single module, so the steps are
# inlined here. Run before wrapping up a phase or task.

echo "Running code formatting and linting..."
go fmt ./...
go vet ./...

if ! command -v golangci-lint &>/dev/null; then
  echo "Error: golangci-lint is not installed."
  echo "Install it: https://golangci-lint.run/welcome/install/"
  exit 1
fi

echo "Linting..."
golangci-lint run ./...

echo "Building..."
go build ./...

echo "Testing..."
go test ./...

echo "✓ All checks passed"
