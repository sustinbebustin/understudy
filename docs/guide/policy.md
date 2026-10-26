# Policy generation

understudy does not enforce IAM. It records, for every call, which principal made it, which IAM actions the operation needs and which resources it touched. From that record it prints the least-privilege identity policy each principal needs, and the resource-policy statements understudy's own deliveries would need on AWS. Run your tests, then read off the policy.

`--iam-enforce` (`UNDERSTUDY_IAM_ENFORCE`, or `{"iam": {"enforce": "off"}}` in the file) takes only `off`, the default, in this build. `advise` and `enforce` are refused at start rather than accepted and ignored:

```sh
understudy serve --iam-enforce enforce
```

```text
understudy serve: --iam-enforce: "enforce" is not supported: IAM enforcement is not built yet, so this build accepts only off; remove the setting or set it to off
```

## Make calls as the principal

Calls signed with understudy's dummy keys act as the account root. To record a role, call as the role: assume it, or run the code in a Lambda function whose execution role it is, since understudy mints credentials for each function's role.

With `trust.json` allowing the account to assume the role:

```json
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"sts:AssumeRole"}]}
```

```sh
aws iam create-role --role-name worker --assume-role-policy-document file://trust.json
aws sts assume-role --role-arn arn:aws:iam::000000000000:role/worker --role-session-name demo
```

With the returned credentials exported, the worker sends a message, uploads an object and receives a message:

```sh
aws sqs send-message --queue-url http://localhost:4566/000000000000/orders-worker --message-body hi
aws s3 cp hello.txt s3://demo-bucket/from-worker.txt
aws sqs receive-message --queue-url http://localhost:4566/000000000000/orders-worker
```

## Read the record

```sh
understudy iam usage --principal arn:aws:iam::000000000000:role/worker
```

```text
PRINCIPAL                              MODE    ACTION              RESOURCE                                          CALLS  FAILURES  LAST
arn:aws:iam::000000000000:role/worker  direct  s3:PutObject        arn:aws:s3:::demo-bucket/*                        1      0         2027-01-01T00:01:18Z
arn:aws:iam::000000000000:role/worker  direct  sqs:ReceiveMessage  arn:aws:sqs:us-east-1:000000000000:orders-worker  1      0         2027-01-01T00:01:19Z
arn:aws:iam::000000000000:role/worker  direct  sqs:SendMessage     arn:aws:sqs:us-east-1:000000000000:orders-worker  1      0         2027-01-01T00:01:18Z
...
```

`MODE` is `direct` for calls the principal made; `caller` for calls a service made with the principal's identity, such as S3 calling KMS for an SSE-KMS object; and `execution-role` for calls understudy made as a function's role, such as creating its log group, writing its logs or polling its SQS event source. `--service sqs` narrows to one IAM prefix. The usage lists exact ARNs, except that S3 object calls are listed per bucket as `arn:aws:s3:::<bucket>/*`, so the record grows with the buckets a run used, not its keys; `understudy journal` shows each call's exact object.

## Print the policy

```sh
understudy iam policy --principal arn:aws:iam::000000000000:role/worker
understudy iam policy --principal arn:aws:iam::000000000000:role/worker --output json
```

```text
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::demo-bucket/*"},{"Effect":"Allow","Action":["sqs:ReceiveMessage","sqs:SendMessage"],"Resource":"arn:aws:sqs:us-east-1:000000000000:orders-worker"},...]}

Resource policies the recorded deliveries need:
  arn:aws:sqs:us-east-1:000000000000:orders-worker must allow sns.amazonaws.com to call sqs:SendMessage from arn:aws:sns:us-east-1:000000000000:orders
```

The policy has one statement per resource, with exact ARNs, except that S3 objects are granted per bucket as `arn:aws:s3:::<bucket>/*`. `Resource: "*"` appears only for an action of the called service whose call touched no resource of that service, such as `sqs:ListQueues` or `sts:GetCallerIdentity`. Calls that failed are included, because handled failures need permissions too; injected faults are not, nor are calls `--sigv4 verify` refused at authentication (a bad signature, an unknown key, a missing or expired session token), which on AWS never reach IAM. Writing and reading an SSE-KMS object records `kms:GenerateDataKey` and `kms:Decrypt` on the key for the caller, as AWS requires.

The second part lists the resource-policy statements that SNS, S3, EventBridge and Lambda deliveries would need on AWS: a queue policy allowing the topic, a function permission allowing the rule. understudy attempts those deliveries anyway, and also journals an `advisory` event for each.

## Review before use

The output is built from what your tests exercised. Check it before it goes into a real account. What each call records:

- **The operation's primary action, always**, on the resources the call touched: `s3:PutObject` for `PutObject`, `sqs:SendMessage` for `SendMessageBatch`.
- **Other actions of the same service, by service.** In S3, Lambda, STS, EventBridge and CloudWatch Logs, every operation for which AWS's Service Reference lists more than one action of its service records the others only when the request's members need them. A `PutObject` with `x-amz-tagging` adds `s3:PutObjectTagging`, one with an ACL or grant header `s3:PutObjectAcl`; a `GetObject` of a version adds `s3:GetObjectVersion`; an `AssumeRole` with session tags adds `sts:TagSession`, one with a source identity `sts:SetSourceIdentity`; a `CreateFunction` with tags adds `lambda:TagResource`; a `GetLogEvents` with `unmask` adds `logs:Unmask`. A plain `PutObject` records `s3:PutObject` alone. In SQS, SNS, IAM, KMS, Secrets Manager, CloudWatch and DynamoDB, an operation records every action of its service the Service Reference lists for it, whether the request used it or not: `CreateQueue` brings in `sqs:TagQueue`, `CreateRole` `iam:TagRole`, `GetItem` `dynamodb:ReadDataForReplication`. Remove the ones your code does not use.
- **Actions of another service only on a resource the call touched.** `CreateFunction` and `UpdateFunctionConfiguration` record `iam:PassRole` on the role they pass, as do EventBridge `PutRule` and `PutTargets` with a `RoleArn` and S3 `PutBucketReplication`; a call that passes no role records none. Actions with no such resource, such as `s3-object-lambda:PutObject` on a `PutObject` that touched no Object Lambda access point, are never recorded.
- **Every action of a call on every resource it touched.** The record cannot tell which resource each action needed, so a call that touches several resources of one service is granted each of its actions on each of them. A `CopyObject` grants `s3:PutObject` on the source bucket's objects as well as the destination's, and `s3:GetObject` on the destination's as well as the source's; a `DeleteObjects` with one versioned key grants `s3:DeleteObjectVersion` on every key it named; a `CreateFunction` with a capacity provider grants `lambda:CreateFunction` on the provider and `lambda:PassCapacityProvider` on the function. Trim those statements to the pairs your code needs.
- **S3 objects per bucket.** Object operations touch the object's ARN (`journal` shows it), and are recorded, listed by `iam usage` and granted on `arn:aws:s3:::<bucket>/*`. Narrow that to a key prefix if your code only uses one.
- **Lambda log delivery** records `logs:CreateLogGroup`, `logs:CreateLogStream` and `logs:PutLogEvents` for the function's execution role.

Actions AWS needs that the record does not yet carry; add them yourself:

- `iam:PassRole` on the delivery status roles SNS `CreateTopic`, `SetTopicAttributes`, `Subscribe` and `SetSubscriptionAttributes` pass in their attributes (such as `LambdaSuccessFeedbackRoleArn` or `SubscriptionRoleArn`).
- `s3:GetObjectVersionAttributes` on a `GetObjectAttributes` with a version ID, which the Service Reference does not list for the operation.
- `s3:GetObjectVersion` on a `HeadObject` with a version ID, which the Service Reference does not list for the operation.

Only code paths your tests ran appear. A branch that never ran, or a request member your tests never sent, needs permissions the record cannot know.

## AWS managed policies

understudy ships the AWS managed policies the AWS Managed Policy Reference Guide lists, in `third_party/aws-managed-policies`; its `SOURCE.json` records the date they were retrieved. IAM reads and attaches them by their AWS ARNs, so code and configurations that name one work unchanged:

```sh
aws iam get-policy --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole \
  --query 'Policy.[Path,DefaultVersionId,UpdateDate]' --output text
```

```text
/service-role/	v1	2015-04-09T15:03:00+00:00
```

```sh
aws iam get-policy-version --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole \
  --version-id v1 --query 'PolicyVersion.Document.Statement[0].Action'
aws iam attach-role-policy --role-name worker --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
```

```text
[
    "logs:CreateLogGroup",
    "logs:CreateLogStream",
    "logs:PutLogEvents"
]
```

`aws iam list-policies --scope AWS` lists the catalogue, `--scope All` (the default) lists it with your own policies, and `--only-attached` keeps the ones attached in the account. Attached, an AWS managed policy is stored like any other, not enforced. In Terraform, the `aws_iam_policy` data source finds one by name:

```hcl
data "aws_iam_policy" "basic_execution" {
  name = "AWSLambdaBasicExecutionRole"
}

resource "aws_iam_role_policy_attachment" "worker_basic_execution" {
  role       = aws_iam_role.worker.name
  policy_arn = data.aws_iam_policy.basic_execution.arn
}
```

An ARN the catalogue does not hold, such as a policy AWS published after it was retrieved, answers `NoSuchEntity`, and a name it does not hold fails the data source with `no matching IAM Policy found`:

```sh
aws iam attach-role-policy --role-name worker --policy-arn arn:aws:iam::aws:policy/NotAPolicy
```

```text
An error occurred (NoSuchEntity) when calling the AttachRolePolicy operation: Policy arn:aws:iam::aws:policy/NotAPolicy does not exist or is not attachable.
```

Each policy has only its default version, so another `--version-id` answers `NoSuchEntity`; its dates have minute precision and its `PolicyId` is derived from the ARN. [Differences from AWS](differences.md) lists the rest.
