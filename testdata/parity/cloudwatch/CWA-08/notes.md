# CWA-08 (13.9)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

- nd-b2 plural text (control): matches ("no datapoints were received for 2 periods and 2 missing datapoints were treated as [Breaching].").
- nd-b1 singular: matches ("Threshold Crossed: no datapoints were received for 1 period and 1 missing datapoint was treated as [Breaching].").
- nd-n2 label: matches, label is [NonBreaching] ("... 2 missing datapoints were treated as [NonBreaching].").
- nd-p3 mixed form: matches ("Threshold Crossed: 1 datapoint [5.0 (03/11/26 03:13:00)] was greater than the threshold (3.0) and 2 missing datapoints were treated as [Breaching].").
- singular Insufficient Data text: n/a (from CWA-01; CWA-02 and CWA-11 show "Insufficient Data: 1 datapoint was unknown.").
