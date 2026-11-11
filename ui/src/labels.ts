// Human labels for the wire names in labels.gen.ts. Every generated name
// needs one (test/labels.test.ts); an unknown name renders as itself.

export const kindLabels: Readonly<Record<string, string>> = {
  "api-call": "API call",
  delivery: "Delivery",
  invocation: "Invocation",
  "timer-fired": "Timer fired",
  "state-change": "State change",
  "fault-injected": "Fault injected",
  "clock-changed": "Clock changed",
  advisory: "Advisory",
  unclaimed: "Unclaimed request",
  gap: "Gap",
  "admin-action": "Admin action",
  redrive: "Redrive",
};

// ref.Via: who made a call on whose behalf (internal/ref/via.go).
export const viaLabels: Readonly<Record<string, string>> = {
  "sns-delivery": "SNS delivery",
  "lambda-esm": "Lambda event source mapping",
  "events-target": "EventBridge target",
  "s3-notification": "S3 notification",
  "lambda-logs": "Lambda logs",
  "dynamodb-ttl": "DynamoDB TTL",
  timer: "Timer",
  snapshot: "Snapshot",
  admin: "Admin API",
  "kms-forward": "KMS on the caller's behalf",
  "sts-assume-role": "STS AssumeRole",
  "lambda-code": "Lambda code fetch",
  "lambda-destination": "Lambda destination",
  "apigateway-integration": "API Gateway integration",
  "sfn-log-destination": "Step Functions log destination check",
  "sfn-task": "Step Functions Lambda task",
  "cloudwatch-alarm": "CloudWatch alarm",
  "scheduler-target": "EventBridge Scheduler target",
  "ssm-parameter": "Parameter Store change event",
};

export const mechanismLabels: Readonly<Record<string, string>> = {
  "sns-to-sqs": "SNS to SQS",
  "sns-to-lambda": "SNS to Lambda",
  "s3-to-sqs": "S3 to SQS",
  "s3-to-sns": "S3 to SNS",
  "s3-to-lambda": "S3 to Lambda",
  "events-to-lambda": "EventBridge to Lambda",
  "events-to-sqs": "EventBridge to SQS",
  "events-to-sns": "EventBridge to SNS",
  "events-to-events": "EventBridge to EventBridge",
  "sns-to-http": "SNS to HTTP",
  "lambda-to-sqs": "Lambda to SQS",
  "lambda-to-sns": "Lambda to SNS",
  "lambda-to-lambda": "Lambda to Lambda",
  "lambda-to-events": "Lambda to EventBridge",
  "lambda-to-s3": "Lambda to S3",
  "esm-to-sqs": "Event source mapping to SQS",
  "esm-to-sns": "Event source mapping to SNS",
  "cloudwatch-to-sns": "CloudWatch alarm to SNS",
  "cloudwatch-to-lambda": "CloudWatch alarm to Lambda",
  "scheduler-to-lambda": "Scheduler to Lambda",
  "scheduler-to-sqs": "Scheduler to SQS",
  "scheduler-to-events": "Scheduler to EventBridge",
  "scheduler-to-sns": "Scheduler to SNS",
  "scheduler-to-states": "Scheduler to Step Functions",
};

// ids.Kind: the generator streams (internal/ids/kind.go).
export const idKindLabels: Readonly<Record<string, string>> = {
  "request-id": "Request ID",
  "sqs-message-id": "SQS message ID",
  "sqs-receipt-handle": "SQS receipt handle",
  "sns-message-id": "SNS message ID",
  "lambda-request-id": "Lambda request ID",
  "lambda-esm-id": "Lambda event source mapping ID",
  "s3-upload-id": "S3 upload ID",
  "s3-version-id": "S3 version ID",
  "s3-etag": "S3 ETag",
  "kms-key-id": "KMS key ID",
  "iam-id": "IAM unique ID",
  "access-key-id": "Access key ID",
  "secret-access-key": "Secret access key",
  "session-token": "Session token",
  "delivery-id": "Delivery ID",
  "fault-stream": "Fault probability stream",
  jitter: "Delivery jitter",
  "events-event-id": "EventBridge event ID",
  "s3-ssec-salt": "S3 SSE-C key salt",
  "lambda-record-key": "Lambda record object key",
  "apigateway-api-id": "API Gateway API ID",
  "apigateway-child-id": "API Gateway route, integration or deployment ID",
  "dynamodb-stream-event-id": "DynamoDB stream event ID",
  "shard-iterator-key": "Shard iterator key",
  "sfn-execution-name": "Step Functions execution name",
  "sfn-revision-id": "Step Functions revision ID",
  "sfn-uuid": "Step Functions States.UUID",
  "sfn-math-random": "Step Functions States.MathRandom",
  "sfn-jitter": "Step Functions retry jitter seed",
  "ses-message-id": "SES message ID",
  "ses-domain-token": "SES domain verification token",
  "ses-dkim-token": "SES DKIM token",
  "ses-link-token": "SES verification link token",
  "scheduler-execution-id": "Scheduler execution id",
  "scheduler-role-session": "Scheduler role session name",
  "scheduler-flex-offset": "Scheduler flexible-window offset",
};

// label returns table's label for name, or name when it has none.
export function label(table: Readonly<Record<string, string>>, name: string): string {
  const text = Object.hasOwn(table, name) ? table[name] : undefined;
  return typeof text === "string" && text !== "" ? text : name;
}
