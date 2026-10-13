# Policy generation

understudy does not enforce IAM. It records, for every call, which principal made it, which IAM actions the operation needs and which resources it touched. From that record it prints the least-privilege identity policy each principal needs, and the resource-policy statements understudy's own deliveries would need on AWS. Run your tests, then read off the policy.

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
arn:aws:iam::000000000000:role/worker  direct  s3:PutObject        arn:aws:s3:::demo-bucket/from-worker.txt          1      0         2027-01-01T00:01:18Z
arn:aws:iam::000000000000:role/worker  direct  sqs:ReceiveMessage  arn:aws:sqs:us-east-1:000000000000:orders-worker  1      0         2027-01-01T00:01:19Z
arn:aws:iam::000000000000:role/worker  direct  sqs:SendMessage     arn:aws:sqs:us-east-1:000000000000:orders-worker  1      0         2027-01-01T00:01:18Z
...
```

`MODE` is `direct` for calls the principal made; `caller` for calls a service made with the principal's identity, such as S3 calling KMS for an SSE-KMS object; and `execution-role` for calls understudy made as a function's role, such as creating its log group, writing its logs or polling its SQS event source. `--service sqs` narrows to one IAM prefix. The usage lists exact ARNs, an S3 object call's object included.

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

The policy has one statement per resource, with exact ARNs, except that S3 objects are granted per bucket as `arn:aws:s3:::<bucket>/*`. `Resource: "*"` appears only for an action of the called service whose call touched no resource of that service, such as `sqs:ListQueues` or `sts:GetCallerIdentity`. Calls that failed are included, because handled failures need permissions too; injected faults are not. Writing and reading an SSE-KMS object records `kms:GenerateDataKey` and `kms:Decrypt` on the key for the caller, as AWS requires.

The second part lists the resource-policy statements that SNS, S3, EventBridge and Lambda deliveries would need on AWS: a queue policy allowing the topic, a function permission allowing the rule. understudy attempts those deliveries anyway, and also journals an `advisory` event for each.

## Review before use

The output is built from what your tests exercised. Check it before it goes into a real account. What each call records:

- **The operation's primary action, always**, on the resources the call touched: `s3:PutObject` for `PutObject`, `sqs:SendMessage` for `SendMessageBatch`.
- **Other actions of the same service, by service.** In S3, Lambda, STS, EventBridge and CloudWatch Logs, every operation for which AWS's Service Reference lists more than one action of its service records the others only when the request's members need them. A `PutObject` with `x-amz-tagging` adds `s3:PutObjectTagging`, one with an ACL or grant header `s3:PutObjectAcl`; a `GetObject` of a version adds `s3:GetObjectVersion`; an `AssumeRole` with session tags adds `sts:TagSession`, one with a source identity `sts:SetSourceIdentity`; a `CreateFunction` with tags adds `lambda:TagResource`; a `GetLogEvents` with `unmask` adds `logs:Unmask`. A plain `PutObject` records `s3:PutObject` alone. In SQS, SNS, IAM, KMS, Secrets Manager, CloudWatch and DynamoDB, an operation records every action of its service the Service Reference lists for it, whether the request used it or not: `CreateQueue` brings in `sqs:TagQueue`, `CreateRole` `iam:TagRole`, `GetItem` `dynamodb:ReadDataForReplication`. Remove the ones your code does not use.
- **Actions of another service only on a resource the call touched.** `CreateFunction` and `UpdateFunctionConfiguration` record `iam:PassRole` on the role they pass, as do EventBridge `PutRule` and `PutTargets` with a `RoleArn` and S3 `PutBucketReplication`; a call that passes no role records none. Actions with no such resource, such as `s3-object-lambda:PutObject` on a `PutObject` that touched no Object Lambda access point, are never recorded.
- **Every action of a call on every resource it touched.** The record cannot tell which resource each action needed, so a call that touches several resources of one service is granted each of its actions on each of them. A `CopyObject` grants `s3:PutObject` on the source bucket's objects as well as the destination's, and `s3:GetObject` on the destination's as well as the source's; a `DeleteObjects` with one versioned key grants `s3:DeleteObjectVersion` on every key it named; a `CreateFunction` with a capacity provider grants `lambda:CreateFunction` on the provider and `lambda:PassCapacityProvider` on the function. Trim those statements to the pairs your code needs.
- **S3 objects per bucket.** Object operations are recorded on the object's ARN (`iam usage` and `journal` show it), and the policy grants them on `arn:aws:s3:::<bucket>/*`. Narrow that to a key prefix if your code only uses one.
- **Lambda log delivery** records `logs:CreateLogGroup`, `logs:CreateLogStream` and `logs:PutLogEvents` for the function's execution role.

Actions AWS needs that the record does not yet carry; add them yourself:

- `iam:PassRole` on the delivery status roles SNS `CreateTopic`, `SetTopicAttributes`, `Subscribe` and `SetSubscriptionAttributes` pass in their attributes (such as `LambdaSuccessFeedbackRoleArn` or `SubscriptionRoleArn`).
- `s3:GetObjectVersionAttributes` on a `GetObjectAttributes` with a version ID, which the Service Reference does not list for the operation.
- `s3:GetObjectVersion` on a `HeadObject` with a version ID, which the Service Reference does not list for the operation.

Only code paths your tests ran appear. A branch that never ran, or a request member your tests never sent, needs permissions the record cannot know.
