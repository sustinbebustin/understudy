# IAM-EVAL-21

Date: 2026-11-03 (first run 03:16-03:25Z; reruns 16:40-16:47Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root); the 13.1 IAM-user switch was not run.
Recipe fix: role session names of one character fail the 2-character STS minimum (ParamValidation: Invalid length for parameter RoleSessionName, value: 1, valid min length: 2); renamed to two characters and the steps rerun. (trust-implicit and session-policy-incomplete-arn used session t; rerun with tt at 16:40Z by the parallel duplicate agent's script with the same fix).

- sts get-caller-identity succeeds (control): matches (200).
- implicit codes/statuses (record): sqs AccessDenied 403; sns AuthorizationError 403 (action spelled SNS:ListTopics); s3 AccessDenied 403 (s3:ListAllMyBuckets, no resource); lambda AccessDeniedException 403; dynamodb, kms, secretsmanager, logs, events AccessDeniedException 400; cloudwatch AccessDenied 403 (no resource); iam AccessDenied 403 with the GetRequestAuthorizationDetails URL and authorization id appended. All say "because no identity-based policy allows the <action> action".
- explicit-identity: AccessDenied 403 "... with an explicit deny in an identity-based policy".
- explicit-resource (caller): AccessDenied 403 "... with an explicit deny in a resource-based policy".
- explicit-both: names the resource-based policy ("with an explicit deny in a resource-based policy").
- trust-implicit: AccessDenied "User: <caller> is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::XXXXXXXXXXXX:role/us1103-eval-closed".
- session-policy-incomplete-arn: MalformedPolicyDocument "The following resources are invalid : arn:aws:s3".
