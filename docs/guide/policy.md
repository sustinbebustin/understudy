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
PRINCIPAL                              MODE    ACTION                      RESOURCE                                          CALLS  FAILURES  LAST
arn:aws:iam::000000000000:role/worker  direct  s3:PutObject                arn:aws:s3:::demo-bucket                          1      0         2027-01-01T00:01:18Z
arn:aws:iam::000000000000:role/worker  direct  sqs:ReceiveMessage          arn:aws:sqs:us-east-1:000000000000:orders-worker  1      0         2027-01-01T00:01:19Z
arn:aws:iam::000000000000:role/worker  direct  sqs:SendMessage             arn:aws:sqs:us-east-1:000000000000:orders-worker  1      0         2027-01-01T00:01:18Z
...
```

`MODE` is `direct` for calls the principal made; `caller` for calls a service made with the principal's identity, such as S3 calling KMS for an SSE-KMS object; and `execution-role` for calls understudy made as a function's role, such as creating its log group or polling its SQS event source. `--service sqs` narrows to one IAM prefix.

## Print the policy

```sh
understudy iam policy --principal arn:aws:iam::000000000000:role/worker
understudy iam policy --principal arn:aws:iam::000000000000:role/worker --output json
```

```text
{"Version":"2012-10-17","Statement":[...,{"Effect":"Allow","Action":["sqs:ReceiveMessage","sqs:SendMessage"],"Resource":"arn:aws:sqs:us-east-1:000000000000:orders-worker"},{"Effect":"Allow","Action":"sts:GetCallerIdentity","Resource":"*"}]}

Resource policies the recorded deliveries need:
  arn:aws:sqs:us-east-1:000000000000:orders-worker must allow sns.amazonaws.com to call sqs:SendMessage from arn:aws:sns:us-east-1:000000000000:orders
```

The policy has one statement per resource, with exact ARNs, and `Resource: "*"` only for actions that take no resource. Calls that failed are included, because handled failures need permissions too; injected faults are not. Writing and reading an SSE-KMS object records `kms:GenerateDataKey` and `kms:Decrypt` on the key for the caller, as AWS requires.

The second part lists the resource-policy statements that SNS, S3, EventBridge and Lambda deliveries would need on AWS: a queue policy allowing the topic, a function permission allowing the rule. understudy attempts those deliveries anyway, and also journals an `advisory` event for each.

## Review before use

The output is a starting point, built from what your tests exercised. Check it before it goes into a real account:

- An operation maps to every action AWS's Service Reference lists for it, so `PutObject` brings in `s3:PutObjectAcl`, `s3:PutObjectTagging` and related actions you may not use.
- S3 object operations are recorded against the bucket ARN. AWS evaluates them against the object ARN, so change those statements' resource to `arn:aws:s3:::<bucket>/*` or narrower.
- Lambda log delivery records `logs:CreateLogGroup` and `logs:CreateLogStream` for a function's role but not `logs:PutLogEvents`, which a role needs on AWS to write its logs.
- Only code paths your tests ran appear. A branch that never ran needs permissions the record cannot know.
