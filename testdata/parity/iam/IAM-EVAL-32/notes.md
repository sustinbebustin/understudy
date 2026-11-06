# IAM-EVAL-32

Date: 2026-11-03 (first run 03:16-03:25Z; reruns 16:40-16:47Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root); the 13.1 IAM-user switch was not run.
Recipe fixes: none.

- AmazonSQSFullAccess: CreateDate=UpdateDate 2015-02-06T18:41:07Z, v1 (default): matches 10.3 at minute precision.
- AWSLambdaBasicExecutionRole: Path /service-role/, CreateDate=UpdateDate 2015-04-09T15:03:43Z, v1: matches 10.3.
- AmazonS3FullAccess: CreateDate 2015-02-06T18:40:58Z, UpdateDate 2021-09-27T20:16:37Z, v2 default, document actions s3:* and s3-object-lambda:*: matches 10.3. v1 document is s3:* only.
- Description vs the page: n/a (the page text is not quoted in the doc; IAM values: SQS "Provides full access to Amazon SQS via the AWS Management Console.", Lambda "Provides write permissions to CloudWatch Logs.", S3 "Provides full access to all buckets via the AWS Management Console.").
- AmazonSSMServiceRolePolicy IsAttachable: true (Path /aws-service-role/, v17, UpdateDate 2026-06-26T19:12:18Z).
