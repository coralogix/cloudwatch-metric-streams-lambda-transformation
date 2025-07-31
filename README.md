# CloudWatch Metric Streams Lambda transformation

## About The Project
This Lambda function can be used as a Kinesis Firehose transformation function, to enrich the metrics from CloudWatch Metric Streams with AWS resource tags.

- Accepts Kinesis Firehose events with metric data in [OTLP v1.0, size-delimited format](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch-metric-streams-formats-opentelemetry-100.html)
- Obtains AWS resource information through the [AWS tagging API](https://docs.aws.amazon.com/resourcegroupstagging/latest/APIReference/overview.html) and related APIs (API Gateway, EC2...)
- Supports a map of tag keys that will be copied to metrics; skipping the rest
- Associates CloudWatch metrics with particular resources and enriches the metric labels with resource tags, based on the [Yet Another CloudWatch Exporter](https://github.com/nerdswords/yet-another-cloudwatch-exporter) library
- Returns Kinesis Firehose response with transformed record in OTLP v1.0, size-delimited format, for further processing and exporting to Chronopshere (or other) destination by the Kinesis stream

### Installation and usage
1. Download the `bootstrap.zip` file from the [releases](https://github.com/UrbanCompass/cloudwatch-metric-streams-lambda-transformation/releases) page. Unless instructed otherwise, we recommend downloading the latest release. Alternatively, you can test, lint and build the zipped Lambda function by yourself by running `make all`.
2. Create a new AWS Lambda function in your designated region with the following parameters:
    - Runtime: `Custom runtime on Amazon Linux 2`
    - Handler: `bootstrap`
    - Architecture: `arm64` (but you can also build the function for `x86_64`)
3. Upload the `bootstrap.zip` file as the code source.
4. Make sure to set the memory. We recommend starting with `128 MB` and, depending on the number of metrics you export and speed of Lambda processinr, see if you need to increase it.
5. Adjust the role of the Lambda function as described below in section [Necessary permissions](###necessary-permissions).
6. Optionally, add environment variables to configure the Lambda, as described in the [Configuration](###configuration) section.
7. The Lambda function is ready to be used as in [Kinesis Data Firehose Data Transformation](https://docs.aws.amazon.com/firehose/latest/dev/data-transformation.html?icmpid=docs_console_unmapped). Please note the function ARN and provide it in the relevant section of the Kinesis Data Firehose configuration.

Depending on the size of your setup, we also recommend to accordingly adjust your Lambda [buffer hint](https://docs.aws.amazon.com/firehose/latest/dev/data-transformation.html) and Kinesis Data Firehose [buffer size](https://docs.aws.amazon.com/firehose/latest/dev/basic-deliver.html#frequency) configuration. For most optimal experience, we recommend setting the Lambda buffer hint to `0.2 MB` and Kinsis Data Firehose buffer size to `1 MB`. **Beware that this might cause more frequent Lambda runs, which might result in higher costs**.

## Migrating from `Go 1.x` runtime to custom runtime on Amazon Linux 2
Please beware that the `Go 1.x` runtime will be [deprecated](https://aws.amazon.com/blogs/compute/migrating-aws-lambda-functions-from-the-go1-x-runtime-to-the-custom-runtime-on-amazon-linux-2/) at the end of December 2023. If you were previously using this Lambda function with the `Go 1.x`, you will need to migrate the function in accordance with the instructions in the [AWS documentation](https://aws.amazon.com/blogs/compute/migrating-aws-lambda-functions-from-the-go1-x-runtime-to-the-custom-runtime-on-amazon-linux-2/).

### Configuration
There is a couple of configuration options that can be set via environment variables:

| Environment variable             | Default | Possible values                                                           | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
|----------------------------------|--------|---------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `LOG_LEVEL`                      | `info` | `debug`                                                                   | Sets log level.                                                                                                                                                                                                                                                                                                                                                                                                                                                                    
| `CONTINUE_ON_RESOURCE_FAILURE`   | `true` | `false`                                                                   | Determines whether to continue on a failed API call to obtain resources. If set to true (by default), the Lambda will skip enriching the metrics with tags and return metrics without tags. If set to false, the Lambda will terminate and the metrics won't be exported to Kinesis Data Firehose.                                                                                                                                                                                 
| `FILE_CACHE_ENABLED`             | `true` | `false`                                                                   | Enables caching of resources to local file. See [Caching resources](###caching-resources) for more details.                                                                                                                                                                                                                                                                                                                                                                        
| `FILE_CACHE_PATH`                | `/tmp` | `<file_path>`                                                             | Sets the path to directory where to cache resources. See [Caching resources](###caching-resources) for more details.                                                                                                                                                                                                                                                                                                                                                               
| `FILE_CACHE_EXPIRATION`          | `1h`   | `<duration>`                                                              | Sets the expiration time for the cached resources. See [Caching resources](###caching-resources) for more details.                                                                                                                                                                                                                                                                                                                                                                 
| `METRICS_TO_REWRITE_TIMESTAMP`   |        | `["<namespace_1>:<metric_name_1>","<namespace_2>:<metric_name_2>", ...]`  | If provided, rewrites the timestamp of the metrics in the list to the current time. This is useful for metrics that are published with older dates (ex. `AWS/S3:BucketSizeBytes` published throughout the day with time of midnight) and may be rejected by downstream consumers (ex. Chronosphere aggregation rules accept data points from two minutes to eight minutes past the current ingestion time; Raw data points can be written to the database up to two hours before the ingestion time). Provide a list of strings, each string in the format `<namespace>:<metric_name>`. The timestamp will be set to the current time when the Lambda function is invoked.
| `AWS_ACCOUNTS_TO_TAGS`           | `true` | `[["account_1",[["tag_name_1", "tag_value_1"], ["tag_name_2", "tag_value_2"]]], ...]` | If provided, adds tags to all the in-memory resources (e.g. not the resources in AWS) fetched from an account, before these in-memory resources are used to determine which resource tags will be copied to related metrics. For example. use to add a production environment tag to all resources in an account known to only contain production resources. Provide tuples of tuples (each 2-element, and non-blank), each representing an account and an associated list of tags to add to resources for the account. The tags will only be applied if not already present on the resources.
| `AWS_TAG_NAMES_TO_METRIC_LABELS` |        | `[["tag_input_1", "label_output_1"], ["tag_input_2", "label_output_2"], [...]]` | If provided, translates AWS tag names to metric label names. Provide a list of 2-element, non-blank tuples. The list will be applied in the order provided. Therefore, if multiple AWS tags resolve to the same metric label, the value of the last tag will be used.
| `AWS_TAG_NAMES_IS_FILTER`        | `true` | `false`                                                                   | If `true` (by default), `AWS_TAG_NAME_TO_METRIC_LABEL` acts as a filter; only tag names in the list found on resources will be copied to related metrics. If `false`, all tags on resources will be copied to related metrics.
| `AWS_ROLE_TO_ASSUME`             |        | `<role_name>`                                                             | If not provided, get tagged resources from the current Lambda execution account/role. If provided, the Lambda will get tagged resources from the current Lambda execution account/role plus the provided role on all the accounts provided in `AWS_ACCOUNTS_TO_SEARCH`. The assumed role ARN will be constructed like: `arn:aws:iam::123456789000:role/FirehoseLambdaMetricsTaggerReadTagsAccessRole`  Make sure the Lambda's execution role has permissions to assume the provided roles.
| `AWS_ACCOUNTS_TO_SEARCH`         |        | `account_1,account_2`                                                     | If `AWS_ROLE_TO_ASSUME` is provided, these are the accounts used to get tagged resources. The Lambda will try to obtain tags from resources in these accounts by assuming the role provided in `AWS_ROLE_TO_ASSUME`. If not provided, the Lambda will only obtain tags from resources in the current account (the monitoring account).

### Necessary permissions
The Lambda will use it's [execution role](https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html) to call other AWS APIs. You need to therefore ensure your Lambda's role has a policy with the following permissions. You can use the following JSON to create an inline policy for your role, to grant all necessary permissions:
Example role name: `FirehoseLambdaMetricsTaggerReadTagsAccessRole` with the following policy named `FirehoseLambdaMetricsTaggerReadTagsPolicy`:
```
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Action": [
        "iam:ListAccountAliases",
        "tag:GetResources",
        "cloudwatch:GetMetricData",
        "cloudwatch:GetMetricStatistics",
        "cloudwatch:ListMetrics",
        "apigateway:GET",
        "aps:ListWorkspaces",
        "autoscaling:DescribeAutoScalingGroups",
        "dms:DescribeReplicationInstances",
        "dms:DescribeReplicationTasks",
        "ec2:DescribeTransitGatewayAttachments",
        "ec2:DescribeSpotFleetRequests",
        "shield:ListProtections",
        "storagegateway:ListGateways",
        "storagegateway:ListTagsForResource"
      ],
      "Effect": "Allow",
      "Resource": "*"
    }
  ]
}
```

The trust policy for the role should have at least the following statement:
```
{
  "Effect": "Allow",
  "Principal": {
    "Service": "lambda.amazonaws.com"
  },
  "Action": "sts:AssumeRole"
}
```

## Multiple accounts support

AWS CloudWatch Metric Streams can be set up to stream metrics from multiple accounts into a single Kinesis Data Firehose.
One account is set up in CloudWatch as the "monitoring account", which is the account where the Kinesis Data Firehose and the Lambda function are set up. The other accounts are "source accounts", which stream their metrics to the monitoring account.
After setting up the monitoring account, an option will be available in the CloudWatch Metric Streams configuration to stream metrics from source accounts.
For more information on setting up multi-account CloudWatch Metric Streams, see [this documentation](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch-Unified-Cross-Account.html).

Once metrics are being streamed from multiple accounts, the Lambda function needs to be configured to obtain tags from resources in these accounts as well.
AWS has a concept of [cross-account observability](https://docs.aws.amazon.com/IAM/latest/UserGuide/tutorial_cross-account-with-roles.html), which allows a role in one account to assume a role in another account. This can be used to obtain tags from resources in multiple accounts.
To enable the Lambda function to obtain tags from resources in multiple accounts, set the `AWS_ACCOUNT_ROLE_ARNS_TO_SEARCH` environment variable to a comma-separated list of role ARNs that the Lambda can assume in order to obtain tags from resources in other accounts.

example:
```
AWS_ACCOUNT_ROLE_ARNS_TO_SEARCH=arn:aws:iam::123456789000:role/FirehoseLambdaMetricsTaggerReadTagsAccessRole,arn:aws:iam::987654321000:role/FirehoseLambdaMetricsTaggerReadTagsAccessRole
```

The Lambda will begin by trying to obtain tags from the current account (the monitoring account) using its own execution role.
The Lambda will then try to assume each role in the `AWS_ACCOUNT_ROLE_ARNS_TO_SEARCH` list and fetch the resource tags.

The permissions needed for the Lambda's execution role to assume roles in other accounts are as follows.
On the monitoring account (the account where the Lambda is running), make sure the Lambda's execution role has permissions to assume the roles in other accounts.

Example role name: `FirehoseLambdaMetricsTaggerReadTagsAccessRole` (from above) on the monitoring account needs this additional statement in the policy:

```
{
  "Effect": "Allow",
  "Action": "sts:AssumeRole",
  "Resource": [
     "arn:aws:iam::123456789000:role/FirehoseLambdaMetricsTaggerReadTagsAccessRole",
     "arn:aws:iam::987654321000:role/FirehoseLambdaMetricsTaggerReadTagsAccessRole",
     "..."
   ]
}
```
If there are a lot of accounts, you can use a wildcard in the resource ARN, like this:
```"Resource": "arn:aws:iam::*:role/FirehoseLambdaMetricsTaggerReadTagsAccessRole"
```
And deny individual accounts if needed, as follows (put this statement after the allow statement):
```
{
  "Effect": "Deny",
  "Action": "sts:AssumeRole",
  "Resource": [
     "arn:aws:iam::111111111111:role/FirehoseLambdaMetricsTaggerReadTagsAccessRole",
     "arn:aws:iam::222222222222:role/FirehoseLambdaMetricsTaggerReadTagsAccessRole"
   ]
}
```

On the source accounts (the accounts where the resources are located), make sure the role that the Lambda will assume have a trust policy that allows the Lambda's execution role to assume them.
Example role on the source account named `FirehoseLambdaMetricsTaggerReadTagsAccessRole` with trust policy:
```
{
  "Effect": "Allow",
  "Principal": {
    "AWS": "arn:aws:iam::<monitoring-account-id>:role/FirehoseLambdaMetricsTaggerReadTagsAccessRole"
  },
  "Action": "sts:AssumeRole"
}
```
The source account role should also have a policy with the necessary permissions to obtain resource tags, as described in the [Necessary permissions](###necessary-permissions) section above.


### Caching resources
Users, who do not wish to fetch resources from the AWS API on every Lambda invocation, can take advantage of caching of resources to a local file. Caching is enabled by default; to disable it, set the `FILE_CACHE_ENABLED` environment variable to `false`.

 This can be especially useful for users with a large number of resources, in order to keep the Lambda invocation time low and at the same to avoid hitting the [resource tagging API](https://aws.amazon.com/blogs/aws/new-aws-resource-tagging-api/) rate limits. Beware though that any changes to resource tags will not be reflect in metrics until the cache expires and is renewed.

Caching can be enabled by setting the `FILE_CACHE_PATH` environment variable to a path of the directory, where the resources will be cached. You can leverage Lambda's emphemeral storage by settings this siply to `/tmp`. This ensures that the resources will be cached between Lambda invocations on a single Lambda environment, meaning the cache will be reset when new Lambda environment is created. For more details on ephemeral storage and other storage options see [here](https://aws.amazon.com/blogs/compute/choosing-between-aws-lambda-data-storage-options-in-web-apps/).

The resources will be cached for a period of time, which can be set by the `FILE_CACHE_EXPIRATION` environment variable. The expiration time can be set in the [Go duration format](https://golang.org/pkg/time/#ParseDuration). If the `FILE_CACHE_EXPIRATION` is not set, the resources will be cached for 1 hour by default.

## Extra costs and usage of AWS APIs
There are couple of costs connected with usage of Lambda transformation. Below, these costs are described in details (based on examples and pricing in the US East region). This example assumes a user will be exporting metrics from all namespaces and that the metrics updates are coming once per minute (which is not true for all AWS metrics, see [this thread](https://serverfault.com/questions/1003344/aws-cloudwatch-metrics-are-there-convergence-delays?newreg=80710344c54e4a8397eac3acfdb01941) to learn more). The example is based on a region with ~5000 metrics. For detailed pricing information see [here](https://aws.amazon.com/lambda/pricing/). This calculation is for informative purposes and it is valid as of March 2023 to the best of our knowledge.

- The costs connected with Lambda
  - With assumed update every minute (accounting for 43,200 invocations per month), the costs for invocation (every 1,000,000 requests / 0.20 USD), comes to **0.01 USD**. 
  - For computation costs, it's not easy to estimate the duration of Lambda - this will depend on some factors like cold vs. warm, amount of metrics etc. We’re using some guesswork and more pessimistic estimate here for the duration, so we round the duration to 1 second (but realistically our tests show the function to take anywhere between 300-700 ms). This comes up to 5400 GB seconds per month (with configured 128 MB of memory), which equals to **0.1 USD** per month.

- The costs connected with calling AWS APIs
  - Depending on the namespaces you will stream your CloudWatch metrics from, in addition to the [resource tagging API](https://aws.amazon.com/blogs/aws/new-aws-resource-tagging-api/), the Lambda can make call to a number of AWS APIs, depending on the services in questions. However, all of these API calls should be **free**.

## Todos
- [ ] Support for JSON data input
