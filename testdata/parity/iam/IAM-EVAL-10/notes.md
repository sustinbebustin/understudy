# IAM-EVAL-10

Date: 2026-11-03 (03:11-03:15Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root), so the 13.1 IAM-user switch was not run.
Label: aws-recorded, simulator (simulate-custom-policy). Recipe fixes: none.

- 01-exact allowed: matches (action S3:GETOBJECT case-insensitive).
- 02-lower denied: matches (implicitDeny).
- 03-arn-upper either: differs: InvalidInput "ResourceNames are not in a valid ARN format: ARN:AWS:S3:::CaseBucket/Key." (exit 254) -- neither allowed nor denied.
