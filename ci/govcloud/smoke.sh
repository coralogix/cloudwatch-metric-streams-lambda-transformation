#!/usr/bin/env bash
# Smoke-tests a published GovCloud transformer version: creates a function from the
# uploaded ZIP, invokes it with ci/govcloud/smoke-event.json (one CloudWatch metric-stream
# record) and passes only when the record comes back Ok with its data point and the
# function's last log line ("Batch done", see logBatchDone in main.go) reports
# fips140=true. Missing, unreadable or truncated evidence fails.
#
# The evidence line exists from 0.1.20 (the release that added it); older versions fail
# here by design, with that reason.
#
# Covers deployment, invocation and record preservation. It does not prove tag
# enrichment (the smoke role cannot read tags) or Firehose delivery to Coralogix.
#
# Usage: smoke.sh <version> <region>
#
# Environment:
#   SMOKE_ARTIFACT_BUCKET  bucket with the published ZIP (required)
#   SMOKE_BOUNDARY_ARN     permissions boundary for the function's role (required)
#   SMOKE_RUN_ID           unique id for resource names (default GITHUB_RUN_ID or the time)
#
# Every resource is named smoke-* and removed at the end, pass or fail. The exit code is
# the test's; cleanup problems are reported but never hide a test failure.
set -uo pipefail

version=${1:?usage: smoke.sh <version> <region>}
region=${2:?usage: smoke.sh <version> <region>}
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "::error::'$version' is not a version like 0.1.20."; exit 2; }
first_compatible=0.1.20
[ "$(printf '%s\n' "$first_compatible" "$version" | sort -V | head -1)" = "$first_compatible" ] \
  || { echo "::error::$version predates the FIPS evidence log; the smoke test needs $first_compatible or later."; exit 1; }
: "${SMOKE_ARTIFACT_BUCKET:?SMOKE_ARTIFACT_BUCKET is required}" "${SMOKE_BOUNDARY_ARN:?SMOKE_BOUNDARY_ARN is required}"

export AWS_REGION="$region" AWS_DEFAULT_REGION="$region"
here=$(cd "$(dirname "$0")" && pwd)
name="smoke-${SMOKE_RUN_ID:-${GITHUB_RUN_ID:-$(date +%s)}}-firehose"
key=firehose-metrics-transformer-$version.zip
tmp=$(mktemp -d)
log() { echo "[$(date -u +%H:%M:%S)] $*"; }

result=1 created_role=false created_function=false
cleanup() {
  local leftovers=() out
  log "cleanup"
  if [ "$created_function" = true ]; then
    aws lambda delete-function --function-name "$name" || leftovers+=("function $name")
  fi
  if ! out=$(aws logs delete-log-group --log-group-name "/aws/lambda/$name" 2>&1); then
    grep -q ResourceNotFoundException <<<"$out" || leftovers+=("log group /aws/lambda/$name")
  fi
  if [ "$created_role" = true ]; then
    { aws iam detach-role-policy --role-name "$name" --policy-arn "$policy" \
        && aws iam delete-role --role-name "$name"; } || leftovers+=("role $name")
  fi
  rm -rf "$tmp"
  for item in ${leftovers[@]+"${leftovers[@]}"}; do echo "::warning::smoke cleanup left: $item"; done
  log "result: $([ "$result" -eq 0 ] && echo PASS || echo FAIL)"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 1' INT TERM
fail() { echo "::error::$*"; result=1; exit 1; }

partition=$(aws sts get-caller-identity --query Arn --output text | cut -d: -f2) || fail "No AWS credentials."
policy=arn:$partition:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
object_version=$(aws s3api head-object --bucket "$SMOKE_ARTIFACT_BUCKET" --key "$key" --query VersionId --output text) \
  || fail "s3://$SMOKE_ARTIFACT_BUCKET/$key is not there."

log "role $name"
created_role=true
role_arn=$(aws iam create-role --role-name "$name" --permissions-boundary "$SMOKE_BOUNDARY_ARN" \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}' \
  --query Role.Arn --output text) || fail "Can't create role $name."
aws iam attach-role-policy --role-name "$name" --policy-arn "$policy" || fail "Can't attach $policy."

# A new role takes a few seconds before Lambda can assume it.
log "function $name from s3://$SMOKE_ARTIFACT_BUCKET/$key ($object_version)"
for attempt in $(seq 1 12); do
  if out=$(aws lambda create-function --function-name "$name" --role "$role_arn" \
      --runtime provided.al2023 --architectures arm64 --handler bootstrap --timeout 30 \
      --environment 'Variables={LOG_LEVEL=debug}' \
      --code "S3Bucket=$SMOKE_ARTIFACT_BUCKET,S3Key=$key,S3ObjectVersion=$object_version" 2>&1); then
    created_function=true; break
  fi
  grep -q "cannot be assumed" <<<"$out" && [ "$attempt" -lt 12 ] || fail "Can't create $name: $(tail -n1 <<<"$out")"
  sleep 5
done
aws lambda wait function-active-v2 --function-name "$name" || fail "$name did not become active."

log "invoke"
meta=$(aws lambda invoke --function-name "$name" --log-type Tail --cli-binary-format raw-in-base64-out \
  --payload "fileb://$here/smoke-event.json" "$tmp/response.json" --output json) || fail "Invoke failed."
jq -r '.LogResult // empty' <<<"$meta" | base64 -d > "$tmp/log.txt" 2>/dev/null \
  || fail "The invoke returned no readable log tail."
cat "$tmp/log.txt"

[ "$(jq -r '.FunctionError // empty' <<<"$meta")" = "" ] || fail "The function failed: $(cat "$tmp/response.json")"
# Affirmative evidence: a complete JSON line from this binary saying FIPS mode is on.
# Non-JSON lines (Lambda's START/END/REPORT) are skipped; a cut-off line never parses.
fips=$(jq -R 'fromjson? | select(.msg == "Batch done") | .fips140' "$tmp/log.txt" | tail -n1)
[ "$fips" = true ] || fail "No evidence of FIPS mode: want a \"Batch done\" log line with fips140=true, got '${fips:-none}'."
jq -e '.records | length == 1 and .[0].recordId == "smoke-1" and .[0].result == "Ok"' "$tmp/response.json" >/dev/null \
  || fail "Unexpected response: $(cat "$tmp/response.json")"
# The data is a length-delimited OTLP message; the volume id must survive the transformation.
jq -r '.records[0].data' "$tmp/response.json" | base64 -d | grep -aq vol-smoke \
  || fail "The returned record lost its data point."

result=0
