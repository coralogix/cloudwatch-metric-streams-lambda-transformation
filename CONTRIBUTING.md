# Contributing

Thank for your interest in our project!

If you'd like to report a bug or suggest a feature or get in touch with the repository owners for any other reason, the best way to do this is by opening an issue on the repository.

In case of a bug report, please be as precise in your description, include all the relevant information, such as the version of the Lambda function you used (Git commit SHA, branch name etc.), your OS, environment, Lambda configuration etc. 

If you'd like to contribute to our code or documentation, the best way to go about this is to first open an issue (if you have e.g. a new feature you'd like to contribute) and discuss with the repository owners, before submitting the change itself.

## Pull Request Process

We require all of our commits to be signed, please make sure they are signed by following [this guide](https://docs.github.com/en/authentication/managing-commit-signature-verification/signing-commits).

1. Before opening a PR, ensure all checks (formatting, lint, tests) are passing locally by running the `make package` target.
2. Optionally, if you'd like to check the functionality of your changes, you can run a manual test on your own AWS account - the resulting `function.zip` afer running `make package` can be used for upload and testing.
3. If you're PR is not ready for a review yet, please mark it as a draft.
4. Reviewers will get to your PR as soon as possible. In order for your PR to be merged, all substantial comments must be addressed and at least **1** approval from a repository owner is required.