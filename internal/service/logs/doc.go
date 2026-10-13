// Package logs implements CloudWatch Logs: log groups and streams,
// PutLogEvents, GetLogEvents, FilterLogEvents with the filter pattern
// grammar of internal/logsfilter, tags and retention on the emulator clock.
// The overlay operation PutLogEventsInternal is how Lambda function logs
// arrive (ARCHITECTURE.md 14.6).
//
// Decisions the architecture and the research leave open:
//
//   - An event's place in its stream is its position: its timestamp, then a
//     sequence number the scope hands out in ingestion order. The eventId is
//     the position's 56-digit form, and every event paging token is a
//     direction and a position ("f/" or "b/" and the 56 digits, the form
//     AWS's examples show), so a page stays correct when events arrive or
//     expire between calls.
//   - Every paging token, the listing tokens of DescribeLogGroups and
//     DescribeLogStreams included, carries a 20-digit fingerprint of the
//     listing that issued it (the operation and its scope, group or stream)
//     and is refused as invalid by any other, as AWS refuses a token it did
//     not issue for the call. It ends in its issue time on the emulator
//     clock (20 digits of milliseconds) and is refused as invalid 24 hours
//     later, as AWS documents for GetLogEvents and FilterLogEvents tokens. An
//     empty GetLogEvents page hands back the token it was read from, issue
//     time included.
//   - DescribeLogGroups with logGroupNamePattern returns the field set AWS
//     records for it (arn, creationTime, logGroupArn, logGroupClass,
//     logGroupName, metricFilterCount), not the three the documentation
//     promises.
//   - GetLogEvents reads [startTime, endTime) as AWS documents; FilterLogEvents
//     reads both bounds inclusively, by the literal documentation, which no
//     recording settles (logs-filter-patterns.md 6). FilterLogEvents returns
//     full pages and omits nextToken once no matching event remains.
//   - The 2-hour and 14-day windows of PutLogEvents accept an event valid by
//     the wall clock or the emulator clock (lambda-runtime-contract.md 3);
//     retention reads the emulator clock only. PutLogEventsInternal stores the
//     emulator's own lines without either check. When the two clocks diverge
//     by more than 14 days 2 hours, a rejected event can lie between two
//     stored ones, which AWS's rejectedLogEventsInfo index fields cannot
//     express, so the reported indices can cover stored events.
//   - Retention removes events exactly when timestamp plus the retention
//     period passes on the emulator clock, through one timer per group armed
//     at its oldest event's expiry, instead of AWS's lazy deletion within 72
//     hours (the logs.retention.expiry switch).
//   - The 24-hour span of a PutLogEvents batch is measured over every event
//     inside the time windows, including those retention would expire, so
//     such a batch fails whole. This follows the literal documentation; no
//     recording confirms it.
//   - A DELIVERY group's retention is fixed at the days of the
//     logs.delivery.retention-days switch, and PutRetentionPolicy and
//     DeleteRetentionPolicy refuse to change it.
//   - GetLogEvents and FilterLogEvents refuse an INFREQUENT_ACCESS group
//     with InvalidParameterException, the operations' modeled client error,
//     since the log class documentation lists neither as supported there.
//   - A stream's storedBytes is always 0, as AWS documents for streams; a
//     group's is the sum of its events' message bytes.
//   - AssociateKmsKey and DisassociateKmsKey stay unbound and answer 501:
//     CreateLogGroup checks kmsKeyId is the key ARN of an enabled symmetric
//     encryption key through KMS's DescribeKey, then stores and echoes it,
//     which covers creating a group with a key, but Terraform changing
//     kms_key_id on an existing group fails loudly instead of storing a key
//     nothing encrypts with.
//     PutLogGroupDeletionProtection is bound, so a protected group can be
//     unprotected and destroyed. Deletion protection also refuses
//     DeleteLogStream in the group.
//   - Error texts no recording carries are kept in errors.go, each marked
//     inferred, so a recording changes one place.
package logs
