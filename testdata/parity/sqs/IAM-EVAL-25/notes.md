# IAM-EVAL-25

Date: 2026-11-03 (first run 03:16-03:25Z; reruns 16:40-16:47Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root); the 13.1 IAM-user switch was not run.
Recipe fixes: none. The doc states no Expected; recorded:

- SQS, SNS and S3 each return Principal {"AWS":"arn:aws:iam::XXXXXXXXXXXX:root"} for the bare account id (normalised on save). KMS (IAM-EVAL-27/06) does the same.
