# CWA-16 (13.17)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

- ConfigurationUpdate items for both creates and the update: matches (summaries Alarm "us1103-hist-miss" created, Alarm "us1103-hist-ok" created, Alarm "us1103-hist-ok" updated; HistoryData type Create/Update with createdAlarm/updatedAlarm; standard statistic shows as "extendedStatistic":"MAXIMUM").
- Action item for the failed publish: matches ("Failed to execute action arn:aws:sns:us-east-2:<acct>:us1103-missing"; HistoryData actionState "Failed", error "Topic does not exist", publishedMessage null, muteWindowEndTimestamp null).
- Action item for the delivered publish: matches ("Successfully executed action arn:aws:sns:us-east-2:<acct>:us1103-cwa"; actionState "Succeeded", publishedMessage {"default":"<message JSON>"}).
- hist-miss history listed after the delete: matches, plus a ConfigurationUpdate item Alarm "us1103-hist-miss" deleted.
