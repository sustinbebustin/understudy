# IAM-EVAL-11

Date: 2026-11-03 (03:11-03:15Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root), so the 13.1 IAM-user switch was not run.
Label: aws-recorded, simulator (simulate-custom-policy). Recipe fixes: none. The doc states no Expected besides the 03 rerun rule; recorded:

- 01-arnlike-incomplete: InvalidInput "The simulation request contains invalid input." (exit 254).
- 02-arnlike-nonarn: implicitDeny (ArnLike "*" does not match a non-ARN value).
- 03-resource-policy-incomplete: InvalidInput "ResourceNames are not in a valid ARN format: arn:aws:s3." (exit 254) -- names the resource policy's incomplete Resource, not the role caller.
- 03 rerun with an IAM user: n/a (no $P-eval-admin: caller was not root, and the error is about ARN format, not caller type).
