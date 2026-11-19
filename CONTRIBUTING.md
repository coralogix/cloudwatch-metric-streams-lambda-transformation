# Contributing

Thank for your interest in our project!

If you'd like to report a bug or suggest a feature or get in touch with the repository owners for any other reason, the best way to do this is by opening an issue on the repository.

In case of a bug report, please be as precise in your description, include all the relevant information, such as the version of the Lambda function you used (Git commit SHA, branch name etc.), your OS, environment, Lambda configuration etc. 

If you'd like to contribute to our code or documentation, the best way to go about this is to first open an issue (if you have e.g. a new feature you'd like to contribute) and discuss with the repository owners, before submitting the change itself.

## Pull Request Process

We require all of our commits to be signed, please make sure they are signed by following [this guide](https://docs.github.com/en/authentication/managing-commit-signature-verification/signing-commits).

1. Before opening a PR, ensure all checks (formatting, lint, tests) are passing locally by running the `make package` target.
2. Optionally, if you'd like to check the functionality of your changes, you can run a manual test on your own AWS account - the resulting `bootstrap.zip` afer running `make package` can be used for upload and testing.
3. If you're PR is not ready for a review yet, please mark it as a draft.
4. Reviewers will get to your PR as soon as possible. In order for your PR to be merged, all substantial comments must be addressed and at least **1** approval from a repository owner is required.

## Release Process

Every merge to `main` that changes the code (Go sources other than tests, `go.mod`, `go.sum` or the `Makefile`) is released automatically:

1. The `publish` workflow takes the newest `vX.Y.Z` tag and bumps the patch version. Add the `minor` or `major` label to the PR for a bigger bump (the largest label on any PR merged since the last release wins), or `skip-release` to merge without releasing (for example to batch several PRs; the next release includes them).
2. It builds the standard and FIPS ZIPs and uploads `firehose-metrics-transformer-<version>.zip` and `firehose-metrics-transformer-fips-<version>.zip`, each with a `.sha256` file, to the Coralogix serverless S3 buckets. Versioned files are written once and can't be replaced. The unversioned `firehose-metrics-transformer.zip` moves to the new version.
   The commit is tagged `v<version>` before the upload, so a failed release keeps its version.
3. It then publishes a GitHub release with these files and generated release notes. Edit the notes afterwards if needed.

To release a merge that didn't touch the code, run the `publish` workflow manually on `main`. A manual run ignores `skip-release`.

If a run fails, re-run it: a commit keeps the version it was given, and a commit already included in a newer release isn't released again.

### Default version in the CloudFormation template and Terraform module

The `firehose-metrics` CloudFormation template (`cloudformation-coralogix-aws`) and Terraform module (`terraform-coralogix-aws`) each have two default processor versions, one for commercial regions and one for GovCloud, on the lines marked `bump-consumers: commercial` and `bump-consumers: govcloud`. Customers set the same parameter in both partitions; only the default differs, and normally it doesn't.

`ci/bump-consumers.sh` moves them with one rolling pull request per repository:

- `publish` moves the commercial default after a release;
- `publish-govcloud` moves the GovCloud default once the release has passed the GovCloud smoke test, a few minutes later, in the same pull request.

Later releases update the same pull requests; a re-run of an older release never moves a default back. Once the pull requests are updated, `ci/notify-bump.sh` posts them to Slack (`SLACK_WEBHOOK_URL`, mentioning `SLACK_MENTION`, default `@team-ido`), as the shipper's template sync does: after the GovCloud default when GovCloud publishing is on (`GOVCLOUD_PUBLISH=true`), otherwise after the commercial default. The commits are attributed to the account behind `GH_TOKEN`.

- **Normal release:** merge the pull request once it moves both defaults, then let each repository release as usual.
- **Commercial-only fix, or GovCloud on hold** (`GOVCLOUD_PUBLISH=false`): the pull request moves only the commercial default; merge it as usual. GovCloud keeps its version until a release passes the GovCloud smoke test and is approved.
- **Security fix:** the same path on an expedited schedule (validation, approval and release, then an announcement); it skips the wait, not the checks.

The bump runs in its own `bump-consumers` job, which can fail without failing the release or the GovCloud publication; check its annotation when a run shows one. To retry it, or to refresh an open pull request after master has moved (for example when a consumer's release changed its next version), re-run that job: in the release's `publish` run for the commercial default, in its `publish-govcloud` run for the GovCloud default. Re-running a version rebuilds the pull request from current master and never moves a default back.
