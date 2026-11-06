"""Kinesis Data Streams (docs/research/kinesis.md 3-6, 10 and 13): streams, shards, tags and
records through boto3's kinesis client, which speaks awsJson1_1."""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

import pytest

from support import REGION, raises, uniq

# kinesis.md 3.3: the documented 3-shard stream (DescribeStream example).
THREE_SHARDS = [
    ("shardId-000000000000", "0", "113427455640312821154458202477256070484"),
    ("shardId-000000000001", "113427455640312821154458202477256070485", "226854911280625642308916404954512140969"),
    ("shardId-000000000002", "226854911280625642308916404954512140970", "340282366920938463463374607431768211455"),
]


@pytest.fixture
def make_stream(kinesis: Any, cleanup: Any) -> Callable[..., str]:
    """Create a stream (PROVISIONED with `shards`, or with only its name when None), wait until
    it is ACTIVE and delete it after the test."""

    def make(shards: int | None = 1) -> str:
        name = uniq("conf-kin")
        args: dict[str, Any] = {} if shards is None else {"ShardCount": shards}
        kinesis.create_stream(StreamName=name, **args)
        cleanup.add(kinesis.delete_stream, StreamName=name)
        # kinesis.md 4.2: the model's StreamExists waiter.
        kinesis.get_waiter("stream_exists").wait(StreamName=name, WaiterConfig={"Delay": 1, "MaxAttempts": 60})
        return name

    return make


def account(sts: Any) -> str:
    return str(sts.get_caller_identity()["Account"])


def test_describe_stream_summary(kinesis: Any, sts: Any, make_stream: Callable[..., str]) -> None:
    name = make_stream(2)
    s = kinesis.describe_stream_summary(StreamName=name)["StreamDescriptionSummary"]
    # kinesis.md 3.1 (ARN form), 4.4 (required members), 4.7 (24 hours by default),
    # 10.3 (PROVISIONED and two open shards for a config with only a shard count).
    assert s["StreamARN"] == f"arn:aws:kinesis:{REGION}:{account(sts)}:stream/{name}"
    assert s["StreamStatus"] == "ACTIVE" and s["RetentionPeriodHours"] == 24 and s["OpenShardCount"] == 2
    assert s["StreamModeDetails"] == {"StreamMode": "PROVISIONED"}
    assert [m for e in s["EnhancedMonitoring"] for m in e["ShardLevelMetrics"]] == []
    assert "StreamCreationTimestamp" in s


def test_stream_with_only_a_name_has_four_shards(kinesis: Any, make_stream: Callable[..., str]) -> None:
    # kinesis.md finding 4 (aws-recorded, LS test_kinesis.snapshot.json:L2-L47).
    name = make_stream(None)
    shards = kinesis.list_shards(StreamName=name)["Shards"]
    assert [s["ShardId"] for s in shards] == [f"shardId-00000000000{i}" for i in range(4)]


def test_three_shards_split_the_hash_space_evenly(kinesis: Any, make_stream: Callable[..., str]) -> None:
    name = make_stream(3)
    shards = kinesis.list_shards(StreamName=name)["Shards"]
    assert [(s["ShardId"], s["HashKeyRange"]["StartingHashKey"], s["HashKeyRange"]["EndingHashKey"]) for s in shards] == THREE_SHARDS
    # kinesis.md 3.4: an open shard has no EndingSequenceNumber.
    assert all("EndingSequenceNumber" not in s["SequenceNumberRange"] for s in shards)


def test_describe_missing_stream(kinesis: Any, sts: Any) -> None:
    # kinesis.md finding 5 (aws-recorded over CBOR, LS test_kinesis.snapshot.json:L230-L237):
    # the name and account, even when addressed by ARN.
    acct, name = account(sts), uniq("conf-kin-missing")
    with raises("ResourceNotFoundException") as missing:
        kinesis.describe_stream(StreamARN=f"arn:aws:kinesis:{REGION}:{acct}:stream/{name}")
    assert missing.value.response["Error"]["Message"] == f"Stream {name} under account {acct} not found."


def test_tags_for_stream(kinesis: Any, make_stream: Callable[..., str]) -> None:
    # kinesis.md 4.10 (aws-recorded, LS test_kinesis.snapshot.json:L49-L57).
    name = make_stream()
    kinesis.add_tags_to_stream(StreamName=name, Tags={"foo": "bar"})
    tags = kinesis.list_tags_for_stream(StreamName=name)
    assert tags["Tags"] == [{"Key": "foo", "Value": "bar"}] and tags["HasMoreTags"] is False


def test_put_and_get_records(kinesis: Any, make_stream: Callable[..., str]) -> None:
    name = make_stream()
    first = kinesis.put_record(StreamName=name, PartitionKey="k1", Data=b"hello")
    assert first["ShardId"] == "shardId-000000000000"
    # kinesis.md 5.1: empty data is legal (aws-recorded, LS test_kinesis.snapshot.json:L58-L67).
    kinesis.put_record(StreamName=name, PartitionKey="k2", Data=b"")
    # kinesis.md 5.2: one result per entry, in request order.
    batch = kinesis.put_records(StreamName=name, Records=[{"PartitionKey": "k3", "Data": b"three"}])
    assert batch.get("FailedRecordCount", 0) == 0 and len(batch["Records"]) == 1

    it = kinesis.get_shard_iterator(StreamName=name, ShardId="shardId-000000000000", ShardIteratorType="TRIM_HORIZON")["ShardIterator"]
    page = kinesis.get_records(ShardIterator=it)
    records = page["Records"]
    assert [(r["PartitionKey"], r["Data"]) for r in records] == [("k1", b"hello"), ("k2", b""), ("k3", b"three")]
    assert records[0]["SequenceNumber"] == first["SequenceNumber"]
    # kinesis.md 4.11 (aws-recorded, LS test_kinesis.snapshot.json:L58-L88): no EncryptionType
    # on an unencrypted stream's records.
    assert all("EncryptionType" not in r for r in records)
    # kinesis.md 3.4: increasing decimal sequence numbers within a shard.
    seqs = [int(r["SequenceNumber"]) for r in records]
    assert seqs == sorted(seqs) and len(set(seqs)) == len(seqs)
    # kinesis.md 5.5: an open shard always answers a NextShardIterator; an empty read returns
    # Records [] (aws-recorded, LS test_kinesis.py:L552-L575).
    tail = kinesis.get_records(ShardIterator=page["NextShardIterator"])
    assert tail["Records"] == [] and tail["NextShardIterator"]


def test_resharding_is_not_implemented(kinesis: Any, make_stream: Callable[..., str]) -> None:
    # kinesis.md 13.1 and 13.9: resharding is not emulated, so it answers 501 rather than
    # being accepted and ignored.
    name = make_stream()
    with raises("NotImplemented") as refused:
        kinesis.update_shard_count(StreamName=name, TargetShardCount=2, ScalingType="UNIFORM_SCALING")
    assert refused.value.response["ResponseMetadata"]["HTTPStatusCode"] == 501
