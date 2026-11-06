# IAM-EVAL-14

Date: 2026-11-03 (03:11-03:15Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root), so the 13.1 IAM-user switch was not run.
Label: aws-recorded, simulator (simulate-custom-policy). Recipe fixes: none. The doc states no Expected; recorded:

- 01-multivalued (${aws:TagKeys} in Resource): implicitDeny.
- 02-unknown-literal: implicitDeny.
- 03-unknown-value (aws:nosuchkey supplied as context): allowed.
- 04-in-action (s3:${aws:PrincipalTag/act}): implicitDeny (no substitution in Action).
