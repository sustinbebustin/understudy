# IAM-EVAL-04

Date: 2026-11-03 (03:11-03:15Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root), so the 13.1 IAM-user switch was not run.
Label: aws-recorded, simulator (simulate-custom-policy). Recipe fixes: none.

- 01-any-ne-absent denied: matches (implicitDeny).
- 02-any-ne-empty allowed or denied: matches (allowed; the empty string is not a). No InvalidInput for the empty value.
- 03-any-ne-a denied: matches (implicitDeny).
- 04-any-ne-b allowed: matches.
- 05-all-eq-empty allowed: differs: implicitDeny (ForAllValues:StringEquals with TagKeys [""] is not satisfied; the empty string is a non-matching member, not an empty set).
- 06-any-eq-empty denied: matches (implicitDeny).
