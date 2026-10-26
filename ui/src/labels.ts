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
};

// label returns table's label for name, or name when it has none.
export function label(table: Readonly<Record<string, string>>, name: string): string {
  const text = Object.hasOwn(table, name) ? table[name] : undefined;
  return typeof text === "string" && text !== "" ? text : name;
}
