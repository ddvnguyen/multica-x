#!/usr/bin/env bash
# scripts/ci-green-run.test.sh — the green-ci gate runs the exact gh+jq
# pipeline this script exercises (M-556: the safety test mocks gh, so a bad
# gh flag there is invisible).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/ci-green-run.sh"
SANDBOX="$(mktemp -d "$REPO_ROOT/.ci-green-run-test.XXXXXX")"
trap 'rm -rf "$SANDBOX"' EXIT
STUB_BIN="$SANDBOX/bin"
mkdir -p "$STUB_BIN"

# The mock gh records its argv, rejects the flags real gh chokes on, and
# answers --json headSha the way the API does.
cat > "$STUB_BIN/gh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >> "$STUB_LOG"
for arg in "$@"; do
  case "$arg" in
    --jq|--arg) printf 'mock gh: rejected flag %s\n' "$arg" >&2; exit 90 ;;
  esac
done
printf '[{"headSha":"%s"},{"headSha":"cccccccccccccccccccccccccccccccccccccccc"}]\n' "$GH_SHA"
EOF
chmod 755 "$STUB_BIN/gh"
export PATH="$STUB_BIN:$PATH"
export STUB_LOG="$SANDBOX/gh-args.log"
export GH_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

# A SHA with a green push run passes and names the short SHA.
: > "$STUB_LOG"
out="$(bash "$SCRIPT" owner/repo CI multica-moct "$GH_SHA")"
[[ "$out" == *"green CI push run confirmed for aaaaaaaaa"* ]]
grep -q -- '--json' "$STUB_LOG"
grep -q 'headSha' "$STUB_LOG"
! grep -q -- '--jq' "$STUB_LOG"

# A SHA with no green push run fails.
if bash "$SCRIPT" owner/repo CI multica-moct bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb >/dev/null 2>&1; then
  echo "expected failure for a SHA with no green run" >&2; exit 1
fi

# A non-SHA (branch/tag dispatch) passes silently: only SHA releases gate.
: > "$STUB_LOG"
out="$(bash "$SCRIPT" owner/repo CI multica-moct multica-moct)"
[[ "$out" == *"not a SHA; nothing to gate"* ]]
test ! -s "$STUB_LOG"

# Wrong arity is a usage error.
if bash "$SCRIPT" owner/repo >/dev/null 2>&1; then
  echo "expected usage failure" >&2; exit 1
fi

printf 'ci-green-run tests passed\n'
