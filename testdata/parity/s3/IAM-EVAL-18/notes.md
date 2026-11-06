# IAM-EVAL-18

Date: 2026-11-03 (first run 03:16-03:25Z; reruns 16:40-16:47Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root); the 13.1 IAM-user switch was not run.
Recipe fixes: none.

- Only c-session-role-account succeeds (200); a, b, d, e, f AccessDenied 403 "... with an explicit deny in a resource-based policy": matches (branch: every-level).
