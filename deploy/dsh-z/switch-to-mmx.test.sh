#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$REPO_ROOT/deploy/dsh-z/switch-to-mmx.sh"
# dry-run should not require container; but quiescence queries need podman. Stub podman.
SANDBOX="$(mktemp -d "$REPO_ROOT/.switch-test.XXXXXX")"
trap 'rm -rf "$SANDBOX"' EXIT
STUB="$SANDBOX/bin"; mkdir -p "$STUB"
cat > "$STUB/podman" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  *inspect*) printf '{"State":{"Status":"running"}}\n' ;;
  *psql*) printf '0\n' ;;
  *) printf '0\n' ;;
esac
EOF
chmod +x "$STUB/podman"
export PATH="$STUB:$PATH"
export LANE_ROOT="$SANDBOX/lane"
export LANE_RUNTIME_ENV="$SANDBOX/lane-runtime.env"
mkdir -p "$LANE_ROOT/multica-be" "$LANE_ROOT/data"
touch "$LANE_ROOT/multica-be/env"
export MULTICA_Z_DB_CONTAINER=infra-postgres
export MULTICA_Z_DB_NAME=multica_z
export MULTICA_MMX_PG17_CONTAINER=multica-pg17-mmx
export MULTICA_MMX_PG17_PORT=55434
export MULTICA_MMX_PG17_PW_FILE="$SANDBOX/pw"
printf 'secret' > "$SANDBOX/pw"
export MULTICA_MMX_PG17_UPLOADS="$SANDBOX/uploads"
export MULTICA_MMX_SWITCH_BACKUP_ROOT="$SANDBOX/backups"
# dry run
out="$(bash "$SCRIPT" --dry-run)"
echo "$out" | grep -q "dry-run complete" || { echo "dry-run failed: $out"; exit 1; }
echo "PASS"
