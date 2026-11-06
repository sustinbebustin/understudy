# IAM-EVAL-28

Date: 2026-11-03 (first run 03:16-03:25Z; reruns 16:40-16:47Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root); the 13.1 IAM-user switch was not run.
Recipe fixes: none. The doc states no Expected; recorded:

- 01-describe: AccessDeniedException 400 "User: ... is not authorized to perform: kms:DescribeKey on resource: <key arn> because no identity-based policy allows the kms:DescribeKey action".
- 02-encrypt: AccessDeniedException 400, same form for kms:Encrypt.
