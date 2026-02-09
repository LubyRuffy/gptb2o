#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(
  cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." >/dev/null 2>&1
  pwd
)"
cd "$ROOT_DIR"

FIX="${FIX:-0}"
RACE="${RACE:-0}"

echo "[go_quality_check] root=$ROOT_DIR"

gofmt_list="$(gofmt -l . | tr '\n' ' ' | sed -e 's/[[:space:]]*$//')"
if [[ -n "${gofmt_list}" ]]; then
  if [[ "$FIX" == "1" ]]; then
    echo "[gofmt] fixing: ${gofmt_list}"
    # shellcheck disable=SC2086
    gofmt -w ${gofmt_list}
  else
    echo "[gofmt] files not formatted:"
    # Re-run with line breaks for readability
    gofmt -l .
    echo "Hint: run FIX=1 ./scripts/go_quality_check.sh"
    exit 1
  fi
else
  echo "[gofmt] ok"
fi

echo "[go test] ./..."
go test ./...

if [[ "$RACE" == "1" ]]; then
  echo "[go test -race] ./..."
  go test -race ./...
fi

echo "[go vet] ./..."
go vet ./...

if command -v staticcheck >/dev/null 2>&1; then
  echo "[staticcheck] ./..."
  staticcheck ./...
else
  echo "[staticcheck] skipped (not installed)"
fi

if command -v golangci-lint >/dev/null 2>&1; then
  echo "[golangci-lint] ./..."
  golangci-lint run ./...
else
  echo "[golangci-lint] skipped (not installed)"
fi

echo "[go_quality_check] done"

