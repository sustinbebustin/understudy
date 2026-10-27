# Policy generation

understudy records, for every call, which principal made it, which IAM actions the operation needs and which resources it touched. From that record it prints the least-privilege identity policy each principal needs, and the resource-policy statements understudy's own deliveries would need on AWS. Run your tests, then read off the policy.

`--iam-enforce` (`UNDERSTUDY_IAM_ENFORCE`, or `{"iam": {"enforce": "off"}}` in the file) decides whether understudy also evaluates the policies it stores:

- `off`, the default, evaluates nothing.
- `advise` evaluates each call against the caller's identity-based policies, permissions boundary and session policies and the resource's policy, serves it as `off` does, and journals an `access-would-deny` advisory for each call AWS would deny.
- `enforce` answers those denials with each service's denial error.

Any other value is refused at start:

```sh
understudy serve --iam-enforce on
```

```text
understudy serve: --iam-enforce: "on" is not off, advise or enforce; set iam.enforce to off, advise or enforce
Run 'understudy serve --help' for usage and examples.
```

## Enforce

Under `enforce`, a user with no policy is denied. As the account root (the dummy keys), create the user and its key:

```sh
understudy serve --iam-enforce enforce
aws iam create-user --user-name app
aws iam create-access-key --user-name app
```

With the returned key exported, the user's call is refused:

```sh
aws sqs list-queues
```

```text
aws: [ERROR]: An error occurred (AccessDenied) when calling the ListQueues operation: User: arn:aws:iam::000000000000:user/app is not authorized to perform: sqs:ListQueues because no identity-based policy allows the sqs:ListQueues action
```

Once root puts a policy allowing `sqs:ListQueues` on the user (`aws iam put-user-policy`), the same call succeeds. Under `advise` the call is served and the journal says what AWS would have answered:

```sh
understudy journal --kind advisory
```

```text
4      2026-10-27T00:44:20.070175997Z  advisory       000000000000/us-east-1 access-would-deny: User: arn:aws:iam::000000000000:user/app is not authorized to perform: sqs:CreateQueue because no identity-based policy allows the sqs:CreateQueue action; decided by identity-based policy for arn:aws:iam::000000000000:user/app calling sqs:CreateQueue on arn:aws:sqs:us-east-1:000000000000:orders; iam.enforce is advise, so understudy served the call
```

A condition on a key understudy cannot compute, such as `aws:SourceIp`, evaluates as absent, and each evaluation that tests it journals a `condition-key-unsupplied` advisory naming the key. Which texts and statuses each service answers with, and which of them are inferred, is listed by `understudy coverage --switches` (the `*.denial.*` switches).

Deliveries are decided the same way. SNS, S3 and EventBridge call the target as their service principal, with `aws:SourceArn` the topic, bucket or rule, and `aws:SourceAccount` as each sends it: the topic's account, the bucket owner's, the rule's to a queue or topic, and none to a function. A delivery the target's policy refuses is not retried. It goes to the subscription's or target's dead-letter queue when one is set, and an EventBridge dead letter carries `ERROR_CODE` `NO_PERMISSIONS`; otherwise it is dropped. Subscribe and PutTargets neither check nor change the target's policy. With a queue that has no policy subscribed to a topic, a published message is dropped:

```sh
aws sqs create-queue --queue-name orders
aws sns create-topic --name orders
aws sns subscribe --topic-arn arn:aws:sns:us-east-1:000000000000:orders --protocol sqs --notification-endpoint arn:aws:sqs:us-east-1:000000000000:orders
aws sns publish --topic-arn arn:aws:sns:us-east-1:000000000000:orders --message hello
understudy journal --kind delivery
```

```text
8      2026-10-27T01:22:00.236664666Z  delivery       000000000000/us-east-1 sns-to-sqs orders -> orders attempt 1 dropped: no dead-letter queue is set and the target refused it with a client-side error, which is not retried: com.amazon.coral.service#AccessDenied: User: sns.amazonaws.com is not authorized to perform: sqs:SendMessage because no resource-based policy allows the sqs:SendMessage action
```

The statements such deliveries need are the second part of `understudy iam policy` (below). Which delivery choices are inferred is listed by `understudy coverage --switches` (the `*.delivery.*`, `sns.subscribe.*`, `events.put-targets.*` and `s3.notification.*` switches).

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
  arn:aws:sqs:us-east-1:000000000000:orders-worker must allow sns.amazonaws.com to call sqs:SendMessage from arn:aws:sns:us-east-1:000000000000:orders (aws:SourceAccount 000000000000)
```

The policy has one statement per resource, with exact ARNs, except that S3 objects are granted per bucket as `arn:aws:s3:::<bucket>/*`. `Resource: "*"` appears only for an action of the called service whose call named no resource of that service, such as `sqs:ListQueues`. `sts:GetCallerIdentity` needs no permission, so the policy leaves it out; `iam usage` still lists its calls. Calls that failed are included, because handled failures need permissions too; injected faults are not, nor are calls `--sigv4 verify` refused at authentication (a bad signature, an unknown key, a missing or expired session token), which on AWS never reach IAM. Writing and reading an SSE-KMS object records `kms:GenerateDataKey` and `kms:Decrypt` on the key for the caller, as AWS requires.

The second part lists the resource-policy statements that SNS, S3, EventBridge and Lambda deliveries would need on AWS: a queue policy allowing the topic, a function permission allowing the rule. Each names the `aws:SourceAccount` the delivery sends, when it sends one; EventBridge sends none to a function, so a function permission conditioned on `aws:SourceAccount` would refuse the rule. Under `off` understudy attempts those deliveries anyway, and journals an `advisory` event for each one the target's stored policy would refuse; under `enforce` the target refuses them (see Enforce).

Only deliveries are listed. Two kinds of call are the service acting on its own behalf: S3 checking that each destination exists when you configure a bucket's notifications, and DynamoDB deleting expired TTL items. On AWS, S3 validates a destination by sending it a test message, which needs the same grant as a delivery, so it adds nothing to the policy; understudy's existence check is not that message. Neither `iam usage` nor the resource policies list these calls. So a bucket that notifies a queue needs only `sqs:SendMessage` from the bucket. With `queue-policy.json` holding the queue's policy as its `Policy` attribute and `notification.json` sending `s3:ObjectCreated:*` to the queue:

```json
{"Policy":"{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"Service\":\"s3.amazonaws.com\"},\"Action\":\"sqs:SendMessage\",\"Resource\":\"arn:aws:sqs:us-east-1:000000000000:uploads\",\"Condition\":{\"ArnLike\":{\"aws:SourceArn\":\"arn:aws:s3:::uploads\"}}}]}"}
```

```json
{"QueueConfigurations":[{"QueueArn":"arn:aws:sqs:us-east-1:000000000000:uploads","Events":["s3:ObjectCreated:*"]}]}
```

```sh
aws sqs create-queue --queue-name uploads
aws sqs set-queue-attributes --queue-url http://localhost:4566/000000000000/uploads --attributes file://queue-policy.json
aws s3api create-bucket --bucket uploads
aws s3api put-bucket-notification-configuration --bucket uploads --notification-configuration file://notification.json
aws s3 cp hello.txt s3://uploads/in/hello.txt
understudy settle
understudy iam policy --principal arn:aws:iam::000000000000:root
```

```text
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:CreateBucket","s3:PutBucketNotification"],"Resource":"arn:aws:s3:::uploads"},...]}

Resource policies the recorded deliveries need:
  arn:aws:sqs:us-east-1:000000000000:uploads must allow s3.amazonaws.com to call sqs:SendMessage from arn:aws:s3:::uploads (aws:SourceAccount 000000000000)
```

The destination check is still in the journal: `understudy journal --operation sqs.GetQueueUrl --output json` shows it with `"principal":"s3.amazonaws.com","mode":"platform:s3.amazonaws.com"`.

## Review before use

The output is built from what your tests exercised. Check it before it goes into a real account. What each call records:

- **The operation's primary action, always**, on the resources it applies to: `s3:PutObject` for `PutObject`, `sqs:SendMessage` for `SendMessageBatch`.
- **Other actions of the same service only when the request needs them.** Every operation for which AWS's Service Reference lists more than one action of its service records the others only when the request's members need them. A `PutObject` with `x-amz-tagging` adds `s3:PutObjectTagging`, one with an ACL or grant header `s3:PutObjectAcl`; a `GetObject` of a version adds `s3:GetObjectVersion`; an `AssumeRole` with session tags adds `sts:TagSession`, one with a source identity `sts:SetSourceIdentity`; a `CreateQueue`, `CreateRole`, `CreateKey` or `CreateFunction` with tags adds the service's tag action; a `GetLogEvents` with `unmask` adds `logs:Unmask`; a `ReEncrypt` adds `kms:ReEncryptTo`; a PartiQL `INSERT` adds `dynamodb:PartiQLInsert`. A plain `PutObject` records `s3:PutObject` alone. DynamoDB's replication actions, such as `dynamodb:ReadDataForReplication` on `GetItem`, are never recorded. Two operations record a primary AWS does not always need: `TransactWriteItems` records `dynamodb:PutItem` on every table it names, and the PartiQL operations record `dynamodb:PartiQLSelect`, each with every PartiQL action on `"*"` (the coverage notes list both).
- **Actions of another service only on a resource the call touched.** `CreateFunction` and `UpdateFunctionConfiguration` record `iam:PassRole` on the role they pass, as do EventBridge `PutRule` and `PutTargets` with a `RoleArn`, SNS `CreateTopic` with a delivery status role and S3 `PutBucketReplication`; a call that passes no role records none. Actions with no such resource, such as `s3-object-lambda:PutObject` on a `PutObject` that touched no Object Lambda access point, are never recorded.
- **Each action on the resources it applies to, where understudy knows them.** A `CopyObject` records `s3:GetObject` on the source bucket's objects and `s3:PutObject` on the destination's; a `ReEncrypt` `kms:ReEncryptFrom` on the key that sealed the ciphertext and `kms:ReEncryptTo` on the destination key; a `StartMessageMoveTask` its receive, delete and attribute actions on the dead-letter queue and `sqs:SendMessage` on where the messages go; an `AddRoleToInstanceProfile` `iam:PassRole` on the role and its own action on the profile. Other calls that touch several resources of one service are granted each of their actions on each of them: a `DeleteObjects` with one versioned key grants `s3:DeleteObjectVersion` on every key it named; a `CreateFunction` with a capacity provider grants `lambda:CreateFunction` on the provider and `lambda:PassCapacityProvider` on the function. Trim those statements to the pairs your code needs.
- **`s3:ListBucket` for a missing key.** A `GetObject`, `HeadObject` or `GetObjectAttributes` of a key that does not exist records `s3:ListBucket` on the bucket: AWS answers `404 NoSuchKey` to a caller with it and `403 AccessDenied` to one without, so a test that reads a missing key needs it.
- **S3 objects per bucket.** Object operations touch the object's ARN (`journal` shows it), and are recorded, listed by `iam usage` and granted on `arn:aws:s3:::<bucket>/*`. Narrow that to a key prefix if your code only uses one.
- **Lambda log delivery** records `logs:CreateLogGroup`, `logs:CreateLogStream` and `logs:PutLogEvents` for the function's execution role.

Actions AWS needs that the record does not yet carry; add them yourself:

- `iam:PassRole` on the roles SNS `SetTopicAttributes`, `Subscribe` and `SetSubscriptionAttributes` pass in their attributes (such as `LambdaSuccessFeedbackRoleArn` or `SubscriptionRoleArn`).
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
