# DynamoDB Streams and Kinesis

This page covers the two stream sources understudy serves: DynamoDB Streams, below, and [Kinesis Data Streams](#kinesis-data-streams). Lambda event source mappings read both through the same poller.

understudy serves DynamoDB Streams from a stream log of its own, one per stream-enabled table. After every write to such a table (`PutItem`, `UpdateItem`, `DeleteItem`, `BatchWriteItem`, `TransactWriteItems`, the PartiQL writes and TTL deletes) it copies DynamoDB Local's change records into the log before it answers, so a record is readable as soon as the write returns. Labels, sequence numbers, iterators and retention run on the emulator clock. Lambda event source mappings read the log and invoke a function with each batch.

The commands below use the AWS CLI against understudy, as set up in [Getting started](getting-started.md). DynamoDB runs on DynamoDB Local, so Docker is required.

## A stream-enabled table

```sh
aws dynamodb create-table --table-name orders \
  --attribute-definitions AttributeName=id,AttributeType=S --key-schema AttributeName=id,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST --stream-specification StreamEnabled=true,StreamViewType=NEW_AND_OLD_IMAGES \
  --query 'TableDescription.[LatestStreamArn,LatestStreamLabel]'
aws dynamodbstreams list-streams
```

```text
[
    "arn:aws:dynamodb:us-east-1:000000000000:table/orders/stream/2026-10-27T20:23:33.476",
    "2026-10-27T20:23:33.476"
]
{
    "Streams": [
        {
            "StreamArn": "arn:aws:dynamodb:us-east-1:000000000000:table/orders/stream/2026-10-27T20:23:33.476",
            "TableName": "orders",
            "StreamLabel": "2026-10-27T20:23:33.476"
        }
    ]
}
```

The label is the emulator time at which the stream was enabled. `CreateTable`, `UpdateTable` and `DescribeTable` answer understudy's ARN and label, not DynamoDB Local's, and both survive a snapshot save and load ([Snapshots](snapshots.md)), with the stream's records and each mapping's place in it.

The Streams API is signed for `dynamodb`, as every SDK does. A request signed for `streams.dynamodb` is refused with 400 `InvalidSignatureException` "Credential should be scoped to correct service: 'dynamodb'.", as AWS refuses it.

## Reading records

```sh
aws dynamodb put-item --table-name orders --item '{"id":{"S":"o-1"},"status":{"S":"new"}}'
aws dynamodb update-item --table-name orders --key '{"id":{"S":"o-1"}}' --update-expression 'SET #s = :s' \
  --expression-attribute-names '{"#s":"status"}' --expression-attribute-values '{":s":{"S":"paid"}}'
STREAM=$(aws dynamodb describe-table --table-name orders --query Table.LatestStreamArn --output text)
SHARD=$(aws dynamodbstreams describe-stream --stream-arn "$STREAM" --query 'StreamDescription.Shards[0].ShardId' --output text)
IT=$(aws dynamodbstreams get-shard-iterator --stream-arn "$STREAM" --shard-id "$SHARD" \
  --shard-iterator-type TRIM_HORIZON --query ShardIterator --output text)
aws dynamodbstreams get-records --shard-iterator "$IT" \
  --query 'Records[].[eventName,dynamodb.SequenceNumber,dynamodb.NewImage.status.S,dynamodb.OldImage.status.S]' --output text
```

```text
INSERT	100000000000000000001	new	None
MODIFY	100000000000000000002	paid	new
```

Each record carries the keys and the images its view type asks for, `SizeBytes`, a 32-character `eventID` and the write's time in `ApproximateCreationDateTime`. A record of a TTL delete carries `userIdentity` `{"PrincipalId":"dynamodb.amazonaws.com","Type":"Service"}`; no other record does. All four iterator types work. Disabling the stream with `update-table --stream-specification StreamEnabled=false` closes the shard: `describe-stream` answers `DISABLED` and an `EndingSequenceNumber`, and the records stay readable.

Iterators expire 15 minutes after they are issued, and records are trimmed 24 hours after they arrive, both on the emulator clock, so a test can step past either:

```sh
understudy clock advance 16m
aws dynamodbstreams get-records --shard-iterator "$IT"
```

```text
aws: [ERROR]: An error occurred (ExpiredIteratorException) when calling the GetRecords operation: Shard iterator has expired
```

After `understudy clock advance 24h`, a new `TRIM_HORIZON` iterator reads no records. A deleted table's stream stays listed, `DISABLED` and readable, until 24 hours after the delete.

## Delivering to Lambda

A function that logs each record's event name and keys:

```python
# handler.py
import json


def handler(event, context):
    for record in event["Records"]:
        print(record["eventName"], json.dumps(record["dynamodb"]["Keys"]))
```

```sh
python3 -m zipfile -c function.zip handler.py
aws lambda create-function --function-name order-events --runtime python3.13 --handler handler.handler \
  --role arn:aws:iam::000000000000:role/worker --zip-file fileb://function.zip
aws lambda create-event-source-mapping --function-name order-events --event-source-arn "$STREAM" \
  --starting-position LATEST --batch-size 10
```

```text
{
    "UUID": "2398bb0d-faf8-43cf-987c-333548540987",
    "StartingPosition": "LATEST",
    "BatchSize": 10,
    "MaximumBatchingWindowInSeconds": 0,
    "ParallelizationFactor": 1,
    "EventSourceArn": "arn:aws:dynamodb:us-east-1:000000000000:table/orders/stream/2026-10-27T20:23:33.476",
    "FunctionArn": "arn:aws:lambda:us-east-1:000000000000:function:order-events",
    "LastModified": "2026-10-27T20:27:28.131000+00:00",
    "LastProcessingResult": "No records processed",
    "State": "Enabled",
    "StateTransitionReason": "User action",
    "DestinationConfig": {
        "OnFailure": {}
    },
    "MaximumRecordAgeInSeconds": -1,
    "BisectBatchOnFunctionError": false,
    "MaximumRetryAttempts": -1,
    "TumblingWindowInSeconds": 0,
    "FunctionResponseTypes": [],
    "EventSourceMappingArn": "arn:aws:lambda:us-east-1:000000000000:event-source-mapping:2398bb0d-faf8-43cf-987c-333548540987"
}
```

`LATEST` delivers every record written after `create-event-source-mapping` returns; `TRIM_HORIZON` starts from the oldest record still in the stream.

```sh
aws dynamodb put-item --table-name orders --item '{"id":{"S":"o-2"},"status":{"S":"new"}}'
aws dynamodb delete-item --table-name orders --key '{"id":{"S":"o-1"}}'
aws logs filter-log-events --log-group-name /aws/lambda/order-events --filter-pattern '?INSERT ?REMOVE' --query 'events[].message'
```

```text
[
    "INSERT {\"id\": {\"S\": \"o-2\"}}\n",
    "REMOVE {\"id\": {\"S\": \"o-1\"}}\n"
]
```

The mapping reads a stream as soon as a record arrives, rather than polling it four times a second as AWS does, so the delivery is not delayed and an idle mapping does not keep `/_understudy/v1/settle` waiting. It batches up to `BatchSize` records, the batching window or 6 MB of event, whichever comes first, and resumes where it left off after a disable and re-enable or a snapshot load. The state of a mapping is a checkpoint per shard: when it falls behind the 24-hour trim, the lost records are dropped, the mapping resumes at the oldest record left, and the journal says so.

## Failures

A batch whose invocation fails is retried, 250 milliseconds of emulator time after the failure, until `MaximumRetryAttempts` or `MaximumRecordAgeInSeconds` ends it; by default neither does, so a failing record blocks its shard until it is trimmed, as on AWS. With `FunctionResponseTypes ReportBatchItemFailures`, the function's `batchItemFailures` answer is read as AWS reads it. An on-failure destination receives a record describing each discarded batch:

```python
# failing.py
def handler(event, context):
    raise RuntimeError("payment service unavailable")
```

```sh
python3 -m zipfile -c failing.zip failing.py
aws sqs create-queue --queue-name order-failures
aws lambda create-function --function-name charge --runtime python3.13 --handler failing.handler \
  --role arn:aws:iam::000000000000:role/worker --zip-file fileb://failing.zip
aws lambda create-event-source-mapping --function-name charge --event-source-arn "$STREAM" \
  --starting-position LATEST --maximum-retry-attempts 1 \
  --destination-config '{"OnFailure":{"Destination":"arn:aws:sqs:us-east-1:000000000000:order-failures"}}'
aws dynamodb put-item --table-name orders --item '{"id":{"S":"o-3"},"status":{"S":"new"}}'
aws sqs receive-message --queue-url "$(aws sqs get-queue-url --queue-name order-failures --query QueueUrl --output text)" \
  --wait-time-seconds 5 --query 'Messages[0].Body' --output text | jq .
```

```text
{
  "requestContext": {
    "requestId": "924b07e6-a086-41b3-8bdc-52c5eb307c48",
    "functionArn": "arn:aws:lambda:us-east-1:000000000000:function:charge",
    "condition": "RetryAttemptsExhausted",
    "approximateInvokeCount": 2
  },
  "responseContext": {
    "statusCode": 200,
    "executedVersion": "$LATEST",
    "functionError": "Unhandled"
  },
  "version": "1.0",
  "timestamp": "2026-10-27T20:28:44.796Z",
  "DDBStreamBatchInfo": {
    "shardId": "shardId-00000001793132613476-e525994b",
    "startSequenceNumber": "100000000000000000005",
    "endSequenceNumber": "100000000000000000005",
    "approximateArrivalOfFirstRecord": "2026-10-27T20:28:44Z",
    "approximateArrivalOfLastRecord": "2026-10-27T20:28:44Z",
    "batchSize": 1,
    "streamArn": "arn:aws:dynamodb:us-east-1:000000000000:table/orders/stream/2026-10-27T20:23:33.476"
  }
}
```

The record is sent as the mapping's execution role and journaled as an `esm-to-sqs` delivery (`esm-to-sns` for a topic):

```sh
understudy journal --kind delivery
```

```text
110    2026-10-27T20:28:44.796306532Z  delivery       000000000000/us-east-1 esm-to-sqs 60fd33f3-6355-4ac7-8033-eb55107a4c84 -> order-failures attempt 1 delivered
```

## IAM

The mapping reads the stream as its execution role. `understudy iam policy --principal <role ARN>` lists the `dynamodb:DescribeStream`, `dynamodb:GetShardIterator` and `dynamodb:GetRecords` statements it needs on the stream, and the `sqs:SendMessage` or `sns:Publish` its on-failure destination needs. Under `--iam-enforce enforce` a mapping whose role may not read the stream is refused at create, reads the role loses are denied and journaled, and an on-failure record the role may not send is dropped with a journal entry ([Enforce](policy.md#enforce)).

## Limitations

- A stream has one shard, which never splits or rolls over; AWS opened a new on-demand table's stream with 4 shards and replaced each with a child about every four hours. understudy trims records from its one shard after 24 hours, answering `TrimmedDataAccessException` for a position behind the trim; AWS was not seen to trim, but removed a closed shard whole about a day after it closed, after which `get-shard-iterator` for it answers `ResourceNotFoundException` "Shard does not exist". A stream is `ENABLED` or `DISABLED` at once; AWS reports `ENABLING` for about 7 seconds and `DISABLING` for about 8.
- Record sequence numbers are 21 digits, one above the shard's `StartingSequenceNumber`; AWS's are 23 digits, far above it. Compare them as integers.
- A DynamoDB table's Kinesis streaming destination answers 501.
- On a stream mapping, `ParallelizationFactor` above 1, `BisectBatchOnFunctionError`, `TumblingWindowInSeconds` above 0, a `FilterCriteria` pattern that compiles and an S3 on-failure destination answer 501 `NotImplemented`, naming the setting. A pattern that does not compile answers AWS's `InvalidParameterValueException`.
- Stream ARNs cannot be tagged: `TagResource`, `UntagResource` and `ListTagsOfResource` on one answer 501. AWS tags existing streams.
- More than two concurrent readers of a shard are never throttled; inject `LimitExceededException` with a [fault rule](faults.md).
- The error texts are AWS's as recorded, except that a constraint violation's text quotes the value AWS's omits, and `ExpiredIteratorException` carries DynamoDB's `__type` namespace where AWS's is `com.amazonaws.dynamodb.streams.v20120810`. `understudy coverage --service dynamodbstreams` lists each operation's differences, and [Differences from AWS](differences.md#dynamodb) the recorded ones.

## Kinesis Data Streams

understudy serves Kinesis Data Streams itself; no container is needed. Streams, shards, records, sequence numbers, iterators and retention live in understudy's state, run on the emulator clock and survive a snapshot save and load ([Snapshots](snapshots.md)). The AWS CLI, boto3 and the Go and JavaScript SDKs speak the JSON protocol understudy serves; Java clients need two settings ([Java, KCL and JavaScript clients](#java-kcl-and-javascript-clients)).

```sh
aws kinesis create-stream --stream-name orders --shard-count 2
aws kinesis describe-stream-summary --stream-name orders
```

```text
{
    "StreamDescriptionSummary": {
        "StreamName": "orders",
        "StreamARN": "arn:aws:kinesis:us-east-1:000000000000:stream/orders",
        "StreamStatus": "ACTIVE",
        "StreamModeDetails": {
            "StreamMode": "PROVISIONED"
        },
        "RetentionPeriodHours": 24,
        "StreamCreationTimestamp": "2026-10-30T01:07:21.001000+00:00",
        "EnhancedMonitoring": [
            {
                "ShardLevelMetrics": []
            }
        ],
        "EncryptionType": "NONE",
        "OpenShardCount": 2,
        "MaxRecordSizeInKiB": 1024
    }
}
```

The stream is `ACTIVE` as soon as `create-stream` returns. The shards split the hash key space evenly, as the documented three-shard example does. A stream created with only a name is `ON_DEMAND` with four shards. Retention can be raised and lowered between 24 and 8,760 hours, and tags work through both the stream and the resource APIs. Terraform's `aws_kinesis_stream` applies with an empty second plan.

## Reading Kinesis records

```sh
aws kinesis put-record --stream-name orders --partition-key o-1 \
  --data '{"id":"o-1","status":"new"}' --cli-binary-format raw-in-base64-out
aws kinesis put-record --stream-name orders --partition-key o-1 \
  --data '{"id":"o-1","status":"paid"}' --cli-binary-format raw-in-base64-out --query ShardId --output text
IT=$(aws kinesis get-shard-iterator --stream-name orders --shard-id shardId-000000000001 \
  --shard-iterator-type TRIM_HORIZON --query ShardIterator --output text)
aws kinesis get-records --shard-iterator "$IT" --query 'Records[].[PartitionKey,SequenceNumber]' --output text
```

```text
{
    "ShardId": "shardId-000000000001",
    "SequenceNumber": "49679736136779511679952514256454943654176979123706003474"
}
shardId-000000000001
o-1	49679736136779511679952514256454943654176979123706003474
o-1	49679736136779511679952514256456152579996594508794953746
```

A partition key's MD5 hash picks the shard, or `ExplicitHashKey` does. Sequence numbers have AWS's 56-digit form and increase within a shard. All five iterator types work, and `get-records` never waits: it returns what the shard holds, with a `NextShardIterator` every time.

Iterators expire 5 minutes after they are issued, and records are trimmed when the stream's retention passes, both on the emulator clock:

```sh
understudy clock advance 6m
aws kinesis get-records --shard-iterator "$IT"
```

```text
aws: [ERROR]: An error occurred (ExpiredIteratorException) when calling the GetRecords operation: Iterator expired. The iterator was created at time Fri Oct 30 01:07:52 UTC 2026 while right now it is Fri Oct 30 01:13:53 UTC 2026 which is further in the future than the tolerated delay of 300000 milliseconds.
```

After `understudy clock advance 24h`, a new `TRIM_HORIZON` iterator reads no records.

## Delivering Kinesis records to Lambda

A function that logs each record's partition key and data:

```python
# handler.py
import base64


def handler(event, context):
    for record in event["Records"]:
        k = record["kinesis"]
        print(k["partitionKey"], base64.b64decode(k["data"]).decode())
```

```sh
python3 -m zipfile -c function.zip handler.py
aws lambda create-function --function-name order-events --runtime python3.13 --handler handler.handler \
  --role arn:aws:iam::000000000000:role/worker --zip-file fileb://function.zip
aws lambda create-event-source-mapping --function-name order-events \
  --event-source-arn arn:aws:kinesis:us-east-1:000000000000:stream/orders --starting-position LATEST --batch-size 10
aws kinesis put-record --stream-name orders --partition-key o-3 \
  --data '{"id":"o-3","status":"new"}' --cli-binary-format raw-in-base64-out
aws logs filter-log-events --log-group-name /aws/lambda/order-events --filter-pattern '"o-3"' --query 'events[].message'
```

```text
[
    "o-3 {\"id\":\"o-3\",\"status\":\"new\"}\n"
]
```

Each record arrives in AWS's form: `eventSource` `aws:kinesis`, `eventID` `<shard id>:<sequence number>`, `invokeIdentityArn` the execution role, and `kinesis.data` the record's bytes in base64. One reader per shard batches up to `BatchSize` records, the batching window or 6 MB of event. As for DynamoDB streams, it reads a shard as soon as a record arrives rather than once a second as AWS does, resumes at its checkpoint after a disable and re-enable or a snapshot load, and drops records that age out of the stream's retention, resuming at the oldest record left.

A failing batch is retried one second of emulator time after the failure, until `MaximumRetryAttempts` or `MaximumRecordAgeInSeconds` ends it. `ReportBatchItemFailures` works as AWS documents it, and an on-failure destination receives a record with `KinesisBatchInfo`:

```python
# failing.py
def handler(event, context):
    raise RuntimeError("payment service unavailable")
```

```sh
python3 -m zipfile -c failing.zip failing.py
aws sqs create-queue --queue-name order-failures
aws lambda create-function --function-name charge --runtime python3.13 --handler failing.handler \
  --role arn:aws:iam::000000000000:role/worker --zip-file fileb://failing.zip
aws lambda create-event-source-mapping --function-name charge \
  --event-source-arn arn:aws:kinesis:us-east-1:000000000000:stream/orders --starting-position LATEST \
  --maximum-retry-attempts 1 --destination-config '{"OnFailure":{"Destination":"arn:aws:sqs:us-east-1:000000000000:order-failures"}}'
aws kinesis put-record --stream-name orders --partition-key o-4 \
  --data '{"id":"o-4","status":"new"}' --cli-binary-format raw-in-base64-out
aws sqs receive-message --queue-url "$(aws sqs get-queue-url --queue-name order-failures --query QueueUrl --output text)" \
  --wait-time-seconds 10 --query 'Messages[0].Body' --output text | jq .
```

```text
{
  "requestContext": {
    "requestId": "bdcca587-4ba6-49f7-b460-cc6871027376",
    "functionArn": "arn:aws:lambda:us-east-1:000000000000:function:charge",
    "condition": "RetryAttemptsExhausted",
    "approximateInvokeCount": 2
  },
  "responseContext": {
    "statusCode": 200,
    "executedVersion": "$LATEST",
    "functionError": "Unhandled"
  },
  "version": "1.0",
  "timestamp": "2026-10-31T01:14:33.838Z",
  "KinesisBatchInfo": {
    "shardId": "shardId-000000000000",
    "startSequenceNumber": "49679736136757210934753983633314616861729911685064425474",
    "endSequenceNumber": "49679736136757210934753983633314616861729911685064425474",
    "approximateArrivalOfFirstRecord": "2026-10-31T01:14:32.449Z",
    "approximateArrivalOfLastRecord": "2026-10-31T01:14:32.449Z",
    "batchSize": 1,
    "streamArn": "arn:aws:kinesis:us-east-1:000000000000:stream/orders"
  }
}
```

The mapping reads the stream as its execution role: `understudy iam policy --principal <role ARN>` lists `kinesis:DescribeStreamSummary`, `kinesis:ListShards`, `kinesis:GetShardIterator` and `kinesis:GetRecords` on the stream. Under `--iam-enforce enforce`, a mapping whose role may not call `DescribeStreamSummary` is refused at create, and every Kinesis call asks IAM for its own action on the stream it names ([Enforce](policy.md#enforce)).

## Java, KCL and JavaScript clients

understudy speaks the JSON protocol over HTTP/1.1. Some clients default to something else:

- **AWS SDK for Java v2** sends Kinesis requests in CBOR. Turn it off with the environment variable `CBOR_ENABLED=false` or the system property `-Daws.cborEnabled=false`; a CBOR request otherwise gets a 501 `NotImplemented` that names both settings.
- **`KinesisAsyncClient`**, on Netty, opens `http://` endpoints with HTTP/2. Build its HTTP client with `NettyNioAsyncHttpClient.builder().protocol(Protocol.HTTP1_1)`. Otherwise understudy answers the HTTP/2 preface with a 501 that the client cannot read, and logs `a client sent an HTTP/2 prior-knowledge preface; understudy speaks HTTP/1.1 only`.
- **The Kinesis Client Library** uses both, and by default reads through enhanced fan-out (`SubscribeToShard`), which understudy does not serve. Give it the two settings above and a polling retrieval configuration, `new PollingConfig(streamName, kinesisClient)` as the `retrievalSpecificConfig` of its `RetrievalConfig`.
- **AWS SDK for JavaScript v3** on Node also defaults to HTTP/2 for Kinesis and fails with `Protocol error`. Pass an HTTP/1.1 handler: `new KinesisClient({ requestHandler: new NodeHttpHandler() })`, with `NodeHttpHandler` from `@smithy/node-http-handler`.

## Kinesis limitations

- Streams never reshard or scale: `SplitShard`, `MergeShards`, `UpdateShardCount` and `UpdateStreamMode` answer 501, and an on-demand stream keeps its four shards.
- Enhanced fan-out consumers, `SubscribeToShard`, server-side encryption, resource policies, warm throughput, `RecordDistributionStrategy` `AUTO`, channels and `DryRun` answer 501. A Kinesis mapping on a consumer ARN, or at `AT_TIMESTAMP`, answers 501 as well.
- Per-shard read and write limits are not enforced; inject `ProvisionedThroughputExceededException` with a [fault rule](faults.md). A rule fails a whole `PutRecords` call; no single entry fails.
- Enhanced monitoring is stored and reported on the stream; no metrics reach CloudWatch.
- Many error texts follow kinesalite, an open-source emulator, where AWS's are unrecorded. `understudy coverage --service kinesis` lists each operation's differences.
