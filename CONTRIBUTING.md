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

For each new version a GitHub release is created. To release:

1. In your PR (or a separate one), set `VERSION` to the new semantic version, for example `0.1.18`. Merges that don't change `VERSION` publish nothing.
2. Merge to `main`. The `publish` workflow checks that the version is new and not older than the latest release.
3. The workflow builds the standard and FIPS ZIPs and uploads `firehose-metrics-transformer-<version>.zip` and `firehose-metrics-transformer-fips-<version>.zip`, each with a `.sha256` file, to the Coralogix serverless S3 buckets. Versioned files are written once and can't be replaced: to fix a release, bump `VERSION` again. The unversioned `firehose-metrics-transformer.zip` moves to the new version. It then creates the `v<version>` tag and a _draft_ release with these files attached.
4. Once the draft release is created, go to [Releases](https://github.com/coralogix/cloudwatch-metric-streams-lambda-transformation/releases) page and open the new draft release. Edit the release notes as needed and check that the ZIP and `.sha256` files are attached.
5. Once everything is ready, publish the release.
