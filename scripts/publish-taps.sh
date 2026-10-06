#!/usr/bin/env bash
# Publishes a release's Homebrew formula and Scoop manifest to the tap and bucket repositories.
#
#   scripts/publish-taps.sh v0.5.0
#
# Uses your `gh` login (no token to manage). The release workflow does the same automatically when
# the PACKAGING_TOKEN secret is set; this script is for doing it by hand.
set -euo pipefail

tag="${1:?usage: publish-taps.sh <tag, e.g. v0.5.0>}"
owner="OriginalDaniel02"
here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

gh release download "$tag" -R "$owner/dbguard" -p checksums.txt -D "$work"
bash "$here/package-managers.sh" "$tag" "$work/checksums.txt" "$work/out"

publish() { # repo source dest
  local repo="$1" src="$2" dest="$3"
  gh repo clone "$owner/$repo" "$work/$repo" -- --quiet
  mkdir -p "$work/$repo/$(dirname "$dest")"
  cp "$src" "$work/$repo/$dest"
  (
    cd "$work/$repo"
    git add -A
    if git diff --cached --quiet; then
      echo "$repo: already up to date for $tag"
    else
      git commit -q -m "dbguard $tag"
      git push -q
      echo "$repo: published $tag"
    fi
  )
}

publish homebrew-tap "$work/out/dbguard.rb" Formula/dbguard.rb
publish scoop-bucket "$work/out/dbguard.json" bucket/dbguard.json
