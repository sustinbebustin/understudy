# SSM-33 notes

- Date: 2026-11-04 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu)
- Recipe: docs/research/ssm-parameters.md 13.34; run from `late-ssm.sh`, session changes marked `# session:` (the fill creates only the difference to 10,000 over the account's existing parameters, three PutParameter calls at a time with adaptive retries; the teardown deletes ten names per call by prefix `/us1103/lim/`, then counts what is left)

| Field | Expected | Result |
|---|---|---|
| parameters before the fill | none | 0 other parameters in the account and Region |
| fill | 10,000 Standard parameters | 10,000 created under `/us1103/lim/` (`p00001` to `p10000`, Type String, default tier Standard); the fill's error file holds only AWS CLI telemetry tracebacks, no API error |
| 01 the 10,001st PutParameter (Tier Standard) | `ParameterLimitExceeded` with its text | 400 `{"__type":"ParameterLimitExceeded","message":"You have reached the maximum number of standard parameters for this AWS account and Region (10000). To add more parameters, either delete existing parameters or upgrade to the advanced-parameter tier (charges apply)."}`: member `message` in lower case; the text names neither the parameter nor the tier argument, and differs from the model's documentation ("You have exceeded the number of parameters for this Amazon Web Services account.") |
| teardown | none left | pass 1 left 0 under `/us1103/lim/` (2026-11-04T18:05:45Z) |

Session change: the first run (2026-11-04T16:31Z) completed the fill but its count summed one number per describe page instead of counting names, so it reported the fill incomplete and did not send 01. The rerun with the corrected count found the 10,000 parameters, skipped the fill and sent 01 at 2026-11-04T17:54:27Z.
