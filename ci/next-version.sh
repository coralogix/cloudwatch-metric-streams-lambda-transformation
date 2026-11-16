#!/usr/bin/env bash
# Print the version to release for a commit on main, or nothing if it should not release.
#
#   - Already tagged: that tag's version. The publish job tags a version before uploading
#     anything, so a re-run of a failed release finishes the version it reserved.
#   - Already contained in a newer tag: nothing. A re-run of an old commit must not ship
#     old code under a new version.
#   - The merged PR has the skip-release label: nothing, unless the run was started manually.
#     Its changes ship with the next release.
#   - Otherwise: the newest v* tag bumped by the largest of the minor/major labels on any PR
#     merged since that tag, or the patch if there are none. Looking at every PR, not just
#     this one, keeps a bump whose own run was skipped or replaced by a later merge.
#
# Needs full history and tags (actions/checkout with fetch-depth: 0), and GH_TOKEN plus
# GITHUB_REPOSITORY to read PR labels. A failed label lookup fails the script: guessing could
# release a skip-release merge or understate the bump.
# Usage: next-version.sh <commit>
set -euo pipefail

sha=$(git rev-parse "$1^{commit}")

labels_of() {
  gh api "repos/$GITHUB_REPOSITORY/commits/$1/pulls" --jq '.[0].labels[]?.name'
}

tags=$(git tag --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V || true)

for t in $tags; do
  if [ "$(git rev-parse "$t^{commit}")" = "$sha" ]; then
    echo "${t#v}"
    exit 0
  fi
done

# A manual run is an explicit request to release, so it ignores skip-release.
current=$(labels_of "$sha") || { echo "::error::Can't read the labels of $sha's PR." >&2; exit 1; }
if [ "${GITHUB_EVENT_NAME:-}" != workflow_dispatch ] && grep -qx skip-release <<<"$current"; then
  echo "skip-release label set; not releasing." >&2
  exit 0
fi

newest=$(tail -1 <<<"$tags")
if [ -z "$newest" ]; then
  echo "0.1.0"
  exit 0
fi

if git merge-base --is-ancestor "$sha" "$newest"; then
  echo "$sha is already part of $newest; not releasing." >&2
  exit 0
fi

labels=""
for c in $(git rev-list "$newest..$sha"); do
  l=$(labels_of "$c") || { echo "::error::Can't read the labels of $c's PR." >&2; exit 1; }
  labels+="$l"$'\n'
done

IFS=. read -r major minor patch <<<"${newest#v}"
if grep -qx major <<<"$labels"; then
  echo "$((major + 1)).0.0"
elif grep -qx minor <<<"$labels"; then
  echo "$major.$((minor + 1)).0"
else
  echo "$major.$minor.$((patch + 1))"
fi
