# IAM-EVAL-34

Date: 2026-11-03 (first run 03:16-03:25Z; reruns 16:40-16:47Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root); the 13.1 IAM-user switch was not run.
Recipe fix: role session names of one character fail the 2-character STS minimum (ParamValidation: Invalid length for parameter RoleSessionName, value: 1, valid min length: 2); renamed to two characters and the steps rerun. (session v -> vv). Recipe fix: no wait between creating $P-eval-via and the key policy naming it; the first run failed with MalformedPolicyDocumentException "Policy contains a statement with one or more invalid principals.", so a 15 s sleep was added. Recipe fix: --body /dev/null replaced by an empty regular file (CLI: "Blob values must be a path to a file", per the parallel agent). Rerun at 16:45Z on a new key.

- via-true: put-object denied (AccessDenied "... kms:GenerateDataKey ... with an explicit deny in a resource-based policy") => S3's KMS call carried aws:ViaAWSService true.
- via-false: succeeded => it did not carry false.
- calledvia-s3: succeeded => aws:CalledVia did not hold s3.amazonaws.com.
- calledvia-absent: succeeded => aws:CalledVia was not absent. Together with calledvia-s3 this suggests a value other than s3.amazonaws.com, or a key-policy propagation lag; not settled by these four steps.
