# R16

- Date: 2026-10-14T07:05Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Compared against understudy built from /home/austin/dev/local-aws-stack on 2026-10-14, replaying a copy of r16.sh through scripts/sealed (account 123456789012, Lambda in Docker). Paths are repo-relative.

- Lambda Records[0] (EventSource, EventVersion "1.0", EventSubscriptionArn): matches
- Lambda Sns Subject absent: matches (null)
- Lambda Sns key order: differs: AWS Type, MessageId, TopicArn, Message, Timestamp, SignatureVersion, Signature, SigningCertUrl, Subject, UnsubscribeUrl, MessageAttributes; understudy puts Subject after TopicArn (internal/service/sns/envelope/envelope.go:107-112)
- Lambda MessageAttributes Type for Number and String.Array: differs: AWS "String" for both (n "1.5", a "[\"x\",1]"); understudy the published type (switch sns.lambda.attribute-type as-published, internal/service/sns/service.go:76-85; envelope.go:123-131)
- Lambda Binary Value: matches (base64, "AAEC")
- Lambda SignatureVersion: matches ("1"); v2 topic n/a (not exercised)
- Lambda SigningCertUrl, UnsubscribeUrl casing: matches; values point at the emulator by design
- MessageAttributes order (Lambda and SQS envelope): differs: AWS is Java HashMap<String> iteration order, deterministic (s,n,b,a published -> a,b,s,n; a0..a10 -> a1,a2,a10,a3..a9,a0); understudy sorts by name (internal/service/sns/message_attrs.go:20-26; envelope/doc.go:33-34 calls AWS's order unstable)
- SQS envelope key order, Subject only when set, Timestamp format, SignatureVersion "1", MessageAttributes {"Type","Value"} one per line: matches
- SQS envelope Signature: n/a (different key; both base64 of a 2048-bit signature)
- SQS SenderId of an SNS delivery: differs: AWS "<AROA id>:AWS-CLOUDCAST"; understudy "sns.amazonaws.com" (internal/service/sqs/messages.go:576-577)
- MessageId form: differs: AWS UUID version 5 (xxxxxxxx-xxxx-5xxx-...); understudy version 4 (internal/ids/ids.go:90-92)
- publish-batch 11 entries, duplicate ids, invalid id, one oversize entry, total over 256 KiB: matches (TooManyEntriesInBatchRequest, BatchEntryIdsNotDistinct, InvalidBatchEntryId, BatchRequestTooLong x2; whole call 400)
- publish-batch invalid attribute: status and code match (whole call 400 ParameterValueInvalid); text differs as 03-attr-number-abc
- publish-batch Subject of 101: differs: AWS 200, Successful [ok], Failed [{Id bad, Code InvalidParameter, Message "Invalid parameter: Subject", SenderFault true}]; understudy 400 for the whole call (internal/service/sns/publish.go:236-241)
- Publish Subject 101 chars, Subject with newline: matches ("Invalid parameter: Subject")
- Attribute named AWS.x / Amazon.x: differs: AWS "Message attribute names starting with 'AWS.' or 'Amazon.' are reserved for use by Amazon."; understudy "Message attribute name 'AWS.x' starts with a reserved prefix; names may not start with AWS. or Amazon." (internal/service/sns/message_attrs.go:60-63)
- Attribute name 257: matches
- Number attribute "abc": differs: AWS "Could not cast message attribute 'n' value to number."; understudy "The message attribute 'n' with type 'Number' has a value that is not a number: abc" (internal/service/sns/message_attrs.go:81-83)
- 11 attributes, Publish: matches (accepted)
- 11 attributes to a raw SQS subscription: n/a (neither attribute-carrying message reached 03-sqs-raw*, but the enveloped queue also returned only 3 of 5 messages in two receives, so not conclusive)
- MessageStructure json with duplicate "default": differs: AWS accepts and delivers the last value "b" (Lambda, raw SQS, envelope); understudy 400 "Invalid parameter: Message Structure - JSON message body failed to parse" (internal/service/sns/publish.go:159-169)
- FIFO publish SequenceNumber: differs: AWS "10000000000000003000" (20 digits, first publish on a new topic); understudy "00000000000000000001" (internal/service/sns/publish.go:351). One sample, scheme unknown
- FIFO SQS envelope (SequenceNumber after MessageId, no signature fields): matches
- Subscribe lambda or http to a FIFO topic: differs: AWS "Invalid parameter: Invalid protocol type: lambda" / "...: http"; understudy "Invalid parameter: Endpoint Reason: FIFO topics support only Amazon SQS endpoints" (internal/service/sns/subscriptions.go:290-292)
- Subscribe same endpoint again: matches (same ARN); other protocol: matches ("Invalid parameter: Lambda endpoint ARN")
- ConfirmSubscription wrong token, twice: matches ("Invalid parameter: Token")
- http endpoint without scheme: matches ("Invalid parameter: Endpoint must match the specified protocol")
- application endpoint not an ARN: differs: AWS 400 InvalidParameter "Invalid parameter: Application endpoint arn invalid:not-an-arn"; understudy 501 NotImplemented (internal/service/sns/subscriptions.go:266-267)
- DeliveryPolicy malformed: differs: AWS "Invalid parameter: DeliveryPolicy: Invalid JSON: Unexpected character ('n' (code 110)) at [line: 1, column: 2]: was expecting double-quote to start field name"; understudy "Invalid parameter: DeliveryPolicy: failed to parse JSON." (internal/service/sns/topic_attributes.go:141-143)
- DeliveryPolicy unknown member: differs: AWS 400 "Invalid parameter: DeliveryPolicy: Unexpected JSON member: http.nope"; understudy 200 (internal/service/sns/delivery_policy.go:23-33)
- delete-topic twice: matches (200, 200)
- list-subscriptions after delete-topic: differs: AWS still lists the deleted topic's 3 subscriptions 5 s later; understudy lists none (internal/service/sns/subscriptions.go:516-540 walks live topics only). 48 h retention not settled
- ErrorResponse envelope: differs: AWS declares xmlns="http://sns.amazonaws.com/doc/2010-03-31/"; understudy none (internal/protocol/awsquery/codec.go:217-233)

