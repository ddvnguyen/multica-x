#!/usr/bin/env bash
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
GUARD_SCRIPT="$SCRIPT_DIR/go-test-with-agent-cli-guard.sh"

usage() {
  echo "usage: $0 [--race] [--only regular|agent|db|pkg]" >&2
}

# The suite is `go test` invocations split by group so CI can give each its
# own runner (M-161): `db` is everything outside pkg/ (cmd/ and internal/,
# whose tests need Postgres/Redis), `pkg` is server/pkg/** minus pkg/agent
# (pure packages, no services), `agent` is pkg/agent throttled (see below),
# `regular` is db+pkg in one invocation for local `check.sh` callers. The
# default still runs all groups for `make test`.
go_test_args=(test)
only=all
while [ "$#" -gt 0 ]; do
  case "$1" in
    --race)
      go_test_args+=(-race)
      shift
      ;;
    --only)
      case "${2:-}" in
        regular|agent|db|pkg) only=$2 ;;
        *)
          usage
          exit 2
          ;;
      esac
      shift 2
      ;;
    *)
      usage
      exit 2
      ;;
  esac
done

cd "$REPO_ROOT/server"

if [ "$only" != agent ]; then
  packages=$(go list ./...)
  selected=()
  for package in $packages; do
    # pkg/agent always runs in its own throttled invocation.
    case "$package" in
      */pkg/agent|*/pkg/agent/*) continue ;;
    esac
    case "$only" in
      db)
        # Everything outside pkg/: cmd/, internal/, module root.
        case "$package" in */pkg/*) continue ;; esac
        ;;
      pkg)
        case "$package" in */pkg/*) ;; *) continue ;; esac
        ;;
    esac
    selected+=("$package")
  done
  if [ "${#selected[@]}" -eq 0 ]; then
    # Never fall through to a bare `go test` (which would test the current
    # directory instead of the requested group).
    echo "test-go.sh --only $only selected no packages" >&2
    exit 2
  fi
  "$GUARD_SCRIPT" -- go "${go_test_args[@]}" "${selected[@]}"
fi

if [ "$only" = all ] || [ "$only" = agent ]; then
  # Subprocess-backed agent tests have hard deadlines. Limit both package and
  # within-package parallelism so race builds do not starve their parent loops.
  "$GUARD_SCRIPT" -- go "${go_test_args[@]}" -p 2 -parallel 2 ./pkg/agent/...
fi
