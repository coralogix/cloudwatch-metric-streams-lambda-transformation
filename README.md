 # CloudWatch metric streams Lambda transformation

## About The Project
This Lambda function can be used as a Kinesis Firehose transformation function, to enrich the metrics from CloudWatch metric streams with AWS resource tags.

- Accepts Kinesis Firehose events with metric data in [OTLP v0.7, size-delimited format](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch-metric-streams-formats-opentelemetry.html)
- Obtains AWS resource information through the [AWS tagging API](https://docs.aws.amazon.com/resourcegroupstagging/latest/APIReference/overview.html) and related APIs (API Gateway, EC2...)
- Associates CloudWatch metrics with particular resources and enriches the metric labels with resource tags, based on the [Yet Another CloudWatch Exporter](https://github.com/nerdswords/yet-another-cloudwatch-exporter) library
- Returns Kinesis Firehose response with transformed record in OTLP v0.7, size-delimited format, for further processing and exporting to Coralogix (or other) destination by the Kinesis stream

### Installation and usage
1. Test, lint and build the zipped Lambda function by running `make all`.
2. Create a new AWS Lambda function in your designated region with the following parameters:
    - Runtime: `Go 1.x`
    - Handler: `function`
    - Architecture: `x86_64` (but you can also build the function for `arm64`)
3. Upload the `function.zip` file as the code source.
4. Make sure to set the memory to `128 MB`, as the Lambda will not require more memory.
5. Adjust the role of the Lambda function as described below in section [Necessary permissions](##Necessary_permissions).
6. Optionally, add environment variables to configure the Lambda, as described in the [Configuration](##Configuration) section.
7. The Lambda function is ready to be used as in [Kinesis Data Firehose Data Transformation](https://docs.aws.amazon.com/firehose/latest/dev/data-transformation.html?icmpid=docs_console_unmapped). Please note the function ARN and provide it in the relevant section of the Kinesis Data Firehose configuration.

### Configuration
There is a couple of configuration options that can be set via environment variables:

| Environment variable             | Default |Possible values   | Description   |
|----------------------------------|---------|------------------|---------------|
| `LOG_LEVEL`                      | `info`  | `debug`          | Sets log level.
| `CONTINUE_ON_RESOURCE_FAILURE`   | `true`  | `false`          | Determines whether to continue on a failed API call to obtain resources. If set to true (by default), the Lambda will skip enriching the metrics with tags and return metrics without tags. If set to false, the Lambda will terminate and the metrics won't be exported to Kinesis Data Firehose.

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

## Extra costs and usage of AWS APIs
There are couple of costs connected with usage of Lambda transformation. Below, these costs are described in details (based on examples and pricing in the US East region). This example assumes a user will be exporting metrics from all namespaces and that the metrics updates are coming once per minute (which is not true fro all AWS metrics, see [this thread](https://serverfault.com/questions/1003344/aws-cloudwatch-metrics-are-there-convergence-delays?newreg=80710344c54e4a8397eac3acfdb01941) to learn more). The example is based on a region with ~5000 metrics. For detailed pricing information see [here](https://aws.amazon.com/lambda/pricing/). This calculation is for informative purposes and it is valid as of March 2023 to the best of our knowledge.

- The costs connected with Lambda
  - With assumed update every minute (accounting for 43,200 invocations per month), the costs for invocation (every 1,000,000 requests / 0.20 USD), comes to **0.01 USD**. 
  - For computation costs, it's not easy to estimate the duration of Lambda - this will depend on some factors like cold vs. warm, amount of metrics etc. We’re using some guesswork and more pessimistic estimate here for the duration, so we round the duration to 1 second (but realistically our tests show the function to take anywhere between 300-700 ms). This comes up to 5400 GB seconds per month (with configured 128 MB of memory), which equals to **0.1 USD** per month.

- The costs connected with calling AWS APIs
  - Depending on the namespaces you will stream your CloudWatch metrics from, in addition to the [resource tagging API](https://aws.amazon.com/blogs/aws/new-aws-resource-tagging-api/), the Lambda can make call to a number of AWS APIs, depending on the services in questions. However, all of these API calls should be **free**.

## Todos
- [ ] Support for JSON data input