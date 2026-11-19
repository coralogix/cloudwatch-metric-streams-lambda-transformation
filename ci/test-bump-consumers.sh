#!/usr/bin/env bash
# shellcheck disable=SC2016,SC2034,SC2329  # checks are eval'd strings
# Checks ci/bump-consumers.sh against two local consumer repositories and a stub `gh`: the
# commercial and GovCloud moves landing in one PR, re-runs, an older re-run, a GovCloud hold,
# master moving underneath, a merged bump, and one consumer failing without the other.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
t=$(mktemp -d); trap 'rc=$?; [ "$rc" = 0 ] && [ "$fail" = 0 ] || cat "$t/out"; rm -rf "$t"' EXIT; fail=0
mkdir -p "$t/bin" "$t/remotes/coralogix" "$t/prs"
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

cat > "$t/bin/gh" <<'STUB'
#!/usr/bin/env bash
# gh pr list|create|edit -R owner/repo ...; one open PR per repo, kept as $PRS/<repo>
set -euo pipefail
[ "$1 $2" != "api user" ] || { echo "test-bot 1"; exit 0; }
op=$2; shift 2; repo="" title=""
while [ $# -gt 0 ]; do case $1 in -R) repo=$2; shift 2 ;; --title) title=$2; shift 2 ;; --jq|--json|--head|--state|--base|--body) shift 2 ;; *) shift ;; esac; done
f=$PRS/${repo#*/}
case $op in
  list) [ -f "$f" ] && echo "$(cut -d' ' -f1 "$f")" || true ;;
  create) [ -z "${GH_FAIL_CREATE:-}" ] || { echo "stub: create failed" >&2; exit 1; }
          echo "1 $title" > "$f"; echo create >> "$f.log" ;;
  edit) [ -z "${GH_FAIL_EDIT:-}" ] || { echo "stub: edit failed" >&2; exit 1; }
        echo "1 $title" > "$f"; echo edit >> "$f.log" ;;
esac
STUB
chmod +x "$t/bin/gh"
export PATH="$t/bin:$PATH" PRS="$t/prs" CONSUMER_REMOTE="file://$t/remotes/"

seed() { # seed <repo> then files via stdin as "path<TAB>content-file"
  local d=$t/seed-$1; git init -q -b master "$d"
  while IFS=$'\t' read -r path src; do mkdir -p "$d/$(dirname "$path")"; cp "$src" "$d/$path"; done
  git -C "$d" add -A && git -C "$d" commit -qm init
  git clone -q --bare "$d" "$t/remotes/coralogix/$1.git"
}
cat > "$t/template.yaml" <<'X'
Metadata:
  SemanticVersion: 0.1.7
Resources:
Mappings:
  ProcessorDefault:
    Version:
      Commercial: '0.1.19' # bump-consumers: commercial
      GovCloud: '0.1.19' # bump-consumers: govcloud
X
cat > "$t/cfn-changelog.md" <<'X'
# Changelog

## firehose
<!-- To add a new entry write: -->
<!-- ### version / full date -->
<!-- * [Update/Bug fix] message that describes the changes that you apply -->

### 0.1.7 / 19 Nov 2026
* [Update] previous
X
cat > "$t/main.tf" <<'X'
locals {
  lambda_processor_commercial_default = "0.1.19" # bump-consumers: commercial
  lambda_processor_govcloud_default   = "0.1.19" # bump-consumers: govcloud
}
X
printf '# Changelog\n\n## v4.18.1\n\nprevious\n' > "$t/tf-changelog.md"
printf 'aws-integrations/firehose-metrics/template.yaml\t%s\naws-integrations/firehose-metrics/CHANGELOG.md\t%s\n' "$t/template.yaml" "$t/cfn-changelog.md" | seed cloudformation-coralogix-aws
printf 'modules/firehose-metrics/main.tf\t%s\nCHANGELOG.md\t%s\n' "$t/main.tf" "$t/tf-changelog.md" | seed terraform-coralogix-aws
git -C "$t/remotes/coralogix/terraform-coralogix-aws.git" tag v4.18.1 master

cfn=$t/remotes/coralogix/cloudformation-coralogix-aws.git tf=$t/remotes/coralogix/terraform-coralogix-aws.git
show() { git -C "$1" show "$2:$3" 2>/dev/null; }                    # show <remote> <ref> <path>
branch_has() { show "$1" bump/firehose-processor "$2" | grep -qF -- "$3"; }
tip() { git -C "$1" rev-parse -q --verify bump/firehose-processor || true; }
commits() { git -C "$1" rev-list --count master..bump/firehose-processor; }
logs() { cat "$PRS/$1.log" 2>/dev/null | tr '\n' ' '; }
creates() { grep -c create "$PRS/$1.log" 2>/dev/null || true; }
run() { "$here/bump-consumers.sh" "$1" "$2" >>"$t/out" 2>&1; }
T=aws-integrations/firehose-metrics/template.yaml
line() { show "$1" bump/firehose-processor "$2" | grep "bump-consumers: $3" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+'; } # line <remote> <path> <channel>
title() { cut -d' ' -f2- "$PRS/$1"; }
commit_on_master() { # commit_on_master <remote> <path> <content>
  local d; d=$(mktemp -d); git clone -q "$1" "$d"; mkdir -p "$d/$(dirname "$2")"; printf '%s\n' "$3" > "$d/$2"
  git -C "$d" add -A && git -C "$d" commit -qm "$2" && git -C "$d" push -q origin master; rm -rf "$d"
}
fail=0
check() { if eval "$2"; then echo "ok    $1"; else echo "FAIL  $1"; fail=1; fi; }

run commercial 0.1.20
check "commercial release opens a PR per consumer" '[ "$(logs cloudformation-coralogix-aws)" = "create " ] && [ "$(logs terraform-coralogix-aws)" = "create " ]'
check "only the commercial line moves" '[ "$(line "$cfn" $T commercial)" = 0.1.20 ] && [ "$(line "$cfn" $T govcloud)" = 0.1.19 ] && [ "$(line "$tf" modules/firehose-metrics/main.tf commercial)" = 0.1.20 ] && [ "$(line "$tf" modules/firehose-metrics/main.tf govcloud)" = 0.1.19 ]'
check "release notes: SemanticVersion, CHANGELOG, next module version" 'branch_has "$cfn" $T "SemanticVersion: 0.1.8" && branch_has "$cfn" aws-integrations/firehose-metrics/CHANGELOG.md "### 0.1.8 / " && show "$tf" bump/firehose-processor CHANGELOG.md | sed -n 3p | grep -qx "## v4.18.2"'

run govcloud 0.1.20
check "the GovCloud pass updates the same PR" '[ "$(logs cloudformation-coralogix-aws)" = "create edit " ] && [ "$(line "$cfn" $T govcloud)" = 0.1.20 ] && [ "$(line "$cfn" $T commercial)" = 0.1.20 ]'
check "the commit is attributed to the token's account" '[ "$(git -C "$cfn" log -1 --format="%an <%ae>" bump/firehose-processor)" = "test-bot <1+test-bot@users.noreply.github.com>" ]'
check "title says both moved" '[ "$(title cloudformation-coralogix-aws)" = "firehose-metrics: default processor 0.1.20 (commercial and GovCloud)" ]'
check "one commit, one CHANGELOG entry, one version step" '[ "$(commits "$cfn")" = 1 ] && [ "$(show "$cfn" bump/firehose-processor aws-integrations/firehose-metrics/CHANGELOG.md | grep -c "^### ")" = 2 ] && branch_has "$cfn" $T "SemanticVersion: 0.1.8"'

before=$(tip "$cfn")
run govcloud 0.1.20; run commercial 0.1.20
check "re-runs of the same release push nothing and open no PR" '[ "$(tip "$cfn")" = "$before" ] && [ "$(creates cloudformation-coralogix-aws)" = 1 ]'
run commercial 0.1.19
check "an older release never downgrades" '[ "$(tip "$cfn")" = "$before" ]'

commit_on_master "$cfn" aws-integrations/other/README.md "unrelated change"
run commercial 0.1.21
check "GovCloud held: commercial moves alone, GovCloud keeps its pending value" '[ "$(line "$cfn" $T commercial)" = 0.1.21 ] && [ "$(line "$cfn" $T govcloud)" = 0.1.20 ] && [ "$(title cloudformation-coralogix-aws)" = "firehose-metrics: default processor 0.1.21 (commercial), 0.1.20 (GovCloud)" ]'
check "the branch is rebuilt on current master" 'show "$cfn" bump/firehose-processor aws-integrations/other/README.md | grep -q unrelated && [ "$(commits "$cfn")" = 1 ]'

# The PR is merged and released: master now has the PR's values, the PR is gone.
git -C "$cfn" update-ref refs/heads/master bump/firehose-processor; rm -f "$PRS/cloudformation-coralogix-aws"
before=$(tip "$cfn")
run govcloud 0.1.20
check "after the merge an older re-run does nothing" '[ "$(tip "$cfn")" = "$before" ] && [ "$(creates cloudformation-coralogix-aws)" = 1 ]'
run govcloud 0.1.21
check "a later GovCloud pass opens a new PR with only that line" '[ "$(creates cloudformation-coralogix-aws)" = 2 ] && [ "$(title cloudformation-coralogix-aws)" = "firehose-metrics: default processor 0.1.21 (GovCloud)" ]'

# A concurrent commercial run pushes between this run's read and its push: the lease
# rejects the push and the retry keeps both moves.
cat > "$t/race" <<'X'
#!/usr/bin/env bash
[ "$1" = coralogix/cloudformation-coralogix-aws ] && [ ! -e "$RACED" ] || exit 0
touch "$RACED"
BUMP_TEST_BEFORE_PUSH= "$HERE/bump-consumers.sh" commercial 0.1.23 >/dev/null 2>&1
X
chmod +x "$t/race"
RACED=$t/raced HERE=$here BUMP_TEST_BEFORE_PUSH=$t/race run govcloud 0.1.22
check "a concurrent run is not overwritten" '[ -e "$t/raced" ] && [ "$(line "$cfn" $T commercial)" = 0.1.23 ] && [ "$(line "$cfn" $T govcloud)" = 0.1.22 ]'

commit_on_master "$cfn" aws-integrations/other/NOTES.md "another unrelated change"
run govcloud 0.1.22
check "re-running a version refreshes the PR after master moved" 'show "$cfn" bump/firehose-processor aws-integrations/other/NOTES.md | grep -q another && [ "$(line "$cfn" $T commercial)" = 0.1.23 ] && [ "$(commits "$cfn")" = 1 ]'

check "a failed PR update fails the run" '! GH_FAIL_EDIT=1 run commercial 0.1.24'

git -C "$cfn" update-ref -d refs/heads/bump/firehose-processor
check "an open PR whose branch can't be read fails instead of dropping its values" '! run commercial 0.1.25 && [ -z "$(tip "$cfn")" ]'
rm -f "$PRS/cloudformation-coralogix-aws" # that PR is closed

check "a failed PR creation fails the run, even after the branch is pushed" '! GH_FAIL_CREATE=1 run commercial 0.1.26 && [ ! -e "$PRS/cloudformation-coralogix-aws" ] && [ "$(line "$cfn" $T commercial)" = 0.1.26 ]'
run commercial 0.1.26
check "a re-run creates the missing PR from the pushed branch" '[ -e "$PRS/cloudformation-coralogix-aws" ] && [ "$(title cloudformation-coralogix-aws)" = "firehose-metrics: default processor 0.1.26 (commercial)" ]'
GH_FAIL_CREATE=1 run govcloud 0.1.27 || true; rm -f "$PRS/cloudformation-coralogix-aws" # pushed, PR creation failed
run commercial 0.1.27
check "a stranded branch keeps its pending values" '[ "$(line "$cfn" $T govcloud)" = 0.1.27 ] && [ "$(line "$cfn" $T commercial)" = 0.1.27 ]'

check "an unknown channel is rejected" '! run staging 0.1.22'
commit_on_master "$tf" modules/firehose-metrics/main.tf 'lambda_processor_default_version = "0.1.21"'
run commercial 0.1.28 && rc=0 || rc=$?
check "a consumer without the markers fails, the other is still bumped" '[ "$rc" = 1 ] && [ "$(line "$cfn" $T commercial)" = 0.1.28 ]'

exit $fail
