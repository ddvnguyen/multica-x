#!/usr/bin/env bash
# Build immutable, not-yet-used image tags for the main Multica deployment.
set -euo pipefail

die() { printf '[deploy-main-build] ERROR: %s\n' "$*" >&2; exit 1; }

if [ "$#" -ne 5 ]; then
  printf 'Usage: deploy-main-build.sh <image-tag> <commit-sha> <backend-repo> <web-repo> <stack-env>\n' >&2
  exit 2
fi

IMAGE_TAG="$1"
COMMIT="$2"
BACKEND_REPO="$3"
WEB_REPO="$4"
STACK_ENV="$5"

[[ "$IMAGE_TAG" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]] || die "invalid image tag"
[[ "$COMMIT" =~ ^[0-9a-fA-F]{40}$ ]] || die "commit must be a full 40-character SHA"
[[ "$BACKEND_REPO" =~ ^[A-Za-z0-9./:_-]+$ ]] || die "invalid backend image repository"
[[ "$WEB_REPO" =~ ^[A-Za-z0-9./:_-]+$ ]] || die "invalid web image repository"
[ -f "$STACK_ENV" ] || die "stack environment file not found: $STACK_ENV"

read_env_value() {
  awk -F= -v key="$1" '
    $1 == key {
      sub(/^[^=]*=/, "")
      print
      found++
    }
    END {if (found != 1) exit 2}
  ' "$STACK_ENV"
}

current_tag="$(read_env_value MULTICA_IMAGE_TAG)" || die "MULTICA_IMAGE_TAG is missing or duplicated in $STACK_ENV"
current_tag="${current_tag%\"}"; current_tag="${current_tag#\"}"
current_tag="${current_tag%\'}"; current_tag="${current_tag#\'}"
[ -n "$current_tag" ] || die "current MULTICA_IMAGE_TAG is empty"
[ "$IMAGE_TAG" != "$current_tag" ] || die "image tag is already active; choose a new immutable tag"

# Check both targets before building either image. Existing tags are immutable:
# replacing one would make a later rollback select the replacement image.
for image in "$BACKEND_REPO:$IMAGE_TAG" "$WEB_REPO:$IMAGE_TAG"; do
  if podman image exists "$image"; then
    die "refusing to overwrite existing image tag: $image"
  else
    image_exists_status=$?
    [ "$image_exists_status" -eq 1 ] || die "could not check whether image tag exists: $image"
  fi
done

podman build -f Dockerfile \
  --build-arg VERSION="$IMAGE_TAG" --build-arg COMMIT="$COMMIT" \
  --label "org.opencontainers.image.revision=$COMMIT" \
  --label "org.opencontainers.image.version=$IMAGE_TAG" \
  -t "$BACKEND_REPO:$IMAGE_TAG" .
podman build -f Dockerfile.web \
  --build-arg NEXT_PUBLIC_APP_VERSION="$IMAGE_TAG" \
  --label "org.opencontainers.image.revision=$COMMIT" \
  --label "org.opencontainers.image.version=$IMAGE_TAG" \
  -t "$WEB_REPO:$IMAGE_TAG" .
