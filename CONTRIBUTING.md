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

For each new version a GitHub release is created. To prepare a new release, follow these steps:

1. Make sure all the changes you want to release are merged into the `main` branch and you have latest `main` branch checked out locally.
2. Create a tag for the new version on your local machine by running the following. Replace `<semantic_version>` with the new version number:
```
   tag=<semantic_version>
   git tag -s "v${tag}" -m "v${tag}"
```
3. Push the tag to GitHub:
```
   git push origin "v${tag}"
```
4. The `publish` workflow builds the standard and FIPS ZIPs and uploads `firehose-metrics-transformer-<version>.zip` and `firehose-metrics-transformer-fips-<version>.zip`, each with a `.sha256` file, to the Coralogix serverless S3 buckets. Versioned files are written once and can't be replaced: to fix a release, tag a new version. The unversioned `firehose-metrics-transformer.zip` moves to the new version. It then creates a _draft_ release with these files attached.
5. Once the draft release is created, go to [Releases](https://github.com/coralogix/cloudwatch-metric-streams-lambda-transformation/releases) page and open the new draft release. Edit the release notes as needed and check that the ZIP and `.sha256` files are attached.
6. Once everything is ready, publish the release.

### GovCloud

When the repository variable `GOVCLOUD_PUBLISH` is `true`, the `publish` workflow also copies the FIPS ZIP to the GovCloud buckets, using the `GOVCLOUD_ROLE_ARN`, `GOVCLOUD_BUCKET_PREFIX` and `GOVCLOUD_REGIONS` variables of the `govcloud` environment. To publish a version that was released while GovCloud publishing was off, run the workflow manually with that version:
```
   gh workflow run publish.yaml -f version=<semantic_version>
```