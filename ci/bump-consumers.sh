#!/usr/bin/env bash
# shellcheck disable=SC2329  # notes_* are called through bump's 4th argument
# Open or update one rolling pull request per consumer repository that moves one of its two
# default processor versions to <version>:
#   commercial  called by publish after a release;
#   govcloud    called by publish-govcloud once <version> has passed the GovCloud smoke test.
# Both normally land in the same PR minutes apart, so the defaults stay equal; a
# commercial-only fix or a GovCloud hold moves only one of them.
#
#   GH_TOKEN=... ci/bump-consumers.sh <commercial|govcloud> <version>
#
# Each default is the line marked "bump-consumers: <channel>". For each consumer:
#   - never downgrades: each channel takes the newest of master, the open bump PR and
#     <version>, so a re-run of an older release can't move a default back;
#   - rebuilds the bump branch from current master every time, carrying over the other
#     channel's pending value from the open PR, and changes only the two version lines and
#     the release notes, so unrelated changes are never reverted. Re-running a version
#     refreshes an open PR after master has moved (new release headings);
#   - pushes with a lease on the branch commit it read, and redoes the whole update when
#     another run pushed first, so concurrent runs can't overwrite each other;
#   - follows the consumer's release rules (CHANGELOG entry, template SemanticVersion, or
#     the next module version that semantic-release will cut).
#
# Environment:
#   GH_TOKEN         token that can push branches and open PRs in the consumer repositories
#   CONSUMER_REMOTE  clone URL prefix (default https://x-access-token:$GH_TOKEN@github.com/)
#   BUMP_PR_DRAFT    true to open new PRs as drafts
set -euo pipefail
shopt -s inherit_errexit 2>/dev/null || true # bash 4.4+: errexit inside $(...)

channel=${1:?usage: bump-consumers.sh <commercial|govcloud> <version>}
version=${2:?usage: bump-consumers.sh <commercial|govcloud> <version>}
case "$channel" in commercial|govcloud) ;; *) echo "::error::channel must be commercial or govcloud, not '$channel'." >&2; exit 2 ;; esac
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "::error::'$version' is not a version like 0.1.20." >&2; exit 2; }
remote=${CONSUMER_REMOTE:-https://x-access-token:${GH_TOKEN:?GH_TOKEN is required}@github.com/}
branch=bump/firehose-processor
release_url=https://github.com/coralogix/cloudwatch-metric-streams-lambda-transformation/releases/tag/v$version
work=$(mktemp -d); trap 'rm -rf "$work"' EXIT

newer() { [ "$1" != "$2" ] && [ "$(printf '%s\n' "$1" "$2" | sort -V | tail -1)" = "$1" ]; } # newer A B: A > B

# A default is the version on the line marked "bump-consumers: <channel>".
read_default() { # read_default <file> <channel>
  awk -v m="bump-consumers: $2" 'index($0, m) && match($0, /[0-9]+\.[0-9]+\.[0-9]+/) {print substr($0, RSTART, RLENGTH); exit}' "$1"
}
write_default() { # write_default <file> <channel> <version>
  awk -v m="bump-consumers: $2" -v v="$3" 'index($0, m) {sub(/[0-9]+\.[0-9]+\.[0-9]+/, v)} {print}' "$1" > "$1.new" && mv "$1.new" "$1"
}
max() { printf '%s\n' "$@" | sort -V | tail -1; }
# What the PR changes, for titles and release notes: "commercial 0.1.21, GovCloud 0.1.21".
summary() { # summary <commercial> <govcloud> (empty = unchanged)
  if [ -n "$1" ] && [ "$1" = "$2" ]; then echo "$1 (commercial and GovCloud)"
  else echo "${1:+$1 (commercial)}${1:+${2:+, }}${2:+$2 (GovCloud)}"; fi
}

# --- consumer-specific release notes -----------------------------------------------------
notes_cfn() { # in the repo root; prints the PR title
  local dir=aws-integrations/firehose-metrics semver next
  semver=$(sed -n 's/^  SemanticVersion: \([0-9.]*\)$/\1/p' "$dir/template.yaml")
  [ -n "$semver" ] || { echo "::error::no SemanticVersion in $dir/template.yaml" >&2; return 1; }
  next=${semver%.*}.$(( ${semver##*.} + 1 ))
  sed -i.bak "s/^  SemanticVersion: $semver$/  SemanticVersion: $next/" "$dir/template.yaml" && rm "$dir/template.yaml.bak"
  awk -v h="### $next / $(date -u '+%-d %b %Y')" -v v="$changes" '
    {print}
    !done && /^<!-- \* \[Update\/Bug fix\]/ {
      print ""; print h
      print "* [Update] Default `LambdaProcessorVersion`: processor " v ". Stacks with `LambdaProcessorVersion` empty update the processor in place; stacks with an explicit version keep it."
      done=1
    }' "$dir/CHANGELOG.md" > "$dir/CHANGELOG.md.new" && mv "$dir/CHANGELOG.md.new" "$dir/CHANGELOG.md"
  grep -q "^### $next / " "$dir/CHANGELOG.md" || { echo "::error::could not add the CHANGELOG entry" >&2; return 1; }
  echo "firehose-metrics: default processor $changes"
}
notes_tf() {
  local latest next
  latest=$(git tag -l 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1)
  [ -n "$latest" ] || { echo "::error::no release tag" >&2; return 1; }
  next=${latest%.*}.$(( ${latest##*.} + 1 )) # a fix: title is a patch release
  awk -v h="## $next" -v v="$changes" '
    !done && /^## / {
      print h; print ""
      print "#### **firehose-metrics**"; print "### 🧰 Bug fixes 🧰"
      print "- Default `lambda_processor_version`: processor " v ". Deployments with no explicit `lambda_processor_version` update the processor on the next apply; explicit versions are kept."
      print ""; done=1
    }
    {print}' CHANGELOG.md > CHANGELOG.md.new && mv CHANGELOG.md.new CHANGELOG.md
  grep -q "^## $next$" CHANGELOG.md || { echo "::error::could not add the CHANGELOG entry" >&2; return 1; }
  echo "fix: firehose-metrics: default processor $changes"
}

# --- one consumer ----------------------------------------------------------------------
bump() { # bump <owner/repo> <base> <file> <notes function>
  local repo=$1 attempt rc
  for attempt in 1 2 3; do
    # A subshell with errexit on: calling update from a condition would switch errexit off
    # inside it, letting a failed command pass unnoticed.
    set +e; ( set -e; update "$@" ); rc=$?; set -e
    [ "$rc" -eq 75 ] || return "$rc"
    echo "$repo: another run updated the bump branch first; retrying ($attempt)." >&2
  done
  echo "::error::$repo: the bump branch kept changing; giving up." >&2; return 1
}

# One read/merge/push. Returns 75 when the push lost the race, so bump() retries.
update() { # update <owner/repo> <base> <file> <notes function>
  local repo=$1 base=$2 file=$3 notes=$4 dir pr old title ch draft
  # master_*, pending_* and target_* hold each channel's value: on master, in the PR to
  # push, and what the PR changes (empty = unchanged). Plain variables for bash 3.
  local master_commercial master_govcloud pending_commercial pending_govcloud target_commercial target_govcloud
  dir=$(mktemp -d "$work/${repo#*/}.XXXX")
  git clone -q --no-tags --branch "$base" "$remote$repo.git" "$dir"
  cd "$dir"
  git fetch -q --tags origin
  master_commercial=$(read_default "$file" commercial)
  master_govcloud=$(read_default "$file" govcloud)
  [ -n "$master_commercial" ] && [ -n "$master_govcloud" ] \
    || { echo "::error::$repo: $file needs lines marked 'bump-consumers: commercial' and 'bump-consumers: govcloud'" >&2; return 1; }
  pending_commercial=$master_commercial pending_govcloud=$master_govcloud

  # The lease: the branch commit this update builds on (empty = the branch must not exist).
  old=$(git ls-remote origin "refs/heads/$branch" | cut -f1)
  pr=$(gh pr list -R "$repo" --head "$branch" --state open --json number --jq '.[0].number // empty')
  # The branch's pending values must be read whenever it exists, including a branch whose PR
  # could not be created; falling back to master would drop them.
  if [ -n "$old" ]; then
    if ! { git fetch -q origin "$old" && git show "$old:$file" > "$work/pending"; }; then
      echo "::error::$repo: can't read $file from $branch" >&2; return 1
    fi
    ch=$(read_default "$work/pending" commercial); [ -z "$ch" ] || pending_commercial=$(max "$master_commercial" "$ch")
    ch=$(read_default "$work/pending" govcloud); [ -z "$ch" ] || pending_govcloud=$(max "$master_govcloud" "$ch")
  elif [ -n "$pr" ]; then
    echo "::error::$repo: PR #$pr is open but its branch $branch is missing" >&2; return 1
  fi
  if [ "$channel" = commercial ]; then
    pending_commercial=$(max "$pending_commercial" "$version")
  else
    pending_govcloud=$(max "$pending_govcloud" "$version")
  fi
  target_commercial="" target_govcloud=""
  newer "$pending_commercial" "$master_commercial" && target_commercial=$pending_commercial
  newer "$pending_govcloud" "$master_govcloud" && target_govcloud=$pending_govcloud
  if [ -z "$target_commercial$target_govcloud" ]; then
    echo "$repo: $base already has the $channel default $version or newer; nothing to do."; return 0
  fi

  git checkout -q -B "$branch" "origin/$base"
  [ -z "$target_commercial" ] || write_default "$file" commercial "$target_commercial"
  [ -z "$target_govcloud" ] || write_default "$file" govcloud "$target_govcloud"
  [ "$(read_default "$file" commercial)" = "$pending_commercial" ] && [ "$(read_default "$file" govcloud)" = "$pending_govcloud" ] \
    || { echo "::error::$repo: failed to set the defaults in $file" >&2; return 1; }
  changes=$(summary "$target_commercial" "$target_govcloud")
  title=$($notes) && [ -n "$title" ] || { echo "::error::$repo: could not write the release notes" >&2; return 1; }
  git -c user.name="coralogix-release-bot" -c user.email="noreply@coralogix.com" commit -q -am "$title"
  # An identical branch only skips the push: the PR is still created or updated below, so a
  # run whose PR call failed after pushing is completed by the next one.
  if [ -n "$old" ] && [ "$(git rev-parse 'HEAD^{tree}')" = "$(git rev-parse "$old^{tree}")" ]; then
    echo "$repo: $branch is up to date: $changes"
  else
    [ -z "${BUMP_TEST_BEFORE_PUSH:-}" ] || "$BUMP_TEST_BEFORE_PUSH" "$repo" # test seam: simulate a concurrent run
    git push -q --force-with-lease="refs/heads/$branch:$old" origin "HEAD:refs/heads/$branch" || return 75
  fi

  body="Moves the default processor: $changes. [Releases](${release_url%/tag/*})

- commercial: $master_commercial -> $pending_commercial
- GovCloud: $master_govcloud -> $pending_govcloud$([ -z "$target_govcloud" ] || printf '\n\nThe GovCloud version passed the GovCloud smoke test in every region.')

Opened by cloudwatch-metric-streams-lambda-transformation's ci/bump-consumers.sh. Later releases update this PR; merge it after review."
  if [ -n "$pr" ]; then
    gh pr edit "$pr" -R "$repo" --title "$title" --body "$body" >/dev/null \
      || { echo "::error::$repo: pushed, but could not update PR #$pr" >&2; return 1; }
    echo "$repo: updated PR #$pr: $changes"
  else
    draft=(); [ "${BUMP_PR_DRAFT:-}" = true ] && draft=(--draft)
    # Retried: a concurrent run may have opened it first, and the next pass edits it. If
    # creating keeps failing, the run fails and a re-run creates it from the pushed branch.
    gh pr create -R "$repo" --base "$base" --head "$branch" --title "$title" --body "$body" ${draft[@]+"${draft[@]}"} >/dev/null || return 75
    echo "$repo: opened a PR: $changes"
  fi
}

# Each consumer in its own subshell with errexit on: its cd does not leak, any unchecked
# failure stops it, and one consumer failing does not stop the other.
set +e
( set -e; bump coralogix/cloudformation-coralogix-aws master aws-integrations/firehose-metrics/template.yaml notes_cfn ); cfn=$?
( set -e; bump coralogix/terraform-coralogix-aws master modules/firehose-metrics/main.tf notes_tf ); tf=$?
set -e
[ "$cfn" -eq 0 ] && [ "$tf" -eq 0 ]
