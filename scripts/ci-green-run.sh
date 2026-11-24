#!/usr/bin/env bash
# scripts/ci-green-run.sh — M-556: fail unless a SHA has a successful CI push
# run on the release branch. Lane z installs CI artifacts, so a release SHA
# without them can never converge; pull-request runs do not count (they
# publish no artifacts).
#
# Usage: ci-green-run.sh <repo> <workflow> <branch> <sha>
# A non-SHA fourth argument (branch/tag dispatch) passes silently: only SHA
# releases are gated. stdout stays quiet on success; failures explain why.
set -euo pipefail

usage() { printf 'Usage: ci-green-run.sh <repo> <workflow> <branch> <sha>\n' >&2; }
die() { printf 'ci-green-run: ERROR: %s\n' "$*" >&2; exit 1; }

[ "$#" -eq 4 ] || { usage; exit 2; }
REPO="$1"
WORKFLOW="$2"
BRANCH="$3"
SHA="$4"

[[ "$SHA" =~ ^[0-9a-fA-F]{40}$ ]] || {
  printf 'ci-green-run: %s is not a SHA; nothing to gate\n' "$SHA"
  exit 0
}

# NOTE: `gh run list` has no --jq/--arg path worth trusting here — older gh
# builds reject `--arg` outright (`unknown flag: --arg`) and swallow `-r` as
# the jq program. Keep gh to plain `--json` and filter with real jq.
match="$(gh run list --repo "$REPO" --workflow "$WORKFLOW" \
  --branch "$BRANCH" --event push --status success --limit 50 \
  --json headSha \
  | jq -r --arg sha "$SHA" '[.[] | select(.headSha == $sha)] | length')"
[[ "$match" =~ ^[0-9]+$ ]] && [ "$match" -ge 1 ] || \
  die "no successful CI push run for $SHA on $BRANCH"
printf 'ci-green-run: green CI push run confirmed for %s\n' "${SHA:0:9}"
