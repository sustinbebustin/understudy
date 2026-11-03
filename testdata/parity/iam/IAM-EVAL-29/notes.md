# IAM-EVAL-29

Date: 2026-11-03 (first run 03:16-03:25Z; reruns 16:40-16:47Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root); the 13.1 IAM-user switch was not run.
Recipe fixes: the recovery line is guarded so it runs only if 01 succeeds (it did not).

- Both calls fail: matches.
- 01-lockout: MalformedPolicyDocumentException 400 "The new key policy will not allow you to update the key policy in the future."
- 02-missing-principal: MalformedPolicyDocumentException 400 "Policy contains a statement with one or more invalid principals."
