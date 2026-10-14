# R11

- Date: 2026-10-14T07:02Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Understudy paths are under /home/austin/dev/local-aws-stack. Understudy values come from replaying r11.sh against a local understudy.

- 01 GetQueueUrl missing, with and without x-amzn-query-mode: matches (400, `x-amzn-query-error: AWS.SimpleQueueService.NonExistentQueue;Sender` both times, `{"__type":"com.amazonaws.sqs#QueueDoesNotExist","message":"The specified queue does not exist."}`).
- 02 ReceiveMessage MaxNumberOfMessages 0 / 11, WaitTimeSeconds 21: message and query-error match; `__type` differs: AWS `com.amazon.coral.service#InvalidParameterValueException`, understudy `com.amazonaws.sqs#InvalidParameterValueException` (codegen/overlays/sqs.json:54-63).
- 02 ReceiveMessage VisibilityTimeout 43201: differs: AWS reason "Must be between 0 and 43200, if provided."; understudy "Must be >= 0 and <= 43200, if provided." (internal/service/sqs/errors.go:157-158), plus `__type`.
- 02 SendMessage DelaySeconds 901: differs: AWS reason "DelaySeconds must be >= 0 and <= 900."; understudy "Must be >= 0 and <= 900, if provided." (codegen/overlays/sqs.json:137), plus `__type`.
- 02 SendMessage empty body: message and `MissingParameter;Sender` match; `__type` differs: AWS `com.amazon.coral.service#MissingRequiredParameterException`, understudy `com.amazonaws.sqs#MissingRequiredParameterException` (codegen/overlays/sqs.json:71-80).
- 02 CreateQueue MaximumMessageSize 1023, VisibilityTimeout -1: matches (`com.amazonaws.sqs#InvalidAttributeValue`, "Invalid value for the parameter X.").
- 03 unknown member, MaxNumberOfMessages 5.0: matches (200 `{}`; AWS is lenient).
- 03 string for integer: body matches (`com.amazon.coral.service#SerializationException`, `Message` "STRING_VALUE cannot be converted to Integer"); header differs: AWS `x-amzn-query-error: MalformedInput;Sender`, understudy `SerializationException;Sender` (codegen/overlays/common.json:89, no awsQueryError trait).
- 03 Content-Type x-amz-json-1.1, missing Content-Type: matches (404, no Content-Type, `<UnknownOperationException/>`).
- 03 unknown operation: body matches; header differs: AWS `InvalidAction;Sender`, understudy `UnknownOperationException;Sender` (codegen/overlays/common.json:60, no awsQueryError trait).
- 03 wrong target prefix (AmazonSNS.ListQueues): body matches; header differs: AWS sends no x-amzn-query-error, understudy sends `UnknownOperationException;Sender` (internal/edge/unclaimed.go:68 passes svc, internal/protocol/awsjson/codec.go:248-251).
- 04 DeleteMessage / SetQueueAttributes JSON: matches (200, Content-Type application/x-amz-json-1.0, Content-Length 0).
- 04 DeleteMessage Query: element content matches; differs: AWS prefixes `<?xml version="1.0"?>` (no newline), understudy has no XML declaration (internal/protocol/awsquery/codec.go:126). Same for every SQS Query body below.
- 04 ListQueues no match, ReceiveMessage empty: matches (`{}`; QueueUrls/Messages omitted).
- 04 SNS ListTopics / ListSubscriptions empty: n/a (account had topics and subscriptions from other items; AWS pretty-prints SNS XML with newlines, no XML declaration; understudy compact).
- 05 SQS unknown Action (Query): message and Code match; differs: AWS `<?xml version="1.0"?><ErrorResponse xmlns="http://queue.amazonaws.com/doc/2012-11-05/">`, understudy `<ErrorResponse>` without declaration or xmlns (internal/protocol/awsquery/codec.go:229-230).
- 05 SNS unknown Action: differs: AWS `<ErrorResponse xmlns="http://webservices.amazon.com/AWSFault/2005-15-09">`, Message "Could not find operation NoSuchAction for version 2010-03-31", no Detail; understudy no xmlns and "The action NoSuchAction is not valid for this endpoint." (internal/edge/unclaimed.go:40).
- 05 SQS Query NonExistentQueue: Code, Message, `<Detail/>` match; namespace host is queue.amazonaws.com; differs only in missing declaration and xmlns (codec.go:229-230).
- 05 Query with Accept: application/json, success and error: differs: AWS answers Content-Type application/json, `{"GetQueueUrlResponse":{"GetQueueUrlResult":{"QueueUrl":"..."},"ResponseMetadata":{"RequestId":"..."}}}` and `{"Error":{"Code":"AWS.SimpleQueueService.NonExistentQueue","Message":"The specified queue does not exist for this wsdl version.","Type":"Sender"},"RequestId":"..."}`; understudy ignores Accept and answers text/xml (internal/protocol/awsquery/codec.go:126, 159).
- 06 DynamoDB unknown target: body matches; AWS adds x-amz-crc32, understudy omits it on this error.
- 06 DynamoDB duplicate member: differs: AWS 400 `{"__type":"com.amazon.coral.service#SerializationException","Message":"Duplicate key found in payload"}`; understudy decodes last-wins and forwards (ResourceNotFoundException for the second name) (internal/protocol/awsjson/deserializer.go:90).
- 07 Streams signing name: n/a for understudy (DynamoDB Streams not emulated; 501 NotImplemented); AWS accepts `dynamodb` (200 `{"Streams":[]}`) and rejects `streams.dynamodb` with InvalidSignatureException "Credential should be scoped to correct service: 'dynamodb'. ".
- x Query SendMessage without Version (x-query-sendmessage-no-version): differs: AWS 200 SendMessageResponse; understudy 404 `<UnknownOperationException/>` because the Query claim needs Version to find the service (internal/protocol/awsquery/claim.go:71-80). With Version, understudy answers 200.
- x SendMessage 11 attributes: matches ("Number of message attributes [11] exceeds the allowed maximum [10]."), `__type` aside.
- x attribute name 257 chars: differs: AWS "Message (user) attribute name must be shorter than 256 bytes."; understudy moto's character-rule text (internal/service/sqs/messages.go:124-125, validAttributeName length check at :160).
- x DataType 257 chars: differs: AWS "Message (user) attribute type must be shorter than 256 bytes."; understudy "The message attribute 'a' has an invalid message attribute type, the set of supported type prefixes is Binary, Number, and String." (messages.go:128-129).
- backlog decision on strictness: AWS is lenient on unknown members and 5.0; understudy already matches.

