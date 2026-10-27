# DynamoDB Streams

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
aws: [ERROR]: An error occurred (ExpiredIteratorException) when calling the GetRecords operation: The provided iterator exceeds the maximum age allowed.
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

- A stream has one shard, which never splits or rolls over; AWS opens a shard per table partition. A stream is `ENABLED` or `DISABLED` at once, never `ENABLING` or `DISABLING`.
- Kinesis Data Streams is not emulated yet; a Kinesis mapping answers 501, as does a DynamoDB table's Kinesis streaming destination.
- On a stream mapping, `ParallelizationFactor` above 1, `BisectBatchOnFunctionError`, `TumblingWindowInSeconds` above 0, a `FilterCriteria` pattern that compiles and an S3 on-failure destination answer 501 `NotImplemented`, naming the setting. A pattern that does not compile answers AWS's `InvalidParameterValueException`.
- Stream ARNs cannot be tagged: `TagResource`, `UntagResource` and `ListTagsOfResource` on one answer 501. AWS tags existing streams.
- More than two concurrent readers of a shard are never throttled; inject `LimitExceededException` with a [fault rule](faults.md).
- Several error texts are DynamoDB Local's or understudy's where AWS's are unrecorded. `understudy coverage --service dynamodbstreams` lists each operation's differences, and [Differences from AWS](differences.md#dynamodb) the recorded ones.
