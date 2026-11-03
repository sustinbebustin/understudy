# IAM-EVAL-36

Date: 2026-11-03 (03:11-03:15Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root), so the 13.1 IAM-user switch was not run.
Recipe fixes: none.

- 01-ag-instance .. 10-mrap-request-elsewhere created: matches (all ten created; policies deleted).
- 11-object-region refused: matches -- MalformedPolicyDocument: Resource arn:aws:s3:us-east-2:XXXXXXXXXXXX:us1103-eval/k can not contain region information.
- 12-bucket-named-job refused: differs: created (exit 0); per the doc this means the exemption goes by prefix alone, without the account element.
- 13-bucket-named-accesspoint refused: differs: created (exit 0); same implication (moto also accepts 13).
- 10 created while 09 created: async-request/mrap/ is not limited to us-west-2.
