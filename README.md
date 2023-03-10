 # CloudWatch metric streams Lambda transformation

## About The Project
This Lambda function can be used as a Kinesis Firehose transformation function, to enrich the metrics from CloudWatch metric streams with AWS resource tags. 
- Accepts Kinesis Firehose events with metric data in [OTLP v0.7, size-delimited format](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch-metric-streams-formats-opentelemetry.html)
- Obtains AWS resource information through the [AWS tagging API](https://docs.aws.amazon.com/resourcegroupstagging/latest/APIReference/overview.html) and related APIs
- Associates CloudWatch metrics with particular resources and enriches the metric labels with resource tags, based on the [YACE](https://github.com/nerdswords/yet-another-cloudwatch-exporter) library
- Returns Kinesis Firehose response with transformed record in OTLP v0.7, size-delimited format, for further processing and exporting to Coralogix destination by the Kinesis stream

### Installation
1. Test, lint and build the zipped Lambda function by running `make all`.
2. Create a new AWS Lambda function with Go runtime, upload the `function.zip` file
3. The Lambda function is ready to be used as in [Kinesis Data Firehose Data Transformation](https://docs.aws.amazon.com/firehose/latest/dev/data-transformation.html?icmpid=docs_console_unmapped).

## Usage
- TODO: Add any possible config options
- Incurs extra API calls to AWS APIs and potential additional costs

### Necessary permissions
- TODO: add which ones the Lambda exectuion role needs to be added by users

## Todos
- [ ] Configurations
- [ ] Error handling? Retries?
- [ ] Cross-region?
- [ ] Logging?
- [ ] Tests
- [ ] Forking YACE?