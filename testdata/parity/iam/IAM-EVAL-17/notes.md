# IAM-EVAL-17

Date: 2026-11-03 (03:11-03:15Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root), so the 13.1 IAM-user switch was not run.
API: get-context-keys-for-custom-policy (not the simulator). Recipe fixes: none.

- Records whether aws:username and aws:PrincipalTag/team are listed: both listed. ContextKeyNames = [aws:SourceIp, aws:username, aws:PrincipalTag/team, s3:ExistingObjectTag/team], in the case written in the policy.
