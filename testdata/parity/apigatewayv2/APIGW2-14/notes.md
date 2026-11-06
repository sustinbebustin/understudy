# APIGW2-14: service integrations (apigateway-http.md 14.15)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu)

## Times (bash `time`, real)

01 sqs-send 0.220 s; 03 sqs-missing-queue 0.168 s; 04 sqs-role-without-permission 0.170 s; 05 sqs-role-absent 0.145 s. The failures are no slower than the success, so there is no sign of retries (finding 2).

## Expected (6.2-6.4)

- What the client receives (6.4): the target's own response relayed in the Query (XML) protocol, not JSON. 01 SendMessage gives `200`, `Content-Type: text/xml`, `<?xml version="1.0"?><SendMessageResponse xmlns="http://queue.amazonaws.com/doc/2012-11-05/"><SendMessageResult><MessageId>..</MessageId><MD5OfMessageBody>..</MD5OfMessageBody></SendMessageResult>...`. The pre-2023 wrapper the doc called possibly stale is what AWS sends.
- Mapped value for a non-string member (02, `DelaySeconds` from `$request.header.x-delay`): accepted, 200 SendMessageResponse.
- Missing queue (03): `400 Bad Request`, `text/xml`, `<ErrorResponse ...><Error><Type>Sender</Type><Code>AWS.SimpleQueueService.NonExistentQueue</Code><Message>The specified queue does not exist for this wsdl version.</Message><Detail/>...`.
- Role without the action (04): `403 Forbidden`, `text/xml`, `AccessDenied` "User: arn:aws:sts::<account>:assumed-role/us1103-gw-none/<numeric session> is not authorized to perform: sqs:sendmessage on resource: arn:aws:sqs:us-east-2:<account>:us1103-gw-q because no identity-based policy allows the sqs:sendmessage action". The action is lower-cased.
- Role that does not exist (05): `500` `{"message":"Internal Server Error"}`, `application/json` (gateway's own answer).
- ReceiveMessage (06): `200` `text/xml` ReceiveMessageResponse; the body `{"a":1}` is XML-escaped as `{&quot;a&quot;:1}`.
- EventBridge PutEvents (07): `200`, `Content-Type: application/x-amz-json-1.1`, `{"Entries":[{"EventId":".."}],"FailedEntryCount":0}`.
- Text Detail (08): `200` `{"Entries":[{"ErrorCode":"MalformedDetail","ErrorMessage":"Detail is malformed."}],"FailedEntryCount":1}`. The partial failure is relayed as 200.
- Missing Source header (09): `400` `{"message":"Bad Request"}`, `application/json`. The gateway refuses it itself when a required mapped parameter evaluates empty.
