"""DynamoDB (docs/research/phase1/dynamodb-local.md): the data plane through understudy's
DynamoDB Local proxy, conditional writes (the lock-table pattern), the TTL shim on the emulator
clock, tags, continuous backups and per account and region isolation."""

from __future__ import annotations

import re
import time
from collections.abc import Callable
from decimal import Decimal
from typing import Any

import pytest
from boto3.dynamodb.types import Binary, TypeDeserializer, TypeSerializer
from botocore.exceptions import ClientError

from support import Admin, eventually, raises, status_of, uniq

pytestmark = [pytest.mark.docker, pytest.mark.dynamodb]

SERIALIZER = TypeSerializer()
DESERIALIZER = TypeDeserializer()
DAY = 86400


def ddb_item(**values: Any) -> dict[str, Any]:
    return {k: SERIALIZER.serialize(v) for k, v in values.items()}


def plain(item: dict[str, Any]) -> dict[str, Any]:
    return {k: DESERIALIZER.deserialize(v) for k, v in item.items()}


@pytest.fixture
def make_table(dynamodb: Any, cleanup: Any) -> Callable[..., str]:
    def make(
        prefix: str = "conf-table",
        *,
        hash_key: str = "pk",
        range_key: str | None = None,
        client: Any = None,
        **extra: Any,
    ) -> str:
        db = client or dynamodb
        name = uniq(prefix)
        key_schema = [{"AttributeName": hash_key, "KeyType": "HASH"}]
        definitions = [{"AttributeName": hash_key, "AttributeType": "S"}]
        if range_key:
            key_schema.append({"AttributeName": range_key, "KeyType": "RANGE"})
            definitions.append({"AttributeName": range_key, "AttributeType": "S"})
        for gsi in extra.get("GlobalSecondaryIndexes", []):
            for k in gsi["KeySchema"]:
                if k["AttributeName"] not in {d["AttributeName"] for d in definitions}:
                    definitions.append({"AttributeName": k["AttributeName"], "AttributeType": "S"})
        db.create_table(
            TableName=name,
            KeySchema=key_schema,
            AttributeDefinitions=definitions,
            BillingMode="PAY_PER_REQUEST",
            **extra,
        )
        cleanup.add(db.delete_table, TableName=name)
        db.get_waiter("table_exists").wait(TableName=name, WaiterConfig={"Delay": 1, "MaxAttempts": 30})
        return name

    return make


def test_create_table_description(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table(range_key="sk")
    table = dynamodb.describe_table(TableName=name)["Table"]
    assert table["TableName"] == name
    assert table["TableStatus"] == "ACTIVE"
    assert re.fullmatch(rf"arn:aws:dynamodb:us-east-1:\d{{12}}:table/{re.escape(name)}", table["TableArn"])
    assert re.fullmatch(r"[0-9a-f-]{36}", table["TableId"])
    assert table["KeySchema"] == [{"AttributeName": "pk", "KeyType": "HASH"}, {"AttributeName": "sk", "KeyType": "RANGE"}]
    assert {(d["AttributeName"], d["AttributeType"]) for d in table["AttributeDefinitions"]} == {("pk", "S"), ("sk", "S")}
    assert table["BillingModeSummary"]["BillingMode"] == "PAY_PER_REQUEST"
    assert table["ItemCount"] == 0


def test_list_tables_paginates(dynamodb: Any, make_table: Callable[..., str]) -> None:
    names = {make_table() for _ in range(3)}
    listed: set[str] = set()
    for page in dynamodb.get_paginator("list_tables").paginate(PaginationConfig={"PageSize": 2}):
        assert len(page["TableNames"]) <= 2
        listed.update(page["TableNames"])
    assert names <= listed


def test_missing_table(dynamodb: Any) -> None:
    name = uniq("conf-missing")
    with raises("ResourceNotFoundException") as exc:
        dynamodb.describe_table(TableName=name)
    assert status_of(exc.value) == 400
    with raises("ResourceNotFoundException"):
        dynamodb.put_item(TableName=name, Item=ddb_item(pk="x"))


def test_duplicate_table(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    with raises("ResourceInUseException"):
        dynamodb.create_table(
            TableName=name,
            KeySchema=[{"AttributeName": "pk", "KeyType": "HASH"}],
            AttributeDefinitions=[{"AttributeName": "pk", "AttributeType": "S"}],
            BillingMode="PAY_PER_REQUEST",
        )


def test_delete_table(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    out = dynamodb.delete_table(TableName=name)
    assert out["TableDescription"]["TableName"] == name
    dynamodb.get_waiter("table_not_exists").wait(TableName=name, WaiterConfig={"Delay": 1, "MaxAttempts": 30})
    with raises("ResourceNotFoundException"):
        dynamodb.describe_table(TableName=name)


def test_table_tags(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table(Tags=[{"Key": "env", "Value": "test"}])
    arn = dynamodb.describe_table(TableName=name)["Table"]["TableArn"]
    assert dynamodb.list_tags_of_resource(ResourceArn=arn)["Tags"] == [{"Key": "env", "Value": "test"}]
    dynamodb.tag_resource(ResourceArn=arn, Tags=[{"Key": "team", "Value": "a"}, {"Key": "env", "Value": "prod"}])
    tags = {t["Key"]: t["Value"] for t in dynamodb.list_tags_of_resource(ResourceArn=arn)["Tags"]}
    assert tags == {"env": "prod", "team": "a"}
    dynamodb.untag_resource(ResourceArn=arn, TagKeys=["env"])
    assert dynamodb.list_tags_of_resource(ResourceArn=arn)["Tags"] == [{"Key": "team", "Value": "a"}]


def test_continuous_backups_describe_and_update(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    backups = dynamodb.describe_continuous_backups(TableName=name)["ContinuousBackupsDescription"]
    assert backups["ContinuousBackupsStatus"] == "ENABLED"
    assert backups["PointInTimeRecoveryDescription"]["PointInTimeRecoveryStatus"] == "DISABLED"

    dynamodb.update_continuous_backups(TableName=name, PointInTimeRecoverySpecification={"PointInTimeRecoveryEnabled": True})
    pitr = dynamodb.describe_continuous_backups(TableName=name)["ContinuousBackupsDescription"]["PointInTimeRecoveryDescription"]
    assert pitr["PointInTimeRecoveryStatus"] == "ENABLED"
    assert pitr["EarliestRestorableDateTime"] <= pitr["LatestRestorableDateTime"]


def test_tables_are_isolated_per_account_and_region(
    dynamodb: Any, make_client: Callable[..., Any], make_table: Callable[..., str]
) -> None:
    name = make_table()
    other_account = make_client("dynamodb", access_key="333333333333")
    other_region = make_client("dynamodb", region="eu-west-1")

    assert name not in other_account.list_tables()["TableNames"]
    assert name not in other_region.list_tables()["TableNames"]
    with raises("ResourceNotFoundException"):
        other_account.describe_table(TableName=name)
    with raises("ResourceNotFoundException"):
        other_region.describe_table(TableName=name)

    # The same table name is free in the other account and in the other region.
    twin_account = make_table("twin", client=other_account)
    twin_region = make_table("twin", client=other_region)
    other_account.put_item(TableName=twin_account, Item=ddb_item(pk="a", v=1))
    assert "Item" not in dynamodb.get_item(TableName=name, Key=ddb_item(pk="a"))
    assert other_account.describe_table(TableName=twin_account)["Table"]["TableArn"].split(":")[4] == "333333333333"
    assert other_region.describe_table(TableName=twin_region)["Table"]["TableArn"].split(":")[3] == "eu-west-1"


def test_every_attribute_type_round_trips(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    item = {
        "pk": "types",
        "s": "text é\U0001f600",
        "n": Decimal("123.456"),
        "big": Decimal("1E+30"),
        "b": Binary(b"\x00\xff"),
        "t": True,
        "f": False,
        "null": None,
        "l": ["a", Decimal(1), None, [True]],
        "m": {"nested": {"deep": Decimal(7)}, "list": [1, 2]},
        "ss": {"x", "y"},
        "ns": {Decimal(1), Decimal("2.5")},
        "bs": {Binary(b"a"), Binary(b"b")},
    }
    dynamodb.put_item(TableName=name, Item=ddb_item(**item))
    got = plain(dynamodb.get_item(TableName=name, Key=ddb_item(pk="types"), ConsistentRead=True)["Item"])
    assert got["s"] == item["s"] and got["n"] == item["n"] and got["big"] == item["big"]
    assert got["b"] == item["b"] and got["t"] is True and got["f"] is False and got["null"] is None
    assert got["l"] == [ "a", Decimal(1), None, [True]] and got["m"]["nested"]["deep"] == Decimal(7)
    assert got["ss"] == item["ss"] and got["ns"] == item["ns"] and got["bs"] == item["bs"]


def test_get_missing_item_has_no_item_key(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    assert "Item" not in dynamodb.get_item(TableName=name, Key=ddb_item(pk="nope"))


def test_put_item_return_values_and_overwrite(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    assert "Attributes" not in dynamodb.put_item(TableName=name, Item=ddb_item(pk="k", v=1), ReturnValues="ALL_OLD")
    old = dynamodb.put_item(TableName=name, Item=ddb_item(pk="k", v=2), ReturnValues="ALL_OLD")["Attributes"]
    assert plain(old) == {"pk": "k", "v": Decimal(1)}


def test_put_item_validation(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    with raises("ValidationException") as exc:
        dynamodb.put_item(TableName=name, Item=ddb_item(other="x"))
    assert "One of the required keys was not given a value" in exc.value.response["Error"]["Message"]
    with raises("ValidationException") as exc:
        dynamodb.put_item(TableName=name, Item={"pk": {"N": "1"}})
    assert exc.value.response["Error"]["Message"].startswith("One or more parameter values were invalid: Type mismatch for key")


def test_item_size_limit(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    with raises("ValidationException") as exc:
        dynamodb.put_item(TableName=name, Item=ddb_item(pk="big", blob="x" * 410_000))
    assert "Item size has exceeded the maximum allowed size" in exc.value.response["Error"]["Message"]


def test_update_item_expressions(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="k", count=1, tags={"a"}, gone="x"))
    out = dynamodb.update_item(
        TableName=name,
        Key=ddb_item(pk="k"),
        UpdateExpression="SET #c = #c + :inc, label = if_not_exists(label, :label) ADD tags :t REMOVE gone",
        ExpressionAttributeNames={"#c": "count"},
        ExpressionAttributeValues={":inc": {"N": "4"}, ":label": {"S": "first"}, ":t": {"SS": ["b"]}},
        ReturnValues="ALL_NEW",
    )
    assert plain(out["Attributes"]) == {"pk": "k", "count": Decimal(5), "label": "first", "tags": {"a", "b"}}

    again = dynamodb.update_item(
        TableName=name,
        Key=ddb_item(pk="k"),
        UpdateExpression="SET label = if_not_exists(label, :label)",
        ExpressionAttributeValues={":label": {"S": "second"}},
        ReturnValues="UPDATED_NEW",
    )
    assert plain(again["Attributes"]) == {"label": "first"}


def test_update_item_creates_a_missing_item(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    dynamodb.update_item(
        TableName=name, Key=ddb_item(pk="new"), UpdateExpression="SET v = :v", ExpressionAttributeValues={":v": {"N": "1"}}
    )
    assert plain(dynamodb.get_item(TableName=name, Key=ddb_item(pk="new"))["Item"]) == {"pk": "new", "v": Decimal(1)}


def test_delete_item_returns_old_values(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="k", v=1))
    old = dynamodb.delete_item(TableName=name, Key=ddb_item(pk="k"), ReturnValues="ALL_OLD")["Attributes"]
    assert plain(old) == {"pk": "k", "v": Decimal(1)}
    assert "Item" not in dynamodb.get_item(TableName=name, Key=ddb_item(pk="k"))
    assert "Attributes" not in dynamodb.delete_item(TableName=name, Key=ddb_item(pk="k"), ReturnValues="ALL_OLD")


def test_put_if_not_exists_fails_the_second_time(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="k", v=1), ConditionExpression="attribute_not_exists(pk)")
    with raises("ConditionalCheckFailedException") as exc:
        dynamodb.put_item(TableName=name, Item=ddb_item(pk="k", v=2), ConditionExpression="attribute_not_exists(pk)")
    assert status_of(exc.value) == 400
    assert exc.value.response["Error"]["Message"] == "The conditional request failed"
    assert plain(dynamodb.get_item(TableName=name, Key=ddb_item(pk="k"))["Item"])["v"] == Decimal(1)


def test_failed_condition_can_return_the_existing_item(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="k", owner="alice"))
    with raises("ConditionalCheckFailedException") as exc:
        dynamodb.put_item(
            TableName=name,
            Item=ddb_item(pk="k", owner="bob"),
            ConditionExpression="attribute_not_exists(pk)",
            ReturnValuesOnConditionCheckFailure="ALL_OLD",
        )
    assert plain(exc.value.response["Item"]) == {"pk": "k", "owner": "alice"}


LOCK_CONDITION = "attribute_not_exists(LockID) OR ExpiresAt < :now"


def acquire(dynamodb: Any, table: str, lock: str, owner: str, now: int, ttl: int) -> None:
    dynamodb.put_item(
        TableName=table,
        Item=ddb_item(LockID=lock, Owner=owner, ExpiresAt=now + ttl),
        ConditionExpression=LOCK_CONDITION,
        ExpressionAttributeValues={":now": {"N": str(now)}},
    )


def test_lock_table_pattern(dynamodb: Any, make_table: Callable[..., str]) -> None:
    table = make_table("conf-locks", hash_key="LockID")
    now = int(time.time())

    acquire(dynamodb, table, "job-1", "worker-a", now, 60)
    with raises("ConditionalCheckFailedException"):
        acquire(dynamodb, table, "job-1", "worker-b", now + 30, 60)
    assert plain(dynamodb.get_item(TableName=table, Key=ddb_item(LockID="job-1"))["Item"])["Owner"] == "worker-a"

    # Another lock is independent.
    acquire(dynamodb, table, "job-2", "worker-b", now, 60)

    # Once ExpiresAt has passed, the lock is up for grabs again.
    acquire(dynamodb, table, "job-1", "worker-b", now + 61, 60)
    held = plain(dynamodb.get_item(TableName=table, Key=ddb_item(LockID="job-1"))["Item"])
    assert (held["Owner"], held["ExpiresAt"]) == ("worker-b", Decimal(now + 121))


def test_release_only_by_the_owner(dynamodb: Any, make_table: Callable[..., str]) -> None:
    table = make_table("conf-locks", hash_key="LockID")
    acquire(dynamodb, table, "job", "worker-a", int(time.time()), 60)
    # OWNER is a DynamoDB reserved word, so the expression goes through a name placeholder.
    with raises("ConditionalCheckFailedException"):
        dynamodb.delete_item(
            TableName=table,
            Key=ddb_item(LockID="job"),
            ConditionExpression="#owner = :me",
            ExpressionAttributeNames={"#owner": "Owner"},
            ExpressionAttributeValues={":me": {"S": "worker-b"}},
        )
    dynamodb.delete_item(
        TableName=table,
        Key=ddb_item(LockID="job"),
        ConditionExpression="#owner = :me",
        ExpressionAttributeNames={"#owner": "Owner"},
        ExpressionAttributeValues={":me": {"S": "worker-a"}},
    )
    assert "Item" not in dynamodb.get_item(TableName=table, Key=ddb_item(LockID="job"))


def test_conditional_update_with_a_version_check(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="k", version=1, state="new"))
    update = {
        "TableName": name,
        "Key": ddb_item(pk="k"),
        "UpdateExpression": "SET #s = :s, version = version + :one",
        "ConditionExpression": "version = :expected",
        "ExpressionAttributeNames": {"#s": "state"},
    }
    dynamodb.update_item(**update, ExpressionAttributeValues={":s": {"S": "done"}, ":one": {"N": "1"}, ":expected": {"N": "1"}})
    with raises("ConditionalCheckFailedException"):
        dynamodb.update_item(**update, ExpressionAttributeValues={":s": {"S": "again"}, ":one": {"N": "1"}, ":expected": {"N": "1"}})
    assert plain(dynamodb.get_item(TableName=name, Key=ddb_item(pk="k"))["Item"]) == {"pk": "k", "version": Decimal(2), "state": "done"}


def test_malformed_condition_expression(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    with raises("ValidationException") as exc:
        dynamodb.put_item(TableName=name, Item=ddb_item(pk="k"), ConditionExpression="attribute_not_exists(")
    assert "Invalid ConditionExpression" in exc.value.response["Error"]["Message"]
    with raises("ValidationException") as exc:
        dynamodb.put_item(TableName=name, Item=ddb_item(pk="k"), ConditionExpression="a = :missing")
    assert "ExpressionAttributeValues" in exc.value.response["Error"]["Message"] or ":missing" in exc.value.response["Error"]["Message"]


def test_transact_write_is_all_or_nothing(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="existing"))
    with pytest.raises(ClientError) as exc:
        dynamodb.transact_write_items(
            TransactItems=[
                {"Put": {"TableName": name, "Item": ddb_item(pk="fresh")}},
                {"Put": {"TableName": name, "Item": ddb_item(pk="existing", v=2), "ConditionExpression": "attribute_not_exists(pk)"}},
            ]
        )
    assert exc.value.response["Error"]["Code"] == "TransactionCanceledException"
    reasons = [r["Code"] for r in exc.value.response["CancellationReasons"]]
    assert reasons == ["None", "ConditionalCheckFailed"]
    assert "Item" not in dynamodb.get_item(TableName=name, Key=ddb_item(pk="fresh"))


def test_transact_write_and_get_succeed_together(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    dynamodb.transact_write_items(
        TransactItems=[
            {"Put": {"TableName": name, "Item": ddb_item(pk="a", v=1)}},
            {"Put": {"TableName": name, "Item": ddb_item(pk="b", v=2)}},
            {"Update": {"TableName": name, "Key": ddb_item(pk="c"), "UpdateExpression": "SET v = :v", "ExpressionAttributeValues": {":v": {"N": "3"}}}},
        ]
    )
    got = dynamodb.transact_get_items(
        TransactItems=[{"Get": {"TableName": name, "Key": ddb_item(pk=k)}} for k in ("a", "b", "c", "zz")]
    )["Responses"]
    assert [plain(r["Item"])["v"] if "Item" in r else None for r in got] == [Decimal(1), Decimal(2), Decimal(3), None]


def test_batch_write_and_batch_get(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    out = dynamodb.batch_write_item(RequestItems={name: [{"PutRequest": {"Item": ddb_item(pk=f"k{i}", v=i)}} for i in range(25)]})
    assert out["UnprocessedItems"] == {}
    got = dynamodb.batch_get_item(RequestItems={name: {"Keys": [ddb_item(pk=f"k{i}") for i in range(10)]}})
    assert out["UnprocessedItems"] == {} and got["UnprocessedKeys"] == {}
    assert sorted(plain(i)["pk"] for i in got["Responses"][name]) == sorted(f"k{i}" for i in range(10))

    dynamodb.batch_write_item(RequestItems={name: [{"DeleteRequest": {"Key": ddb_item(pk=f"k{i}")}} for i in range(25)]})
    assert dynamodb.scan(TableName=name, Select="COUNT")["Count"] == 0


def test_batch_write_over_25_requests_is_rejected(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    with raises("ValidationException") as exc:
        dynamodb.batch_write_item(RequestItems={name: [{"PutRequest": {"Item": ddb_item(pk=f"k{i}")}} for i in range(26)]})
    assert exc.value.response["Error"]["Message"].endswith("failed to satisfy constraint: Member must have length less than or equal to 25")


def test_query_by_key_condition_orders_by_sort_key(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table(range_key="sk")
    for pk in ("a", "b"):
        for i in range(5):
            dynamodb.put_item(TableName=name, Item=ddb_item(pk=pk, sk=f"{i:02d}", v=i))

    out = dynamodb.query(TableName=name, KeyConditionExpression="pk = :p AND sk BETWEEN :lo AND :hi",
                         ExpressionAttributeValues={":p": {"S": "a"}, ":lo": {"S": "01"}, ":hi": {"S": "03"}})
    assert [plain(i)["sk"] for i in out["Items"]] == ["01", "02", "03"]
    assert out["Count"] == 3 and out["ScannedCount"] == 3

    descending = dynamodb.query(TableName=name, KeyConditionExpression="pk = :p", ScanIndexForward=False,
                                ExpressionAttributeValues={":p": {"S": "b"}})
    assert [plain(i)["sk"] for i in descending["Items"]] == ["04", "03", "02", "01", "00"]

    begins = dynamodb.query(TableName=name, KeyConditionExpression="pk = :p AND begins_with(sk, :b)",
                            ExpressionAttributeValues={":p": {"S": "a"}, ":b": {"S": "0"}})
    assert begins["Count"] == 5


def test_query_pagination_with_limit(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table(range_key="sk")
    for i in range(7):
        dynamodb.put_item(TableName=name, Item=ddb_item(pk="p", sk=f"{i}"))
    pages = list(
        dynamodb.get_paginator("query").paginate(
            TableName=name,
            KeyConditionExpression="pk = :p",
            ExpressionAttributeValues={":p": {"S": "p"}},
            PaginationConfig={"PageSize": 3},
        )
    )
    assert [len(p["Items"]) for p in pages] == [3, 3, 1]
    first = dynamodb.query(TableName=name, KeyConditionExpression="pk = :p", Limit=3, ExpressionAttributeValues={":p": {"S": "p"}})
    assert first["LastEvaluatedKey"] == ddb_item(pk="p", sk="2")


def test_query_filter_expression_counts_scanned_items(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table(range_key="sk")
    for i in range(6):
        dynamodb.put_item(TableName=name, Item=ddb_item(pk="p", sk=f"{i}", even=i % 2 == 0))
    out = dynamodb.query(TableName=name, KeyConditionExpression="pk = :p", FilterExpression="even = :t",
                         ExpressionAttributeValues={":p": {"S": "p"}, ":t": {"BOOL": True}})
    assert out["Count"] == 3 and out["ScannedCount"] == 6


def test_global_secondary_index_query(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table(
        GlobalSecondaryIndexes=[
            {"IndexName": "by-status", "KeySchema": [{"AttributeName": "status", "KeyType": "HASH"}], "Projection": {"ProjectionType": "ALL"}}
        ]
    )
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="1", status="open"))
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="2", status="closed"))
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="3", status="open"))
    out = dynamodb.query(TableName=name, IndexName="by-status", KeyConditionExpression="#s = :s",
                         ExpressionAttributeNames={"#s": "status"}, ExpressionAttributeValues={":s": {"S": "open"}})
    assert sorted(plain(i)["pk"] for i in out["Items"]) == ["1", "3"]


def test_scan_paginator_with_a_filter(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    for i in range(12):
        dynamodb.put_item(TableName=name, Item=ddb_item(pk=f"k{i:02d}", group=i % 3))
    items = [
        plain(i)["pk"]
        for page in dynamodb.get_paginator("scan").paginate(
            TableName=name,
            FilterExpression="#g = :g",
            ExpressionAttributeNames={"#g": "group"},
            ExpressionAttributeValues={":g": {"N": "1"}},
            PaginationConfig={"PageSize": 5},
        )
        for i in page["Items"]
    ]
    assert sorted(items) == ["k01", "k04", "k07", "k10"]


def enable_ttl(dynamodb: Any, table: str, attribute: str = "ExpiresAt") -> None:
    out = dynamodb.update_time_to_live(TableName=table, TimeToLiveSpecification={"Enabled": True, "AttributeName": attribute})
    assert out["TimeToLiveSpecification"] == {"Enabled": True, "AttributeName": attribute}
    eventually(
        lambda: dynamodb.describe_time_to_live(TableName=table)["TimeToLiveDescription"],
        lambda d: d["TimeToLiveStatus"] == "ENABLED",
        what="TTL to reach ENABLED",
    )


def count_items(dynamodb: Any, table: str) -> int:
    return sum(page["Count"] for page in dynamodb.get_paginator("scan").paginate(TableName=table, Select="COUNT"))


def present(dynamodb: Any, table: str, pk: str) -> bool:
    return "Item" in dynamodb.get_item(TableName=table, Key=ddb_item(pk=pk), ConsistentRead=True)


def test_ttl_description_before_and_after_enabling(dynamodb: Any, make_table: Callable[..., str]) -> None:
    name = make_table()
    before = dynamodb.describe_time_to_live(TableName=name)["TimeToLiveDescription"]
    assert before["TimeToLiveStatus"] == "DISABLED"
    assert "AttributeName" not in before
    enable_ttl(dynamodb, name, "Expires")
    after = dynamodb.describe_time_to_live(TableName=name)["TimeToLiveDescription"]
    assert (after["TimeToLiveStatus"], after["AttributeName"]) == ("ENABLED", "Expires")


def test_expired_items_are_deleted_when_the_emulator_clock_passes_their_ttl(
    dynamodb: Any, make_table: Callable[..., str], clock: Admin
) -> None:
    name = make_table()
    enable_ttl(dynamodb, name)
    now = int(time.time())
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="soon", ExpiresAt=now + 3600))
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="later", ExpiresAt=now + 10 * DAY))
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="never"))
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="not-a-number", ExpiresAt=str(now + 3600)))

    clock.advance(3000)
    assert all(present(dynamodb, name, pk) for pk in ("soon", "later", "never", "not-a-number"))

    clock.advance(1200)
    eventually(lambda: present(dynamodb, name, "soon"), lambda p: not p, what="the expired item to be deleted")
    assert present(dynamodb, name, "later") and present(dynamodb, name, "never")
    assert present(dynamodb, name, "not-a-number"), "a TTL attribute that is not a Number is ignored"
    remaining = {plain(i)["pk"] for i in dynamodb.scan(TableName=name)["Items"]}
    assert remaining == {"later", "never", "not-a-number"}

    clock.advance(11 * DAY)
    eventually(lambda: present(dynamodb, name, "later"), lambda p: not p, what="the second item to be deleted")
    assert present(dynamodb, name, "never") and present(dynamodb, name, "not-a-number")


def test_items_written_already_expired_are_swept(dynamodb: Any, make_table: Callable[..., str], clock: Admin) -> None:
    name = make_table()
    enable_ttl(dynamodb, name)
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="stale", ExpiresAt=int(time.time()) - 100))
    clock.advance(1)
    eventually(lambda: present(dynamodb, name, "stale"), lambda p: not p, what="the stale item to be swept")


def test_refreshing_the_ttl_keeps_an_item_alive(dynamodb: Any, make_table: Callable[..., str], clock: Admin) -> None:
    name = make_table()
    enable_ttl(dynamodb, name)
    now = int(time.time())
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="session", ExpiresAt=now + 600))
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="abandoned", ExpiresAt=now + 600))
    dynamodb.update_item(
        TableName=name,
        Key=ddb_item(pk="session"),
        UpdateExpression="SET ExpiresAt = :t",
        ExpressionAttributeValues={":t": {"N": str(now + 10 * DAY)}},
    )
    clock.advance(1200)
    eventually(lambda: present(dynamodb, name, "abandoned"), lambda p: not p, what="the abandoned item to be deleted")
    assert present(dynamodb, name, "session")


def test_tables_without_ttl_keep_expired_items(dynamodb: Any, make_table: Callable[..., str], clock: Admin) -> None:
    plain_table = make_table("conf-nottl")
    ttl_table = make_table("conf-ttl")
    enable_ttl(dynamodb, ttl_table)
    expires = int(time.time()) + 600
    for t in (plain_table, ttl_table):
        dynamodb.put_item(TableName=t, Item=ddb_item(pk="k", ExpiresAt=expires))
    clock.advance(1200)
    eventually(lambda: present(dynamodb, ttl_table, "k"), lambda p: not p, what="the TTL table item to be deleted")
    assert present(dynamodb, plain_table, "k")


def test_disabling_ttl_stops_deletions(dynamodb: Any, make_table: Callable[..., str], clock: Admin) -> None:
    name = make_table()
    enable_ttl(dynamodb, name)
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="k", ExpiresAt=int(time.time()) + 600))
    dynamodb.update_time_to_live(TableName=name, TimeToLiveSpecification={"Enabled": False, "AttributeName": "ExpiresAt"})
    clock.advance(1200)
    clock.settle()
    assert present(dynamodb, name, "k")


def test_ttl_follows_the_configured_attribute_name(dynamodb: Any, make_table: Callable[..., str], clock: Admin) -> None:
    name = make_table()
    enable_ttl(dynamodb, name, "Deadline")
    expires = int(time.time()) + 600
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="by-deadline", Deadline=expires))
    dynamodb.put_item(TableName=name, Item=ddb_item(pk="by-other", ExpiresAt=expires))
    clock.advance(1200)
    eventually(lambda: present(dynamodb, name, "by-deadline"), lambda p: not p, what="the Deadline item to be deleted")
    assert present(dynamodb, name, "by-other")


def test_ttl_sweep_covers_more_than_one_scan_page(dynamodb: Any, make_table: Callable[..., str], clock: Admin) -> None:
    name = make_table()
    enable_ttl(dynamodb, name)
    expires = int(time.time()) + 600
    padding = "x" * 3000
    for start in range(0, 700, 25):
        dynamodb.batch_write_item(
            RequestItems={
                name: [
                    {"PutRequest": {"Item": ddb_item(pk=f"item-{i:04d}", ExpiresAt=expires, padding=padding)}}
                    for i in range(start, start + 25)
                ]
            }
        )
    clock.advance(1200)
    eventually(lambda: count_items(dynamodb, name), lambda c: c == 0, timeout=60, what="every expired item to be deleted")
