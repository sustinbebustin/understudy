"""S3 Object Lock and SSE-C (docs/research/s3-ssec-object-lock.md).

Recorded texts cite LocalStack's AWS-validated snapshots (localstack@8b9a79f,
validated 2026-02-21) by file and line, or the repo's own recordings under
testdata/parity/s3; behavior AWS has not been recorded doing follows the
fidelity switch understudy declares, read from its coverage report.
"""

from __future__ import annotations

import datetime
import json
from collections.abc import Callable, Iterator
from typing import Any

import pytest
from botocore.exceptions import ClientError

from support import ADMIN_PREFIX, REGION, Admin, http_get, raises, signed_http, status_of, uniq, url_host_port

# The refusal of a delete or change Object Lock forbids (test_s3.snapshot.json L9478, L9639, L10044).
LOCKED = "Access Denied because object protected by object lock."


def switch_value(endpoint: str, name: str) -> str:
    """The value the running understudy declares for the fidelity switch `name`."""
    status, _, body = http_get(endpoint.rstrip("/") + ADMIN_PREFIX + "/coverage")
    assert status == 200, body
    for s in json.loads(body)["switches"]:
        if s["name"] == name:
            return s["value"]
    raise AssertionError(f"understudy declares no switch {name} (s3-ssec-object-lock.md 11.4)")


def message(err: ClientError) -> str:
    return err.response["Error"]["Message"]


def _empty_lock_bucket(s3: Any, bucket: str) -> None:
    """Lift every hold, delete every version with the GOVERNANCE bypass, then the bucket.

    The tests here use GOVERNANCE only, so this always removes what they wrote.
    """
    paginator = s3.get_paginator("list_object_versions")
    for page in paginator.paginate(Bucket=bucket):
        for v in page.get("Versions", []):
            try:
                s3.put_object_legal_hold(Bucket=bucket, Key=v["Key"], VersionId=v["VersionId"], LegalHold={"Status": "OFF"})
            except ClientError:
                pass
            s3.delete_object(Bucket=bucket, Key=v["Key"], VersionId=v["VersionId"], BypassGovernanceRetention=True)
        for m in page.get("DeleteMarkers", []):
            s3.delete_object(Bucket=bucket, Key=m["Key"], VersionId=m["VersionId"])
    s3.delete_bucket(Bucket=bucket)


@pytest.fixture
def lock_bucket(s3: Any) -> Iterator[str]:
    name = uniq("lock")
    s3.create_bucket(Bucket=name, ObjectLockEnabledForBucket=True)
    try:
        yield name
    finally:
        try:
            _empty_lock_bucket(s3, name)
        except ClientError:
            pass


def _emulator_now(clock: Admin) -> datetime.datetime:
    """Emulator time from GET /clock, to the second."""
    whole = clock.clock()["now"].split(".")[0].removesuffix("Z")
    return datetime.datetime.fromisoformat(whole).replace(tzinfo=datetime.UTC)


def test_governance_retention_refuses_a_versioned_delete_without_bypass(s3: Any, lock_bucket: str) -> None:
    until = (datetime.datetime.now(datetime.UTC) + datetime.timedelta(hours=1)).replace(microsecond=0)
    put = s3.put_object(Bucket=lock_bucket, Key="gov", Body=b"v", ObjectLockMode="GOVERNANCE", ObjectLockRetainUntilDate=until)
    head = s3.head_object(Bucket=lock_bucket, Key="gov", VersionId=put["VersionId"])
    # test_s3.snapshot.json L9623: HEAD reports the mode and the date.
    assert head["ObjectLockMode"] == "GOVERNANCE"
    assert head["ObjectLockRetainUntilDate"] == until
    # L9639, then L9649 with the bypass header.
    with raises("AccessDenied") as info:
        s3.delete_object(Bucket=lock_bucket, Key="gov", VersionId=put["VersionId"])
    assert status_of(info.value) == 403
    assert message(info.value) == LOCKED
    s3.delete_object(Bucket=lock_bucket, Key="gov", VersionId=put["VersionId"], BypassGovernanceRetention=True)


def test_retention_runs_on_the_emulator_clock(s3: Any, lock_bucket: str, frozen_clock: Admin) -> None:
    """A version is protected until the emulator clock reaches its date (L17904), then deletable without bypass."""
    until = _emulator_now(frozen_clock) + datetime.timedelta(seconds=60)
    put = s3.put_object(Bucket=lock_bucket, Key="k", Body=b"v", ObjectLockMode="GOVERNANCE", ObjectLockRetainUntilDate=until)
    frozen_clock.advance(59)
    with raises("AccessDenied"):
        s3.delete_object(Bucket=lock_bucket, Key="k", VersionId=put["VersionId"])
    frozen_clock.advance(2)
    s3.delete_object(Bucket=lock_bucket, Key="k", VersionId=put["VersionId"])


def test_legal_hold_on_and_off(s3: Any, lock_bucket: str, endpoint: str) -> None:
    put = s3.put_object(Bucket=lock_bucket, Key="held", Body=b"v")
    version = put["VersionId"]
    # L9919: never set.
    with raises("NoSuchObjectLockConfiguration"):
        s3.get_object_legal_hold(Bucket=lock_bucket, Key="held", VersionId=version)
    s3.put_object_legal_hold(Bucket=lock_bucket, Key="held", VersionId=version, LegalHold={"Status": "ON"})
    # L9950, L9935.
    assert s3.get_object_legal_hold(Bucket=lock_bucket, Key="held", VersionId=version)["LegalHold"]["Status"] == "ON"
    assert s3.head_object(Bucket=lock_bucket, Key="held", VersionId=version)["ObjectLockLegalHoldStatus"] == "ON"
    # L10044: bypass never lifts a hold.
    with raises("AccessDenied") as info:
        s3.delete_object(Bucket=lock_bucket, Key="held", VersionId=version, BypassGovernanceRetention=True)
    assert message(info.value) == LOCKED
    s3.put_object_legal_hold(Bucket=lock_bucket, Key="held", VersionId=version, LegalHold={"Status": "OFF"})
    assert switch_value(endpoint, "s3.object-lock.cleared-reads") == "404-and-OFF"
    assert s3.get_object_legal_hold(Bucket=lock_bucket, Key="held", VersionId=version)["LegalHold"]["Status"] == "OFF"
    s3.delete_object(Bucket=lock_bucket, Key="held", VersionId=version)


def test_delete_objects_reports_a_locked_version_per_key(s3: Any, lock_bucket: str) -> None:
    put = s3.put_object(Bucket=lock_bucket, Key="test-delete-locked", Body=b"test")
    s3.put_object_legal_hold(Bucket=lock_bucket, Key="test-delete-locked", VersionId=put["VersionId"], LegalHold={"Status": "ON"})
    out = s3.delete_objects(Bucket=lock_bucket, Delete={"Objects": [{"Key": "test-delete-locked", "VersionId": put["VersionId"]}]})
    # test_s3.snapshot.json L10054.
    assert out["ResponseMetadata"]["HTTPStatusCode"] == 200
    assert out["Errors"] == [
        {"Key": "test-delete-locked", "VersionId": put["VersionId"], "Code": "AccessDenied", "Message": LOCKED}
    ]


def test_default_retention_put_without_a_checksum_is_refused(s3: Any, lock_bucket: str, endpoint: str) -> None:
    s3.put_object_lock_configuration(
        Bucket=lock_bucket,
        ObjectLockConfiguration={"ObjectLockEnabled": "Enabled", "Rule": {"DefaultRetention": {"Mode": "GOVERNANCE", "Days": 1}}},
    )
    # test_s3_api.snapshot.json L2486: the rule is echoed.
    rule = s3.get_object_lock_configuration(Bucket=lock_bucket)["ObjectLockConfiguration"]["Rule"]
    assert rule == {"DefaultRetention": {"Mode": "GOVERNANCE", "Days": 1}}
    # testdata/parity/s3/R12/11-put-without-checksum.json: no Content-MD5 or x-amz-checksum-*.
    # A signed payload hash does not count, and any lock parameter or a locked upload's
    # part needs one too (switch s3.object-lock.checksum-scope; testdata/parity/s3/S3SOL/14-*.json,
    # 15-part-no-checksum.json).
    assert switch_value(endpoint, "s3.object-lock.checksum-scope") == "any-lock-parameter-and-parts"
    status, _, body = signed_http(
        "PUT",
        f"{endpoint.rstrip('/')}/{lock_bucket}/o",
        connect_to=url_host_port(endpoint),
        region=REGION,
        body=b"0123456789",
    )
    assert status == 400, body
    assert b"<Code>InvalidRequest</Code>" in body
    assert (
        b"<Message>Content-MD5 OR x-amz-checksum- HTTP header is required for Put Object requests with Object Lock parameters</Message>"
        in body
    )
    # boto3 sends a checksum on every PutObject, so the default applies.
    put = s3.put_object(Bucket=lock_bucket, Key="k", Body=b"v")
    head = s3.head_object(Bucket=lock_bucket, Key="k", VersionId=put["VersionId"])
    assert head["ObjectLockMode"] == "GOVERNANCE"
    # Within 2 minutes of Last-Modified plus a day, as LocalStack asserts (test_s3.py L9945-L9953; L9711).
    assert abs(head["ObjectLockRetainUntilDate"] - (head["LastModified"] + datetime.timedelta(days=1))) < datetime.timedelta(minutes=2)


def test_bypass_header_on_a_bucket_without_object_lock(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket("nolock")
    s3.put_object(Bucket=bucket, Key="k", Body=b"v")
    # test_s3_api.snapshot.json L3535.
    with raises("InvalidArgument") as info:
        s3.delete_object(Bucket=bucket, Key="k", BypassGovernanceRetention=True)
    assert status_of(info.value) == 400
    assert message(info.value) == "x-amz-bypass-governance-retention is only applicable to Object Lock enabled buckets."
    assert info.value.response["Error"]["ArgumentName"] == "x-amz-bypass-governance-retention"


def test_sse_c_over_plain_http_is_refused(s3: Any, make_bucket: Callable[..., str], endpoint: str) -> None:
    """The suite has no TLS endpoint; SSE-C over plain HTTP follows switch s3.sse-c.plain-http."""
    bucket = make_bucket("ssec")
    s3.put_bucket_encryption(
        Bucket=bucket,
        ServerSideEncryptionConfiguration={"Rules": [{"BlockedEncryptionTypes": {"EncryptionType": ["NONE"]}}]},
    )
    assert switch_value(endpoint, "s3.sse-c.plain-http") == "refused"
    with raises("InvalidRequest") as info:
        # botocore base64-encodes the 32-byte key and adds its MD5 (s3-ssec-object-lock.md 3.12).
        s3.put_object(Bucket=bucket, Key="k", Body=b"v", SSECustomerAlgorithm="AES256", SSECustomerKey="1234567890abcdef1234567890abcdef")
    assert status_of(info.value) == 400
    assert message(info.value) == "Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection."
