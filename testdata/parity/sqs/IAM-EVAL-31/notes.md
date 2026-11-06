# IAM-EVAL-31

Date: 2026-11-03 (first run 03:16-03:25Z; reruns 16:40-16:47Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root); the 13.1 IAM-user switch was not run.
Recipe fix: role session names of one character fail the 2-character STS minimum (ParamValidation: Invalid length for parameter RoleSessionName, value: 1, valid min length: 2); renamed to two characters and the steps rerun. (session t -> tt; rerun at 16:44Z).

- create-queue accepts env=a and Env=b (exit 0); list-queue-tags {"Env":"b","env":"a"}.
- value-a allowed; value-b AccessDenied "... because no identity-based policy allows ...": aws:ResourceTag/env matched the lower-case key only (case-sensitive lookup).
