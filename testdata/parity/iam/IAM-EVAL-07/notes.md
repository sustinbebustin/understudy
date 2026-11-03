# IAM-EVAL-07

Date: 2026-11-03 (03:11-03:15Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root), so the 13.1 IAM-user switch was not run.
Label: aws-recorded, simulator (simulate-custom-policy). Recipe fixes: none.

- v6-near denied => /128: matches (implicitDeny).
- v4-near denied confirms /32: matches (implicitDeny).
- v6-same allowed; v6-case (2001:DB8::1 vs 2001:db8::1) allowed.
