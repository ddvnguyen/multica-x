#!/usr/bin/env bash
# Protect the current release and existing immutable build tags before building.
set -euo pipefail
if [ "$#" != 5 ]; then
  echo "usage: deploy-main-image-guard.sh target-tag current-tag build-or-staged backend-repo web-repo" >&2
  exit 2
fi
target_tag="$1"
current_tag="$2"
image_source="$3"
if [ -z "$current_tag" ] || [ "$target_tag" = "$current_tag" ]; then
  echo "target image tag must differ from the current release tag" >&2
  exit 1
fi
case "$image_source" in
  build)
    for repo in "$4" "$5"; do
      if podman image exists "$repo:$target_tag"; then
        echo "refusing to overwrite existing image: $repo:$target_tag" >&2
        exit 1
      fi
    done
    ;;
  staged) ;;
  *) echo "invalid image source" >&2; exit 2 ;;
esac
