# IAM-EVAL-19

Date: 2026-11-03 (first run 03:16-03:25Z; reruns 16:40-16:47Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root); the 13.1 IAM-user switch was not run.
Recipe fixes: none.

- b-role denied (documented): matches -- AccessDenied "... because no permissions boundary allows the sqs:getqueueattributes action".
- c-session allowed (documented): matches.
- a-star: allowed (200) => direct.
