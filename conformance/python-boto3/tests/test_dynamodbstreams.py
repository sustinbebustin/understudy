"""DynamoDB Streams (docs/research/dynamodb-streams.md 3-7, 11): the stream a table's
StreamSpecification creates, served from understudy's own log, read through boto3's separate
dynamodbstreams client signed for dynamodb."""

from __future__ import annotations

import re
from collections.abc import Callable, Iterator
from typing import Any

import pytest

from support import eventually, raises, uniq

pytestmark = [pytest.mark.docker, pytest.mark.dynamodb]

# dynamodb-streams.md 4.1: the label form AWS answers and Terraform matches against AWS.
LABEL = r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}"


@pytest.fixture(scope="module")
def streams(make_client: Callable[..., Any]) -> Any:
    return make_client("dynamodbstreams")


@pytest.fixture
def make_stream_table(dynamodb: Any, cleanup: Any) -> Callable[..., dict[str, Any]]:
    def make(view: str = "NEW_AND_OLD_IMAGES") -> dict[str, Any]:
        name = uniq("conf-streamed")
        desc = dynamodb.create_table(
            TableName=name,
            KeySchema=[{"AttributeName": "id", "KeyType": "HASH"}],
            AttributeDefinitions=[{"AttributeName": "id", "AttributeType": "S"}],
            BillingMode="PAY_PER_REQUEST",
            StreamSpecification={"StreamEnabled": True, "StreamViewType": view},
        )["TableDescription"]
        cleanup.add(dynamodb.delete_table, TableName=name)
        dynamodb.get_waiter("table_exists").wait(TableName=name, WaiterConfig={"Delay": 1, "MaxAttempts": 30})
        return desc

    return make


def read_all(streams: Any, arn: str, shard: str) -> Iterator[dict[str, Any]]:
    it = streams.get_shard_iterator(StreamArn=arn, ShardId=shard, ShardIteratorType="TRIM_HORIZON")["ShardIterator"]
    # dynamodb-streams.md 4.4: "<stream ARN>|1|<token>".
    assert it.startswith(arn + "|1|")
    while it:
        page = streams.get_records(ShardIterator=it)
        yield from page["Records"]
        if not page["Records"]:
            return
        it = page.get("NextShardIterator")


def test_create_table_answers_the_stream(dynamodb: Any, streams: Any, make_stream_table: Callable[..., dict[str, Any]]) -> None:
    desc = make_stream_table()
    name, arn, label = desc["TableName"], desc["LatestStreamArn"], desc["LatestStreamLabel"]
    assert re.fullmatch(LABEL, label)
    assert re.fullmatch(rf"arn:aws:dynamodb:us-east-1:\d{{12}}:table/{re.escape(name)}/stream/{re.escape(label)}", arn)
    # dynamodb-streams.md 9.3: identical on every read.
    table = dynamodb.describe_table(TableName=name)["Table"]
    assert (table["LatestStreamArn"], table["LatestStreamLabel"]) == (arn, label)
    listed = streams.list_streams(TableName=name)["Streams"]
    assert listed == [{"StreamArn": arn, "TableName": name, "StreamLabel": label}]
    # DDB-SNAP:L708-L735 (third-party-recorded): one open shard, ENABLED.
    stream = streams.describe_stream(StreamArn=arn)["StreamDescription"]
    assert stream["StreamStatus"] == "ENABLED" and stream["StreamViewType"] == "NEW_AND_OLD_IMAGES" and stream["TableName"] == name
    assert len(stream["Shards"]) == 1 and "EndingSequenceNumber" not in stream["Shards"][0]["SequenceNumberRange"]


def test_writes_reach_the_stream(dynamodb: Any, streams: Any, make_stream_table: Callable[..., dict[str, Any]]) -> None:
    desc = make_stream_table()
    name, arn = desc["TableName"], desc["LatestStreamArn"]
    shard = streams.describe_stream(StreamArn=arn)["StreamDescription"]["Shards"][0]
    start = int(shard["SequenceNumberRange"]["StartingSequenceNumber"])
    dynamodb.put_item(TableName=name, Item={"id": {"S": "a"}, "v": {"S": "1"}})
    dynamodb.update_item(TableName=name, Key={"id": {"S": "a"}}, UpdateExpression="SET v = :v", ExpressionAttributeValues={":v": {"S": "2"}})
    dynamodb.delete_item(TableName=name, Key={"id": {"S": "a"}})
    records = list(read_all(streams, arn, shard["ShardId"]))
    # dynamodb-streams.md 7.1, 7.2 (NEW_AND_OLD_IMAGES), eventVersion 1.1 (DDB-SNAP:L761); the
    # first record above StartingSequenceNumber (DDB-TEST:L1993-L1994).
    assert [r["eventName"] for r in records] == ["INSERT", "MODIFY", "REMOVE"]
    previous = start
    for i, r in enumerate(records):
        assert r["eventSource"] == "aws:dynamodb" and r["eventVersion"] == "1.1" and r["awsRegion"] == "us-east-1"
        assert "userIdentity" not in r
        assert int(r["dynamodb"]["SequenceNumber"]) > previous
        previous = int(r["dynamodb"]["SequenceNumber"])
        assert r["dynamodb"]["SizeBytes"] >= 1 and r["dynamodb"]["ApproximateCreationDateTime"].microsecond == 0
        assert ("NewImage" in r["dynamodb"]) == (i < 2) and ("OldImage" in r["dynamodb"]) == (i > 0)


def test_missing_stream(streams: Any, make_stream_table: Callable[..., dict[str, Any]]) -> None:
    arn = make_stream_table()["LatestStreamArn"]
    missing = arn.replace("/stream/", "x/stream/")
    # STREAMS-SNAP:L90-L104 (third-party-recorded).
    with raises("ResourceNotFoundException") as exc:
        streams.describe_stream(StreamArn=missing)
    assert exc.value.response["Error"]["Message"] == f"Requested resource not found: Stream: {missing} not found"


def test_disable_closes_the_shard(dynamodb: Any, streams: Any, make_stream_table: Callable[..., dict[str, Any]]) -> None:
    desc = make_stream_table()
    name, arn = desc["TableName"], desc["LatestStreamArn"]
    dynamodb.put_item(TableName=name, Item={"id": {"S": "a"}})
    dynamodb.update_table(TableName=name, StreamSpecification={"StreamEnabled": False})
    # dynamodb-streams.md 5.3: a disable closes the shard; LatestStreamArn stays (9.3).
    stream = eventually(
        lambda: streams.describe_stream(StreamArn=arn)["StreamDescription"],
        lambda s: s["StreamStatus"] == "DISABLED",
        what="the stream to be DISABLED",
    )
    assert "EndingSequenceNumber" in stream["Shards"][0]["SequenceNumberRange"]
    table = dynamodb.describe_table(TableName=name)["Table"]
    assert table["LatestStreamArn"] == arn and "StreamSpecification" not in table
    it = streams.get_shard_iterator(StreamArn=arn, ShardId=stream["Shards"][0]["ShardId"], ShardIteratorType="TRIM_HORIZON")["ShardIterator"]
    page = streams.get_records(ShardIterator=it)
    assert len(page["Records"]) == 1 and "NextShardIterator" not in page
