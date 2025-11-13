 # CloudWatch Metric Streams Lambda transformation

## About The Project
This Lambda function can be used as a Data Firehose transformation function, to enrich the metrics from CloudWatch Metric Streams with AWS resource tags.

- Accepts Data Firehose events with metric data in [OTLP v0.7, size-delimited format](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch-metric-streams-formats-opentelemetry.html)
- Obtains AWS resource information through the [AWS tagging API](https://docs.aws.amazon.com/resourcegroupstagging/latest/APIReference/overview.html) and related APIs (API Gateway, EC2...)
- Associates CloudWatch metrics with particular resources and enriches the metric labels with resource tags, based on the [Yet Another CloudWatch Exporter](https://github.com/nerdswords/yet-another-cloudwatch-exporter) library
- Returns Data Firehose response with transformed record in OTLP v0.7, size-delimited format, for further processing and exporting to Coralogix (or other) destination by the Firehose stream

### Automated Installation

This Lambda function gets deployed as a part of [AWS CloudWatch Metric Streams with Amazon Data Firehose](https://coralogix.com/docs/integrations/aws/amazon-data-firehose/aws-cloudwatch-metric-streams-with-amazon-data-firehose/#transformation-lambda) Coralogix integration.

To install it automatically, you have a choice of 3 options:

1. [Coralogix UI Integration](https://coralogix.com/docs/integrations/aws/amazon-data-firehose/aws-cloudwatch-metric-streams-with-amazon-data-firehose/#configuration) (uses CloudFormation)
2. [CloudFormation Template](https://github.com/coralogix/cloudformation-coralogix-aws/blob/master/aws-integrations/firehose-metrics/template.yaml)
3. [Terraform Module](https://github.com/coralogix/terraform-coralogix-aws/tree/master/examples/firehose-metrics)

### Manual Installation
1. Download the `bootstrap.zip` file from the [releases](https://github.com/coralogix/cloudwatch-metric-streams-lambda-transformation/releases) page. Unless instructed otherwise, we recommend downloading the latest release. Alterantively, you can test, lint and build the zipped Lambda function by yourself by running `make all`.
2. Create a new AWS Lambda function in your designated region with the following parameters:
    - Runtime: `Custom runtime on Amazon Linux 2`
    - Handler: `bootstrap`
    - Architecture: `arm64` (but you can also build the function for `x86_64`)
3. Upload the `bootstrap.zip` file as the code source.
4. Make sure to set the memory. We recommend starting with `128 MB` and, depending on the number of metrics you export and speed of Lambda processinr, see if you need to increase it.
5. Adjust the role of the Lambda function as described below in section [Necessary permissions](###necessary-permissions).
6. Optionally, add environment variables to configure the Lambda, as described in the [Configuration](###configuration) section.
7. The Lambda function is ready to be used as in [Amazon Data Firehose Data Transformation](https://docs.aws.amazon.com/firehose/latest/dev/data-transformation.html?icmpid=docs_console_unmapped). Please note the function ARN and provide it in the relevant section of the Amazon Data Firehose configuration.

Depending on the size of your setup, we also recommend to accordingly adjust your Lambda [buffer hint](https://docs.aws.amazon.com/firehose/latest/dev/data-transformation.html) and Amazon Data Firehose [buffer size](https://docs.aws.amazon.com/firehose/latest/dev/basic-deliver.html#frequency) configuration. For most optimal experience, we recommend setting the Lambda buffer hint to `0.2 MB` and Kinsis Data Firehose buffer size to `1 MB`. **Beware that this might cause more frequent Lambda runs, which might result in higher costs**.

## Migrating from `Go 1.x` runtime to custom runtime on Amazon Linux 2
Please beware that the `Go 1.x` runtime will be [deprecated](https://aws.amazon.com/blogs/compute/migrating-aws-lambda-functions-from-the-go1-x-runtime-to-the-custom-runtime-on-amazon-linux-2/) at the end of December 2023. If you were previously using this Lambda function with the `Go 1.x`, you will need to migrate the function in accordance with the instructions in the [AWS documentation](https://aws.amazon.com/blogs/compute/migrating-aws-lambda-functions-from-the-go1-x-runtime-to-the-custom-runtime-on-amazon-linux-2/).

### Configuration
There is a couple of configuration options that can be set via environment variables:

| Environment variable             | Default |Possible values   | Description   |
|----------------------------------|---------|------------------|---------------|
| `LOG_LEVEL`                      | `info`  | `debug`          | Sets log level.
| `CONTINUE_ON_RESOURCE_FAILURE`   | `true`  | `false`          | Determines whether to continue on a failed API call to obtain resources. If set to true (by default), the Lambda will skip enriching the metrics with tags and return metrics without tags. If set to false, the Lambda will terminate and the metrics won't be exported to Amazon Data Firehose.
| `FILE_CACHE_ENABLED`             | `true`  | `false`          | Enables caching of resources to local file. See [Caching resources](###caching-resources) for more details.
| `FILE_CACHE_PATH`                | `/tmp`  | `<file_path>`    | Sets the path to directory where to cache resources. See [Caching resources](###caching-resources) for more details.
| `FILE_CACHE_EXPIRATION`          | `1h`    | `<duration>`     | Sets the expiration time for the cached resources. See [Caching resources](###caching-resources) for more details.

### Necessary permissions
The Lambda will use it's [execution role](https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html) to call other AWS APIs. You need to therefore ensure your Lambda's role has following permissions. You can use the following JSON to create an inline policy for your role, to grant all necessary permissions:
```
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Action": [
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
        "storagegateway:ListGateways",
        "storagegateway:ListTagsForResource"
      ],
      "Effect": "Allow",
      "Resource": "*"
    }
  ]
}
```

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
