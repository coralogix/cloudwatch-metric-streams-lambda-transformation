#!/usr/bin/env bash
# Posts the open default-version bump PRs to Slack, like the shipper's template sync, so
# someone reviews and merges them. Never fails the job: the PRs exist either way.
#
#   GH_TOKEN=... SLACK_WEBHOOK_URL=... [SLACK_MENTION=@team-ido] ci/notify-bump.sh <version>
set -uo pipefail
version=${1:?usage: notify-bump.sh <version>}
[ -n "${SLACK_WEBHOOK_URL:-}" ] || { echo "SLACK_WEBHOOK_URL not set; skipping notification"; exit 0; }
prs=()
for repo in coralogix/cloudformation-coralogix-aws coralogix/terraform-coralogix-aws; do
  url=$(gh pr list -R "$repo" --head bump/firehose-processor --state open --json url --jq '.[0].url // empty') || url=""
  [ -z "$url" ] || prs+=("$url")
done
[ "${#prs[@]}" -gt 0 ] || { echo "no open bump PRs; nothing to announce"; exit 0; }
# link_names makes Slack resolve the @handle into a mention, as in the shipper's sync.
jq -n --arg text "${SLACK_MENTION:-@team-ido} firehose metrics processor *$version*: the default version bump needs review and merge: ${prs[*]}" \
  '{text: $text, link_names: 1}' \
  | curl -sf -X POST -H 'Content-type: application/json' --data @- "$SLACK_WEBHOOK_URL" >/dev/null \
  && echo "notified: ${prs[*]}" || echo "slack notification failed; continuing"
