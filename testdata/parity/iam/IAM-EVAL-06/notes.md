# IAM-EVAL-06

Date: 2026-11-03 (03:11-03:15Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root), so the 13.1 IAM-user switch was not run.
Label: aws-recorded, simulator (simulate-custom-policy). Recipe fixes: none.

- 2020-01-01: allowed (parsed as an instant before 00:00:01Z).
- 2020-01-01T00:00:00Z: allowed.
- 2020-01-01T00:00:00.000Z: allowed.
- 2020-01-01T01:00:00+01:00: allowed (offset honoured).
- 1577836800: allowed (epoch seconds parsed).
- 1577836800000: implicitDeny (later or not parsed; not distinguishable here). No InvalidInput/PolicyEvaluation for any value.
- All within the doc's outcome set: matches.
