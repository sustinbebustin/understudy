# IAM-EVAL-24

Date: 2026-11-03 (first run 03:16-03:25Z; reruns 16:40-16:47Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root); the 13.1 IAM-user switch was not run.
Recipe fixes: none.

- 01 MalformedPolicyDocument with Invalid principal in policy: matches -- 400 "Invalid principal in policy: \"AWS\":\"arn:aws:iam::XXXXXXXXXXXX:role/us1103-nope\"".
- 03 and 04 show an AROA... id in place of the ARN: matches (both show the same AROA id; the re-created role does not restore the ARN).
- 05 sqs missing user (record): InvalidAttributeValue 400 "Invalid value for the parameter Policy."
