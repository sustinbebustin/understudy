# R17

- Date: 2026-10-14T07:06Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Compared against understudy built from /home/austin/dev/local-aws-stack on 2026-10-14, replaying a copy of r17.sh through scripts/sealed (account 123456789012). Paths are repo-relative.

NOTE: AWS refused 01-config-full (queue ObjectCreated:* overlaps lambda ObjectCreated:Put), so the unversioned bucket kept config "ps" (prefix a, suffix .txt): no SNS or Lambda events, no lat-* events, and only the 'a b/ü+1.txt' put from that bucket. understudy refused it too, so both runs hold the same configuration.

- Config without queue policy: differs: AWS 400 InvalidArgument "Unable to validate the following destination configurations", ArgumentName1 <queue ARN>, ArgumentValue1 "Permissions on the destination queue do not allow S3 to publish notifications from this bucket"; understudy 200 (no permission check by design, internal/service/s3/notification.go:148-153)
- Config with invalid ARN: matches ("The ARN could not be parsed", ArgumentName Queue, ArgumentValue not-an-arn)
- Overlapping prefixes: message matches; differs: AWS adds <ArgumentName>FilterRule</ArgumentName> (no ArgumentValue); understudy none (internal/service/s3/notification.go:124-125)
- Overlapping event types (01-config-full): message matches; differs: AWS adds <ArgumentName>Event</ArgumentName><ArgumentValue>s3:ObjectCreated:*, s3:ObjectCreated:Put</ArgumentValue>; understudy none (internal/service/s3/notification.go:122-123; notify.OverlapError carries rule Ids, not events)
- Prefix with a space, prefix and suffix, versioned bucket config: matches (200)
- get-bucket-notification-configuration: matches (Prefix/Suffix capitalized)
- s3:TestEvent to SQS: matches (Service, Event, Time, Bucket, RequestId, HostId); RequestId and HostId forms differ (AWS 16-char uppercase id and 76-char base64; understudy UUID and 44-char base64, internal/protocol/restxml/codec.go:257-263)
- TestEvent at Lambda: n/a (Lambda configuration refused)
- eventVersion: differs: AWS "2.6" for Put, Copy, CompleteMultipartUpload and DeleteMarkerCreated; understudy "2.1" (internal/service/s3/notify/record.go:177)
- userIdentity.principalId: matches the implemented role-session shape (AWS "AWS:<AROA id>:<session>", internal/service/s3/notify/doc.go:49-55); the understudy run signed as the account, "AWS:123456789012" (root and user forms unrecorded)
- configurationId: matches for explicit Ids ("ps", "q"); generated Id n/a (the only Id-less config was refused)
- s3.object.key encoding: matches ("a+b/%C3%BC%2B1.txt")
- size and eTag on removals: differs: AWS DeleteMarkerCreated carries eTag "d41d8cd98f00b204e9800998ecf8427e" and no size; understudy neither (internal/service/s3/notify/record.go:181-182; notify/doc.go:59-60). ObjectRemoved:Delete n/a (filtered out by "ps")
- ObjectCreated:Copy object: differs: AWS adds "hasObjectAnnotation":false after sequencer; understudy omits (internal/service/s3/notify/record.go:298-304)
- versionId: matches (present on versioned bucket only, 32 chars); understudy ids repeat their first ~10 characters at offset 11 for small sequence numbers (internal/service/s3/objects.go:285-293)
- sequencer: matches width (18 uppercase hex) and per-key increase (two writes to "twice"); content differs: AWS "00" + 8 hex Unix seconds + 8 hex sub-second; understudy Unix nanoseconds (internal/service/s3/notify/doc.go:22-24)
- requestParameters.sourceIPAddress: n/a (AWS the caller's public IP; understudy 127.0.0.1 by design)
- responseElements: matches shape; id forms as TestEvent
- bucket.ownerIdentity.principalId: differs: AWS a 14-character uppercase id; understudy the 64-hex canonical user id (internal/service/s3/notification.go:290)
- SQS SenderId of an S3 delivery: differs: AWS "<AROA id>:S3-PROD-END"; understudy "s3.amazonaws.com" (internal/service/sqs/messages.go:576-577)
- Event key order: matches
- 02-lambda-events: n/a (no log group in either run)
- Latency (03-put-times.tsv): n/a for the lat-* puts (filtered out). From the 13 delivered events, AWS eventTime to SQS SentTimestamp was 556-1157 ms (median 813); understudy 0-1 ms (prompt delivery by design, docs/research/s3.md:665)

