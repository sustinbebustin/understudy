# Policy generation

understudy records, for every call, which principal made it, which IAM actions the operation needs and which resources it touched. From that record it prints the least-privilege identity policy each principal needs, and the resource-policy statements understudy's own deliveries would need on AWS. Run your tests, then read off the policy.

`--iam-enforce` (`UNDERSTUDY_IAM_ENFORCE`, or `{"iam": {"enforce": "off"}}` in the file) decides whether understudy also evaluates the policies it stores:

- `off`, the default, denies nothing. It journals a `policy-would-deny` advisory for each delivery the target's stored policy would refuse, and evaluates nothing else.
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

Calls signed with the dummy keys act as the account root, which has full access to its own account's resources, as on AWS: no identity policy is needed, though a resource policy's explicit `Deny` and a KMS key policy still apply to it. Under `enforce`, the account root cannot call `AssumeRole`, which AWS also refuses; under `off` and `advise` it can. So to see enforcement, call as an IAM user or a role. The loop below records what a user's code needs, prints the policy, enforces it, and shows what removing one statement breaks.

Record under `advise`. As the account root, create the user and its key:

```sh
understudy serve --iam-enforce advise
aws iam create-user --user-name app
aws iam create-access-key --user-name app
```

With the returned key exported, run the code as the user. Here that code is five calls:

```sh
aws sqs create-queue --queue-name orders
aws sqs send-message --queue-url http://localhost:4566/000000000000/orders --message-body hi
aws sqs receive-message --queue-url http://localhost:4566/000000000000/orders
aws s3api create-bucket --bucket app-uploads
aws s3api put-object --bucket app-uploads --key in/hello.txt --body hello.txt
```

Each call is served. The user has no policy yet, so each one journals what AWS would have answered:

```sh
understudy journal --kind advisory
```

```text
4      2026-10-27T03:56:12.758399069Z  advisory       000000000000/us-east-1 access-would-deny: User: arn:aws:iam::000000000000:user/app is not authorized to perform: sqs:CreateQueue because no identity-based policy allows the sqs:CreateQueue action; decided by identity-based policy for arn:aws:iam::000000000000:user/app calling sqs:CreateQueue on arn:aws:sqs:us-east-1:000000000000:orders; iam.enforce is advise, so understudy served the call
...
```

Print the policy the record implies (see [Print the policy](#print-the-policy)), and save it as a file:

```sh
understudy iam policy --principal arn:aws:iam::000000000000:user/app
understudy iam policy --principal arn:aws:iam::000000000000:user/app --output json | jq .document > app-policy.json
```

```text
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:CreateBucket","Resource":"arn:aws:s3:::app-uploads"},{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::app-uploads/*"},{"Effect":"Allow","Action":["sqs:CreateQueue","sqs:ReceiveMessage","sqs:SendMessage"],"Resource":"arn:aws:sqs:us-east-1:000000000000:orders"}]}
```

Enforce it. Restart with `--iam-enforce enforce`. Without `--persist`, the restart starts empty, so as the root create the user and a key again, and attach the policy:

```sh
understudy serve --iam-enforce enforce
aws iam create-user --user-name app
aws iam create-access-key --user-name app
aws iam put-user-policy --user-name app --policy-name app --policy-document file://app-policy.json
```

Export the new key and run the same five calls. All five succeed, and nothing is denied:

```sh
understudy journal --kind advisory
```

```text
no events matched
```

Now remove the statement for the bucket's objects, attach the shorter policy as the root, and repeat the upload as the user:

```sh
jq 'del(.Statement[] | select(.Resource == "arn:aws:s3:::app-uploads/*"))' app-policy.json > app-policy-trimmed.json
aws iam put-user-policy --user-name app --policy-name app --policy-document file://app-policy-trimmed.json
aws s3api put-object --bucket app-uploads --key in/hello.txt --body hello.txt
```

```text
aws: [ERROR]: An error occurred (AccessDenied) when calling the PutObject operation: User: arn:aws:iam::000000000000:user/app is not authorized to perform: s3:PutObject because no identity-based policy allows the s3:PutObject action
```

The text follows AWS's documented form for an implicit denial. The journal records the call that was refused, and the policy that refused it:

```sh
understudy journal --kind advisory
```

```text
11     2026-10-27T03:59:03.154647689Z  advisory       000000000000/us-east-1 access-denied: User: arn:aws:iam::000000000000:user/app is not authorized to perform: s3:PutObject because no identity-based policy allows the s3:PutObject action; decided by identity-based policy for arn:aws:iam::000000000000:user/app calling s3:PutObject on arn:aws:s3:::app-uploads/in/hello.txt
```

### Ask without calling

`POST /_understudy/v1/iam/evaluate` decides `(action, resource)` pairs for a principal the way `advise` and `enforce` decide calls, but without making the calls, and it journals nothing. It works under every mode, `off` included. With the shorter policy attached:

```sh
curl -s --json '{"principal":"arn:aws:iam::000000000000:user/app","requests":[{"action":"sqs:SendMessage","resource":"arn:aws:sqs:us-east-1:000000000000:orders"},{"action":"s3:PutObject","resource":"arn:aws:s3:::app-uploads/in/hello.txt"}]}' \
  http://localhost:4566/_understudy/v1/iam/evaluate
```

```text
{"results":[{"action":"sqs:SendMessage","resource":"arn:aws:sqs:us-east-1:000000000000:orders","decision":"allowed"},{"action":"s3:PutObject","resource":"arn:aws:s3:::app-uploads/in/hello.txt","decision":"implicitDeny","policyType":"identity-based policy","message":"User: arn:aws:iam::000000000000:user/app is not authorized to perform: s3:PutObject because no identity-based policy allows the s3:PutObject action"}]}
```

`"identityPolicies": [...]` in the request replaces the principal's identity policies for that one evaluation (`[]` is none), so you can try a policy before attaching it. `decision` uses the IAM policy simulator's spellings: `allowed`, `implicitDeny` or `explicitDeny`. `unsuppliedKeys` lists the condition keys understudy could not supply ([Advisories](#advisories)).

### Advisories

Each decision that is not a plain allow is journaled as an `advisory` event (`understudy journal --kind advisory`):

| Advisory | Mode | Means |
|---|---|---|
| `access-would-deny` | `advise` | AWS would deny the call; understudy served it |
| `access-denied` | `enforce` | understudy denied the call with the service's denial error |
| `access-unevaluated` | `advise` | understudy could not evaluate the call, for example because a stored policy does not parse; the call was served. Under `enforce` such a call fails with an internal error naming the policy |
| `condition-key-unsupplied` | `advise`, `enforce` | a condition tested a key AWS would send that understudy cannot compute |
| `policy-would-deny` | `off` | a delivery the target's stored policy would refuse; it is attempted anyway |
| `check-not-run` | all | a call AWS refuses when a check fails that understudy does not run ([What is not enforced](#what-is-not-enforced)); the call was served |

understudy never assumes that a condition holds:

- Some keys AWS would send are ones understudy cannot compute: `aws:SourceIp`, the resource's tags (`aws:ResourceTag/...`), and the called service's own keys that it does not supply, such as `s3:prefix`. A condition on such a key is evaluated as if the key were absent, which is how AWS evaluates a key a request does not carry. Each evaluation that tests one journals `condition-key-unsupplied` naming the key.
- The S3 keys understudy supplies are `s3:x-amz-server-side-encryption`, `s3:x-amz-server-side-encryption-aws-kms-key-id` and `s3:x-amz-server-side-encryption-customer-algorithm`, on `PutObject`, `CopyObject` and `CreateMultipartUpload`, each from its request header and absent without it. A request with no encryption header into a bucket whose default encryption is SSE-KMS takes `s3:x-amz-server-side-encryption-aws-kms-key-id` from the default's key, but not `s3:x-amz-server-side-encryption`, as on AWS (the `s3.condition.sse-from-default` switch). So AWS's "require SSE-KMS" bucket policy, a deny of `s3:PutObject` when the key-id key is `Null`, lets a header-less upload into such a bucket through, and a deny on the algorithm key refuses it. On AWS only `PutObject` was recorded; the other two operations are inferred.
- The SES keys understudy supplies are `ses:ApiVersion` on every SES operation, and `ses:FromAddress` and `ses:Recipients` on v1 `SendEmail` and `SendTemplatedEmail` and v2 `SendEmail` with Simple or Template content. A condition on `ses:FromDisplayName` or `ses:FeedbackAddress`, or on `ses:FromAddress` or `ses:Recipients` for a raw send or an operation that does not send, answers 501 under `enforce` rather than being evaluated as absent, since that could allow a send AWS denies. [SES](ses-email.md#policies-on-sends) lists the values.
- understudy never sends the VPC, organization and MFA keys (`aws:MultiFactorAuthPresent` is `false` in a session), so a condition on one gets AWS's own answer for a request made over the public endpoint, from outside an organization, without MFA.

Where AWS's denial text, status or behavior is not recorded, understudy follows a fidelity switch. `understudy coverage --switches` lists them, and the `iam.*` and `*.denial.*` switches cover enforcement.

### What is not enforced

These differences hold under every mode, and each is stated in the operation's coverage note (`understudy coverage --service <name>`):

- KMS operations that take only a key id, such as `GetKeyPolicy` and `EnableKey`, deny a missing key first. AWS answered `NotFoundException` first to `DescribeKey` on a missing key id, as understudy does for the operations that also take an alias (`kms.enforce.missing-resource.by-name`).

SQS queues, S3 buckets and S3 keys answer not-found first, as AWS does: a missing queue is `QueueDoesNotExist` and a missing bucket `NoSuchBucket`, whatever the caller may do. A read of a missing key asks `s3:ListBucket` on the bucket before `s3:GetObject`, so a caller without it gets the `s3:ListBucket` denial and one with it `NoSuchKey` (the `sqs.enforce.missing-resource` and `s3.enforce.missing-resource` switches name the operations AWS recorded and those that follow by inference). S3 Control's tag operations still deny a missing bucket first.

IAM and Secrets Manager deny first, as AWS does: a caller not allowed `iam:GetRole` gets the denial whether or not the role exists, naming it `role <name>`, and Secrets Manager names the secret as the `SecretId` gave it, existing or not. A caller allowed the action gets `NoSuchEntity` or `ResourceNotFoundException`. Which ARN AWS evaluates for a missing resource is unrecorded: understudy asks about a missing user, role or instance profile on its ARN at the path `/`, and a missing secret on the ARN its `SecretId` implies, without a suffix, so a grant on `*`, `role/*` or `secret:*` covers it (the `iam.enforce.missing-resource.by-name` and `secretsmanager.enforce.missing-resource.by-name` switches).
- S3 Block Public Access and Secrets Manager's `BlockPublicPolicy` are stored and not applied, and KMS's key-policy lockout check is not run. Under `enforce` a public policy or ACL, or a key policy that locks out its caller, is stored where AWS refuses it. Each call AWS would check journals `check-not-run`: `PutBucketPolicy` while `BlockPublicPolicy` is true, `PutBucketAcl` granting `AllUsers` or `AuthenticatedUsers` while `BlockPublicAcls` is true, `PutResourcePolicy` with `BlockPublicPolicy` true, and a key policy sent to `CreateKey` or `PutKeyPolicy` without `BypassPolicyLockoutSafetyCheck`.
- A principal a policy names is not checked for existence. AWS refuses a trust policy or a queue policy that names a role or user that does not exist; understudy stores it, so policies naming roles never created in understudy keep working (the `iam.policy.principal-existence` switch).
- A function URL with `AuthType` `AWS_IAM` checks only the signature, never a policy. An `AuthType` `NONE` URL checks its resource policy under every mode.
- Groups, KMS grants and the IAM policy simulator are not implemented, and answer 501.

### Configuration checks

Some calls that configure a Lambda function check its execution role's trust policy or permissions, as AWS does, whoever makes the call:

- `CreateFunction`, and `UpdateFunctionConfiguration` when it passes a `Role`, ask whether `lambda.amazonaws.com` may assume the role: its trust policy must allow `sts:AssumeRole` for that service principal. A role that does not exist fails the same way. The caller's own `iam:PassRole` on the role is asked first, and its denial is an `AccessDeniedException`. That `UpdateFunctionConfiguration` checks the trust is inferred; only `CreateFunction` was recorded.
- `CreateEventSourceMapping` and `UpdateEventSourceMapping` ask whether the role may read the mapping's queue or stream.
- `PutFunctionEventInvokeConfig` and `UpdateFunctionEventInvokeConfig` ask whether the role may call `sns:Publish` on an SNS destination or `sqs:SendMessage` on an SQS one. Function, event bus and S3 bucket destinations are not checked.

Under `enforce` a refusal fails the call with `InvalidParameterValueException`; under `advise` the call is served and journals `access-would-deny`; under `off` nothing is asked. With the function `worker` and the topic `alerts`, and a role with no policy:

```sh
aws lambda put-function-event-invoke-config --function-name worker --destination-config '{"OnSuccess":{"Destination":"arn:aws:sns:us-east-1:000000000000:alerts"}}'
```

```text
aws: [ERROR]: An error occurred (InvalidParameterValueException) when calling the PutFunctionEventInvokeConfig operation: The function execution role does not have permissions to call Publish on arn:aws:sns:us-east-1:000000000000:alerts
```

Once `aws iam put-role-policy` gives the role `sns:Publish` on the topic, the same call stores the destination.

With the role `worker-role` trusting only the account:

```sh
aws lambda create-function --function-name worker --runtime python3.13 --handler index.handler \
  --role arn:aws:iam::000000000000:role/worker-role --zip-file fileb://fn.zip
```

```text
aws: [ERROR]: An error occurred (InvalidParameterValueException) when calling the CreateFunction operation: The role defined for the function cannot be assumed by Lambda.

Additional error details:
Type: User
```

Once `aws iam update-assume-role-policy` lets `lambda.amazonaws.com` assume the role, the same call creates the function.

`PutBucketNotificationConfiguration` checks each destination's resource policy, as AWS does: it asks whether the queue, topic or function lets `s3.amazonaws.com` call `sqs:SendMessage`, `sns:Publish` or `lambda:InvokeFunction`, with the keys a delivery sends ([Deliveries](#deliveries)). `x-amz-skip-destination-validation: true` skips the check, as it skips the existence check. Under `enforce` a refusal fails the call with `InvalidArgument`, naming each refused destination in `ArgumentName1`, `ArgumentValue1` and so on; under `advise` the call is served and journals `access-would-deny`; under `off` nothing is asked. With the bucket `uploads` and the queue `uploads`, which has no policy:

```sh
aws s3api put-bucket-notification-configuration --bucket uploads --notification-configuration '{"QueueConfigurations":[{"QueueArn":"arn:aws:sqs:us-east-1:000000000000:uploads","Events":["s3:ObjectCreated:*"]}]}'
```

```text
aws: [ERROR]: An error occurred (InvalidArgument) when calling the PutBucketNotificationConfiguration operation: Unable to validate the following destination configurations
```

The body names the queue in `ArgumentName1`, with `ArgumentValue1` `Permissions on the destination queue do not allow S3 to publish notifications from this bucket`. Once the queue's policy allows `s3.amazonaws.com` to call `sqs:SendMessage` from `arn:aws:s3:::uploads`, the same call stores the configuration.

### Deliveries

Under `advise` and `enforce`, deliveries between services are decided the same way as calls. SNS, S3 and EventBridge call the target as their service principal, with `aws:SourceArn` the topic, bucket or rule, and `aws:SourceAccount` the topic's account, the bucket owner's or the rule's. A rule that forwards to an event bus sends no `aws:SourceAccount`.

Under `enforce`, a delivery the target's policy refuses is not retried. It goes to the subscription's or target's dead-letter queue when one is set; otherwise it is dropped. An EventBridge dead letter of a refusal carries four String attributes and no `RETRY_ATTEMPTS`: `ERROR_CODE` `NO_PERMISSIONS`, `RULE_ARN`, `TARGET_ARN` and `ERROR_MESSAGE`, the target's denial followed by the suffix of the SDK client EventBridge called it through, such as `User: events.amazonaws.com is not authorized to perform: lambda:InvokeFunction on resource: arn:aws:lambda:us-east-1:000000000000:function:fn because no resource-based policy allows the lambda:InvokeFunction action (Service: AWSLambdaInternal; Status Code: 403; Error Code: AccessDeniedException; Request ID: <id>; Proxy: null)`. A queue's denial names `sqs:sendmessage` and ends `(Service: AmazonSQS; Status Code: 403; Error Code: AccessDenied; ...)`. A function deleted after `PutTargets` dead-letters the same way under every mode, `off` included, since no policy can admit EventBridge to it. Of the calls that configure a delivery, only S3's `PutBucketNotificationConfiguration` checks the target's policy ([Configuration checks](#configuration-checks)); `Subscribe` and `PutTargets` neither check nor change it. With a queue that has no policy subscribed to a topic, a published message is dropped:

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

An event bus's policy, set with `PutPermission`, is evaluated like the other resource policies, for `PutEvents` to the bus and for the events rules forward to it. A user whose identity policy allows `events:PutEvents` on `*` is still refused once the root puts a bus policy with an explicit `Deny` for it (the first command as the root, the second as the user):

```sh
aws events put-permission --policy '{"Version":"2012-10-17","Statement":[{"Sid":"no-app","Effect":"Deny","Principal":{"AWS":"arn:aws:iam::000000000000:user/app"},"Action":"events:PutEvents","Resource":"arn:aws:events:us-east-1:000000000000:event-bus/default"}]}'
aws events put-events --entries '[{"Source":"app","DetailType":"order","Detail":"{}"}]'
```

```text
aws: [ERROR]: An error occurred (AccessDeniedException) when calling the PutEvents operation: User: arn:aws:iam::000000000000:user/app is not authorized to perform: events:PutEvents on resource: arn:aws:events:us-east-1:000000000000:event-bus/default with an explicit deny in a resource-based policy
```

The statements such deliveries need are the second part of [`understudy iam policy`](#print-the-policy). Which delivery choices are inferred is listed by `understudy coverage --switches` (the `*.delivery.*`, `sns.subscribe.*`, `events.put-targets.*` and `s3.notification.*` switches).

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

`MODE` says who made the call:

- `direct`: the principal itself.
- `caller`: a service, with the principal's identity, such as S3 calling KMS for an SSE-KMS object.
- `execution-role`: understudy, as a function's role, such as creating its log group, writing its logs or polling its SQS event source, or a permission of the role a configuration call checks, such as its write to a Lambda destination.

`--service sqs` narrows to one IAM prefix. The usage lists exact ARNs, except that S3 object calls are listed per bucket as `arn:aws:s3:::<bucket>/*`, so the record grows with the buckets a run used, not its keys; `understudy journal` shows each call's exact object.

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

The policy has one statement per resource, with exact ARNs, with two exceptions. S3 objects are granted per bucket as `arn:aws:s3:::<bucket>/*`. A resource whose id understudy mints is granted as the narrowest wildcard matching every id AWS could mint for it:

| Resource | Granted as |
|---|---|
| KMS keys | `key/*` |
| Secrets Manager secrets | `secret:<name>-??????` |
| SNS subscriptions | `<topic arn>:*` |
| Lambda event source mappings | `event-source-mapping:*` |
| Log streams Lambda creates | `log-group:<group>:log-stream:*` |
| DynamoDB streams | `table/<name>/stream/*` |
| API Gateway APIs | `/apis/??????????` (HTTP) or `/restapis/??????????` (REST) |
| Their routes, integrations, resources and deployments | `/apis/??????????/routes/*`, `/restapis/??????????/resources/*` and so on, also inside a `/tags/` resource |

The ids differ in every run, on AWS too, so the policy carries over to the next run; `iam usage` still lists the exact ARNs. `Resource: "*"` appears only for an action of the called service whose call named no resource of that service, such as `sqs:ListQueues`. Two keys and an encryption with each:

```sh
aws kms create-key
aws kms create-key
aws kms encrypt --key-id <first key id> --plaintext aGk=
aws kms encrypt --key-id <second key id> --plaintext aGk=
understudy iam policy --principal arn:aws:iam::000000000000:root
```

```text
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:CreateKey","Resource":"*"},{"Effect":"Allow","Action":"kms:Encrypt","Resource":"arn:aws:kms:us-east-1:000000000000:key/*"}]}
```

`iam usage` lists `kms:CreateKey` on `*`, where AWS authorizes it before the key exists, and `kms:Encrypt` on each key.

An action the policy grants on `*` is left out of every other statement, and a statement left with no action is dropped; the usage is unchanged. On a fresh server, a key, an encryption with it, and one through an alias that names no key, which fails before any key is known:

```sh
aws kms create-key
aws kms encrypt --key-id <key id> --plaintext aGk=
aws kms encrypt --key-id alias/missing --plaintext aGk=
understudy iam usage --principal arn:aws:iam::000000000000:root
understudy iam policy --principal arn:aws:iam::000000000000:root
```

```text
PRINCIPAL                       MODE    ACTION         RESOURCE                                         CALLS  FAILURES  LAST
arn:aws:iam::000000000000:root  direct  kms:CreateKey  *                                                1      0         ...
arn:aws:iam::000000000000:root  direct  kms:Encrypt    *                                                1      1         ...
arn:aws:iam::000000000000:root  direct  kms:Encrypt    arn:aws:kms:us-east-1:000000000000:key/<key id>  1      0         ...
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["kms:CreateKey","kms:Encrypt"],"Resource":"*"}]}
```

The failed call put `kms:Encrypt` on `*`, so the key's statement lost its only action and was dropped.

What else the policy includes and leaves out:

- `sts:GetCallerIdentity` needs no permission, so the policy leaves it out; `iam usage` still lists its calls.
- Calls that failed are included, because handled failures need permissions too.
- Injected faults are left out, and so are calls `--sigv4 verify` refused at authentication (a bad signature, an unknown key, a missing or expired session token), which on AWS never reach IAM.
- Writing and reading an SSE-KMS object records `kms:GenerateDataKey` and `kms:Decrypt` on the key for the caller, as AWS requires.

Likewise, an action the policy grants on a minted id's wildcard is left out of a statement on an exact ARN the wildcard matches. A REST API, a read of it, and a read and a delete of an id that names no API:

```sh
aws apigateway create-rest-api --name demo
aws apigateway get-rest-api --rest-api-id <api id>
aws apigateway get-rest-api --rest-api-id MISSING123
aws apigateway delete-rest-api --rest-api-id MISSING123
understudy iam usage --principal arn:aws:iam::000000000000:root
understudy iam policy --principal arn:aws:iam::000000000000:root
```

```text
PRINCIPAL                       MODE    ACTION                          RESOURCE                                            CALLS  FAILURES  LAST
arn:aws:iam::000000000000:root  direct  apigateway:DELETE               arn:aws:apigateway:us-east-1::/restapis/MISSING123  1      1         ...
arn:aws:iam::000000000000:root  direct  apigateway:GET                  arn:aws:apigateway:us-east-1::/restapis/MISSING123  1      1         ...
arn:aws:iam::000000000000:root  direct  apigateway:GET                  arn:aws:apigateway:us-east-1::/restapis/<api id>    1      0         ...
...
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"apigateway:DELETE","Resource":"arn:aws:apigateway:us-east-1::/restapis/MISSING123"},{"Effect":"Allow","Action":"apigateway:GET","Resource":"arn:aws:apigateway:us-east-1::/restapis/??????????"},...]}
```

`MISSING123` is not an id understudy mints, so its ARN stays exact, but `??????????` matches it, so its statement keeps only `apigateway:DELETE`.

The second part lists the resource-policy statements that SNS, S3, EventBridge and Lambda deliveries would need on AWS: a queue policy allowing the topic, a function permission allowing the rule. Each names the `aws:SourceAccount` the delivery sends. Under `off` understudy attempts those deliveries anyway, and journals a `policy-would-deny` advisory for each one the target's stored policy would refuse; under `enforce` the target refuses them ([Deliveries](#deliveries)).

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
- **Other actions of the same service only when the request needs them.** Every operation for which AWS's Service Reference lists more than one action of its service records the others only when the request's members need them. A `PutObject` with `x-amz-tagging` adds `s3:PutObjectTagging`, one with an ACL or grant header `s3:PutObjectAcl`; a `GetObject` or `HeadObject` of a version adds `s3:GetObjectVersion`, a `GetObjectAttributes` of one records `s3:GetObjectVersion` and `s3:GetObjectVersionAttributes` where it would record `s3:GetObject`; an `AssumeRole` with session tags adds `sts:TagSession`, one with a source identity `sts:SetSourceIdentity`; a `CreateQueue`, `CreateRole`, `CreateKey` or `CreateFunction` with tags adds the service's tag action; a `GetLogEvents` with `unmask` adds `logs:Unmask`; a `ReEncrypt` adds `kms:ReEncryptTo`; a PartiQL `INSERT` adds `dynamodb:PartiQLInsert`. A plain `PutObject` records `s3:PutObject` alone. DynamoDB's replication actions, such as `dynamodb:ReadDataForReplication` on `GetItem`, are never recorded. Two operations record a primary AWS does not always need: `TransactWriteItems` records `dynamodb:PutItem` on every table it names, and the PartiQL operations record `dynamodb:PartiQLSelect`, each with every PartiQL action on `"*"` (the coverage notes list both).
- **Actions of another service only on a resource the call touched.** `CreateFunction` and `UpdateFunctionConfiguration` record `iam:PassRole` on the role they pass, as do EventBridge `PutRule` and `PutTargets` with a `RoleArn`, SNS `CreateTopic` and `SetTopicAttributes` with a delivery status role and `Subscribe` and `SetSubscriptionAttributes` with a `SubscriptionRoleArn`, S3 `PutBucketReplication` and Step Functions `CreateStateMachine` and `UpdateStateMachine`; a call that passes no role records none. Where understudy sends the `iam:PassedToService` the pass carries, as Lambda (`lambda.amazonaws.com`), Step Functions (`states.amazonaws.com`) and IAM `AddRoleToInstanceProfile` (`ec2.amazonaws.com`) do, the `iam:PassRole` statement is granted on that condition: `"Condition":{"StringEquals":{"iam:PassedToService":"states.amazonaws.com"}}`. Actions with no such resource, such as `s3-object-lambda:PutObject` on a `PutObject` that touched no Object Lambda access point, are never recorded.
- **Each action on the resources it applies to, where understudy knows them.** A `CopyObject` records `s3:GetObject` on the source bucket's objects and `s3:PutObject` on the destination's; a `ReEncrypt` `kms:ReEncryptFrom` on the key that sealed the ciphertext and `kms:ReEncryptTo` on the destination key; a `StartMessageMoveTask` its receive, delete and attribute actions on the dead-letter queue and `sqs:SendMessage` on where the messages go; an `AddRoleToInstanceProfile` `iam:PassRole` on the role and its own action on the profile. Other calls that touch several resources of one service are granted each of their actions on each of them: a `DeleteObjects` with one versioned key grants `s3:DeleteObjectVersion` on every key it named; a `CreateFunction` with a capacity provider grants `lambda:CreateFunction` on the provider and `lambda:PassCapacityProvider` on the function. Trim those statements to the pairs your code needs.
- **`s3:ListBucket` for a missing key.** A `GetObject`, `HeadObject` or `GetObjectAttributes` of a key that does not exist records `s3:ListBucket` on the bucket: AWS answers `404 NoSuchKey` to a caller with it and `403 AccessDenied` to one without, so a test that reads a missing key needs it.
- **S3 objects per bucket.** Object operations touch the object's ARN (`journal` shows it), and are recorded, listed by `iam usage` and granted on `arn:aws:s3:::<bucket>/*`. Narrow that to a key prefix if your code only uses one.
- **Lambda log delivery** records `logs:CreateLogGroup`, `logs:CreateLogStream` and `logs:PutLogEvents` for the function's execution role.
- **A role's permission a configuration call checks** is recorded for that role, in mode `execution-role`, whether or not anything is ever sent: `sns:Publish` or `sqs:SendMessage` on each SNS or SQS destination and dead-letter target a Lambda call checks ([Configuration checks](#configuration-checks)), and the log-delivery actions a Step Functions `CreateStateMachine` or `UpdateStateMachine` with logging checks for the machine's role. The role's policy then lets the same calls through under `enforce`. After `put-function-event-invoke-config` sends `worker`'s successes to the queue `worker-results`, under `advise`:

  ```sh
  understudy iam usage --principal arn:aws:iam::000000000000:role/worker-role
  ```

  ```text
  PRINCIPAL                                   MODE            ACTION           RESOURCE                                           CALLS  FAILURES  LAST
  arn:aws:iam::000000000000:role/worker-role  execution-role  sqs:SendMessage  arn:aws:sqs:us-east-1:000000000000:worker-results  1      0         2026-11-13T20:22:38Z
  ```
- **`*` for the first use of an AWS managed key.** The first call that uses an `alias/aws/<service>` alias creates that key, such as an SSE-KMS `PutObject` without a key id (`alias/aws/s3`) or a `CreateSecret` without `KmsKeyId` (`alias/aws/secretsmanager`). understudy authorizes the call before the key exists, so `kms:GenerateDataKey`, `kms:Encrypt`, `kms:GenerateDataKeyWithoutPlaintext`, `kms:DescribeKey` or `kms:ReEncryptTo` is recorded on `*`, and the policy grants it on `*`. Later uses are recorded on the key. Replace `*` with the key's ARN, which `aws kms describe-key --key-id alias/aws/<service>` prints.

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

`aws iam list-policies --scope AWS` lists the catalogue, `--scope All` (the default) lists it with your own policies, and `--only-attached` keeps the ones attached in the account. An attached AWS managed policy is evaluated like any other under `advise` and `enforce`. In Terraform, the `aws_iam_policy` data source finds one by name:

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
