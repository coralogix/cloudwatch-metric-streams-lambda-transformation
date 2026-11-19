#!/usr/bin/env bash
# shellcheck disable=SC2016,SC2034,SC2329  # checks are eval'd strings
# Checks ci/govcloud/smoke.sh's verdict against a stub `aws` whose invoke returns the
# given log tail and response: it must pass only with complete evidence of FIPS mode.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
t=$(mktemp -d); trap 'rm -rf "$t"' EXIT
mkdir -p "$t/bin"
cat > "$t/bin/aws" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "sts get-caller-identity") echo arn:aws-us-gov:sts::019051664735:assumed-role/r/s ;;
  "s3api head-object") echo "${STUB_VERSION:-v1}" ;;
  "lambda create-function") for a; do case $a in S3Bucket=*) echo "$a" > "$STUB_CODE" ;; esac; done ;;
  "iam create-role") echo arn:aws-us-gov:iam::019051664735:role/smoke ;;
  "lambda invoke")
    for a; do case $a in */response.json) out=$a ;; esac; done
    printf '%s' "$STUB_RESPONSE" > "$out"
    jq -nc --arg l "$STUB_LOG" '{StatusCode: 200, LogResult: $l}' ;;
esac
STUB
chmod +x "$t/bin/aws"
export PATH="$t/bin:$PATH" STUB_CODE="$t/code" SMOKE_ARTIFACT_BUCKET=b SMOKE_BOUNDARY_ARN=arn:aws-us-gov:iam::019051664735:policy/boundary

b64() { printf '%s\n' "$@" | base64 | tr -d '\n'; }
response() { printf '{"records":[{"recordId":"smoke-1","result":"Ok","data":"%s"}]}' "$(printf 'x %s x' "$1" | base64)"; }
ok_response=$(response vol-smoke)
fips_on='{"level":"DEBUG","msg":"Batch done","records":1,"fips140":true}'
smoke() { STUB_LOG=$1 STUB_RESPONSE=${2:-$ok_response} "$here/smoke.sh" 0.1.20 us-gov-west-1 >/dev/null 2>&1; }
fail=0
check() { if eval "$2"; then echo "ok    $1"; else echo "FAIL  $1"; fail=1; fi; }

check "passes with fips140=true, pinned to the object version" 'smoke "$(b64 START "$fips_on" END REPORT)" && grep -q ",S3ObjectVersion=v1$" "$STUB_CODE"'
check "passes for an unversioned object, without a version" 'STUB_VERSION=None smoke "$(b64 "$fips_on")" && ! grep -q S3ObjectVersion "$STUB_CODE"'
check "fails for a non-FIPS binary" '! smoke "$(b64 START "${fips_on/true/false}" END)"'
check "fails with no evidence" '! smoke "$(b64 START END REPORT)"'
check "fails with no log tail" '! smoke ""'
check "fails with a malformed log tail" '! smoke "not base64 !!"'
check "fails with truncated evidence" '! smoke "$(b64 "${fips_on:0:45}")"'
check "fails when the record loses its data" '! smoke "$(b64 "$fips_on")" "$(response vol-other)"'
check "fails for a version before the evidence log" '! STUB_LOG="$(b64 "$fips_on")" STUB_RESPONSE="$ok_response" "$here/smoke.sh" 0.1.19 us-gov-west-1 >/dev/null 2>&1'
exit $fail
