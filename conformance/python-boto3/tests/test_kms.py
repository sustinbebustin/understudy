"""KMS (docs/research/phase1/iam-sts-kms.md section 5): keys, aliases, crypto, rotation and
scheduled deletion on the emulator clock."""

from __future__ import annotations

import datetime as dt
import json
import re
from collections.abc import Callable
from typing import Any

import pytest
from botocore.exceptions import ClientError

from support import Admin, raises, status_of, uniq

KeyFactory = Callable[..., dict[str, Any]]

KEY_ID = r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"


def alias_name() -> str:
    return f"alias/{uniq('conf-kms')}"


# --- Keys --------------------------------------------------------------------------------------------------


def test_create_key_metadata(kms: Any, make_key: KeyFactory) -> None:
    key = make_key("conformance key")
    assert re.fullmatch(KEY_ID, key["KeyId"])
    assert re.fullmatch(rf"arn:aws:kms:us-east-1:\d{{12}}:key/{KEY_ID}", key["Arn"])
    assert key["Arn"].endswith(key["KeyId"])
    assert key["AWSAccountId"] == key["Arn"].split(":")[4]
    assert key["Description"] == "conformance key"
    assert key["Enabled"] is True
    assert key["KeyState"] == "Enabled"
    assert key["KeyUsage"] == "ENCRYPT_DECRYPT"
    assert key["KeySpec"] == "SYMMETRIC_DEFAULT"
    assert key["CustomerMasterKeySpec"] == "SYMMETRIC_DEFAULT"
    assert key["Origin"] == "AWS_KMS"
    assert key["KeyManager"] == "CUSTOMER"
    assert key["MultiRegion"] is False
    assert key["EncryptionAlgorithms"] == ["SYMMETRIC_DEFAULT"]
    assert "DeletionDate" not in key
    assert abs((key["CreationDate"] - dt.datetime.now(dt.UTC)).total_seconds()) < 120


def test_describe_key_accepts_every_identifier_form(kms: Any, make_key: KeyFactory) -> None:
    alias = alias_name()
    key = make_key(alias=alias)
    alias_arn = key["Arn"].replace(f"key/{key['KeyId']}", alias)
    for identifier in (key["KeyId"], key["Arn"], alias, alias_arn):
        assert kms.describe_key(KeyId=identifier)["KeyMetadata"]["KeyId"] == key["KeyId"], identifier


def test_describe_missing_key(kms: Any) -> None:
    missing = "00000000-0000-4000-8000-000000000000"
    with raises("NotFoundException") as exc:
        kms.describe_key(KeyId=missing)
    assert status_of(exc.value) == 400
    assert re.fullmatch(rf"Key 'arn:aws:kms:us-east-1:\d{{12}}:key/{missing}' does not exist", exc.value.response["Error"]["Message"])


def test_list_keys_paginates(kms: Any, make_key: KeyFactory) -> None:
    keys = {make_key()["KeyId"] for _ in range(3)}
    listed: set[str] = set()
    for page in kms.get_paginator("list_keys").paginate(PaginationConfig={"PageSize": 2}):
        assert len(page["Keys"]) <= 2
        listed.update(k["KeyId"] for k in page["Keys"])
    assert keys <= listed


def test_update_key_description(kms: Any, make_key: KeyFactory) -> None:
    key = make_key("before")
    kms.update_key_description(KeyId=key["KeyId"], Description="after")
    assert kms.describe_key(KeyId=key["KeyId"])["KeyMetadata"]["Description"] == "after"


def test_key_tags(kms: Any, make_key: KeyFactory) -> None:
    key = make_key(Tags=[{"TagKey": "env", "TagValue": "test"}])
    assert kms.list_resource_tags(KeyId=key["KeyId"])["Tags"] == [{"TagKey": "env", "TagValue": "test"}]
    kms.tag_resource(KeyId=key["KeyId"], Tags=[{"TagKey": "team", "TagValue": ""}, {"TagKey": "env", "TagValue": "prod"}])
    tags = {t["TagKey"]: t["TagValue"] for t in kms.list_resource_tags(KeyId=key["KeyId"])["Tags"]}
    assert tags == {"env": "prod", "team": ""}
    kms.untag_resource(KeyId=key["KeyId"], TagKeys=["env"])
    assert [t["TagKey"] for t in kms.list_resource_tags(KeyId=key["KeyId"])["Tags"]] == ["team"]


def test_default_key_policy_and_put_key_policy(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    assert kms.list_key_policies(KeyId=key["KeyId"])["PolicyNames"] == ["default"]
    policy = json.loads(kms.get_key_policy(KeyId=key["KeyId"], PolicyName="default")["Policy"])
    [statement] = policy["Statement"]
    assert statement["Sid"] == "Enable IAM User Permissions"
    assert statement["Effect"] == "Allow"
    assert statement["Action"] == "kms:*"
    assert statement["Resource"] == "*"
    assert statement["Principal"]["AWS"] in (
        f"arn:aws:iam::{key['AWSAccountId']}:root",
        [f"arn:aws:iam::{key['AWSAccountId']}:root"],
    )

    replacement = {
        "Version": "2012-10-17",
        "Statement": [
            {
                "Sid": "Root",
                "Effect": "Allow",
                "Principal": {"AWS": f"arn:aws:iam::{key['AWSAccountId']}:root"},
                "Action": "kms:*",
                "Resource": "*",
            },
            {"Sid": "Extra", "Effect": "Allow", "Principal": {"AWS": f"arn:aws:iam::{key['AWSAccountId']}:root"}, "Action": "kms:Decrypt", "Resource": "*"},
        ],
    }
    kms.put_key_policy(KeyId=key["KeyId"], PolicyName="default", Policy=json.dumps(replacement))
    stored = json.loads(kms.get_key_policy(KeyId=key["KeyId"], PolicyName="default")["Policy"])
    assert [s["Sid"] for s in stored["Statement"]] == ["Root", "Extra"]


def test_aws_managed_keys_exist_for_the_services_that_use_kms(kms: Any) -> None:
    for service in ("s3", "secretsmanager", "dynamodb"):
        meta = kms.describe_key(KeyId=f"alias/aws/{service}")["KeyMetadata"]
        assert meta["KeyManager"] == "AWS", service
        assert meta["KeyState"] == "Enabled"
        assert re.fullmatch(KEY_ID, meta["KeyId"])


# --- Aliases -------------------------------------------------------------------------------------------------------


def test_alias_lifecycle(kms: Any, make_key: KeyFactory) -> None:
    first, second = make_key("first"), make_key("second")
    alias = alias_name()
    kms.create_alias(AliasName=alias, TargetKeyId=first["KeyId"])

    entries = [a for page in kms.get_paginator("list_aliases").paginate() for a in page["Aliases"] if a["AliasName"] == alias]
    [entry] = entries
    assert entry["TargetKeyId"] == first["KeyId"]
    assert entry["AliasArn"] == first["Arn"].replace(f"key/{first['KeyId']}", alias)
    assert "CreationDate" in entry

    kms.update_alias(AliasName=alias, TargetKeyId=second["KeyId"])
    assert kms.describe_key(KeyId=alias)["KeyMetadata"]["KeyId"] == second["KeyId"]
    assert [a["AliasName"] for a in kms.list_aliases(KeyId=first["KeyId"])["Aliases"]] == []

    kms.delete_alias(AliasName=alias)
    with raises("NotFoundException"):
        kms.describe_key(KeyId=alias)


def test_alias_must_start_with_the_alias_prefix(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    with raises("ValidationException") as exc:
        kms.create_alias(AliasName=uniq("no-prefix"), TargetKeyId=key["KeyId"])
    assert exc.value.response["Error"]["Message"] == (
        'Alias must start with the prefix "alias/". Please see https://docs.aws.amazon.com/kms/latest/developerguide/kms-alias.html'
    )


def test_duplicate_alias(kms: Any, make_key: KeyFactory) -> None:
    alias = alias_name()
    key = make_key(alias=alias)
    with raises("AlreadyExistsException") as exc:
        kms.create_alias(AliasName=alias, TargetKeyId=key["KeyId"])
    assert exc.value.response["Error"]["Message"] == (
        f"An alias with the name {key['Arn'].replace('key/' + key['KeyId'], alias)} already exists"
    )


# --- Cryptography -----------------------------------------------------------------------------------------------


def test_encrypt_decrypt_round_trip(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    plaintext = b"the quick brown fox \x00\xff"
    enc = kms.encrypt(KeyId=key["KeyId"], Plaintext=plaintext)
    assert enc["KeyId"] == key["Arn"]
    assert enc["EncryptionAlgorithm"] == "SYMMETRIC_DEFAULT"
    assert enc["CiphertextBlob"] != plaintext and plaintext not in enc["CiphertextBlob"]

    dec = kms.decrypt(CiphertextBlob=enc["CiphertextBlob"])
    assert dec["Plaintext"] == plaintext
    assert dec["KeyId"] == key["Arn"]
    assert kms.decrypt(CiphertextBlob=enc["CiphertextBlob"], KeyId=key["Arn"])["Plaintext"] == plaintext


def test_encrypting_twice_gives_different_ciphertext(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    a = kms.encrypt(KeyId=key["KeyId"], Plaintext=b"same")["CiphertextBlob"]
    b = kms.encrypt(KeyId=key["KeyId"], Plaintext=b"same")["CiphertextBlob"]
    assert a != b


def test_encrypt_by_alias_reports_the_key_arn(kms: Any, make_key: KeyFactory) -> None:
    alias = alias_name()
    key = make_key(alias=alias)
    enc = kms.encrypt(KeyId=alias, Plaintext=b"x")
    assert enc["KeyId"] == key["Arn"]
    assert kms.decrypt(CiphertextBlob=enc["CiphertextBlob"])["KeyId"] == key["Arn"]


def test_plaintext_limits(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    big = b"a" * 4096
    assert kms.decrypt(CiphertextBlob=kms.encrypt(KeyId=key["KeyId"], Plaintext=big)["CiphertextBlob"])["Plaintext"] == big
    with raises("ValidationException") as exc:
        kms.encrypt(KeyId=key["KeyId"], Plaintext=b"a" * 4097)
    assert exc.value.response["Error"]["Message"] == (
        "1 validation error detected: Value at 'plaintext' failed to satisfy constraint: "
        "Member must have length less than or equal to 4096"
    )


def test_encryption_context_must_match(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    context = {"purpose": "conformance", "tenant": "a"}
    enc = kms.encrypt(KeyId=key["KeyId"], Plaintext=b"bound", EncryptionContext=context)
    assert kms.decrypt(CiphertextBlob=enc["CiphertextBlob"], EncryptionContext=context)["Plaintext"] == b"bound"
    for wrong in ({}, {"purpose": "conformance"}, {"purpose": "other", "tenant": "a"}, {"purpose": "Conformance", "tenant": "a"}):
        with raises("InvalidCiphertextException"):
            kms.decrypt(CiphertextBlob=enc["CiphertextBlob"], EncryptionContext=wrong)


def test_decrypt_with_the_wrong_key_id(kms: Any, make_key: KeyFactory) -> None:
    key, other = make_key(), make_key()
    enc = kms.encrypt(KeyId=key["KeyId"], Plaintext=b"x")
    with raises("IncorrectKeyException"):
        kms.decrypt(CiphertextBlob=enc["CiphertextBlob"], KeyId=other["KeyId"])


def test_decrypt_garbage(kms: Any) -> None:
    with raises("InvalidCiphertextException") as exc:
        kms.decrypt(CiphertextBlob=b"definitely not a ciphertext")
    assert status_of(exc.value) == 400


def test_decrypt_tampered_ciphertext(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    blob = bytearray(kms.encrypt(KeyId=key["KeyId"], Plaintext=b"integrity")["CiphertextBlob"])
    blob[-1] ^= 0x01
    with raises("InvalidCiphertextException"):
        kms.decrypt(CiphertextBlob=bytes(blob))


@pytest.mark.parametrize(("spec", "length"), [("AES_256", 32), ("AES_128", 16)])
def test_generate_data_key_by_spec(kms: Any, make_key: KeyFactory, spec: str, length: int) -> None:
    key = make_key()
    out = kms.generate_data_key(KeyId=key["KeyId"], KeySpec=spec, EncryptionContext={"a": "b"})
    assert len(out["Plaintext"]) == length
    assert out["KeyId"] == key["Arn"]
    assert kms.decrypt(CiphertextBlob=out["CiphertextBlob"], EncryptionContext={"a": "b"})["Plaintext"] == out["Plaintext"]
    with raises("InvalidCiphertextException"):
        kms.decrypt(CiphertextBlob=out["CiphertextBlob"])


def test_generate_data_key_by_number_of_bytes_is_random(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    a = kms.generate_data_key(KeyId=key["KeyId"], NumberOfBytes=64)
    b = kms.generate_data_key(KeyId=key["KeyId"], NumberOfBytes=64)
    assert len(a["Plaintext"]) == 64
    assert a["Plaintext"] != b["Plaintext"]


def test_generate_data_key_without_plaintext(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    out = kms.generate_data_key_without_plaintext(KeyId=key["KeyId"], KeySpec="AES_256")
    assert "Plaintext" not in out
    assert out["KeyId"] == key["Arn"]
    assert len(kms.decrypt(CiphertextBlob=out["CiphertextBlob"])["Plaintext"]) == 32


def test_re_encrypt_moves_ciphertext_to_another_key(kms: Any, make_key: KeyFactory) -> None:
    source, destination = make_key(), make_key()
    enc = kms.encrypt(KeyId=source["KeyId"], Plaintext=b"move me", EncryptionContext={"c": "1"})
    out = kms.re_encrypt(
        CiphertextBlob=enc["CiphertextBlob"],
        SourceEncryptionContext={"c": "1"},
        DestinationKeyId=destination["KeyId"],
        DestinationEncryptionContext={"c": "2"},
    )
    assert out["SourceKeyId"] == source["Arn"]
    assert out["KeyId"] == destination["Arn"]
    dec = kms.decrypt(CiphertextBlob=out["CiphertextBlob"], EncryptionContext={"c": "2"})
    assert dec["Plaintext"] == b"move me" and dec["KeyId"] == destination["Arn"]


# --- Key states, rotation, deletion --------------------------------------------------------------------------


def test_disabled_key_cannot_be_used(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    enc = kms.encrypt(KeyId=key["KeyId"], Plaintext=b"x")
    kms.disable_key(KeyId=key["KeyId"])
    meta = kms.describe_key(KeyId=key["KeyId"])["KeyMetadata"]
    assert (meta["Enabled"], meta["KeyState"]) == (False, "Disabled")
    for call in (
        lambda: kms.encrypt(KeyId=key["KeyId"], Plaintext=b"y"),
        lambda: kms.decrypt(CiphertextBlob=enc["CiphertextBlob"]),
        lambda: kms.generate_data_key(KeyId=key["KeyId"], KeySpec="AES_256"),
    ):
        with raises("DisabledException") as exc:
            call()
        assert exc.value.response["Error"]["Message"] == f"{key['Arn']} is disabled."

    kms.enable_key(KeyId=key["KeyId"])
    assert kms.decrypt(CiphertextBlob=enc["CiphertextBlob"])["Plaintext"] == b"x"


def test_rotation_status(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    assert kms.get_key_rotation_status(KeyId=key["KeyId"])["KeyRotationEnabled"] is False
    kms.enable_key_rotation(KeyId=key["KeyId"])
    status = kms.get_key_rotation_status(KeyId=key["KeyId"])
    assert status["KeyRotationEnabled"] is True
    assert status["RotationPeriodInDays"] == 365
    kms.enable_key_rotation(KeyId=key["KeyId"], RotationPeriodInDays=90)
    assert kms.get_key_rotation_status(KeyId=key["KeyId"])["RotationPeriodInDays"] == 90
    kms.disable_key_rotation(KeyId=key["KeyId"])
    assert kms.get_key_rotation_status(KeyId=key["KeyId"])["KeyRotationEnabled"] is False


def test_rotation_of_a_disabled_key_is_refused(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    kms.disable_key(KeyId=key["KeyId"])
    with raises("DisabledException") as exc:
        kms.enable_key_rotation(KeyId=key["KeyId"])
    assert exc.value.response["Error"]["Message"] == f"{key['Arn']} is disabled."


def test_schedule_key_deletion(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    enc = kms.encrypt(KeyId=key["KeyId"], Plaintext=b"x")
    out = kms.schedule_key_deletion(KeyId=key["KeyId"], PendingWindowInDays=7)
    assert out["KeyId"] == key["Arn"]
    assert out["KeyState"] == "PendingDeletion"
    assert out["PendingWindowInDays"] == 7
    assert abs((out["DeletionDate"] - dt.datetime.now(dt.UTC)).total_seconds() - 7 * 86400) < 300

    meta = kms.describe_key(KeyId=key["KeyId"])["KeyMetadata"]
    assert (meta["Enabled"], meta["KeyState"]) == (False, "PendingDeletion")
    assert meta["DeletionDate"] == out["DeletionDate"]

    with pytest.raises(ClientError) as exc:
        kms.decrypt(CiphertextBlob=enc["CiphertextBlob"])
    error = exc.value.response["Error"]
    # iam-sts-kms.md 5.6 documents both codes for a crypto call on a pending-deletion key.
    assert error["Code"] in ("KMSInvalidStateException", "DisabledException")
    assert f"{key['Arn']} is pending deletion" in error["Message"]

    with raises("KMSInvalidStateException") as again:
        kms.schedule_key_deletion(KeyId=key["KeyId"], PendingWindowInDays=7)
    assert "is pending deletion" in again.value.response["Error"]["Message"]


def test_cancel_key_deletion_leaves_the_key_disabled(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    kms.schedule_key_deletion(KeyId=key["KeyId"], PendingWindowInDays=7)
    assert kms.cancel_key_deletion(KeyId=key["KeyId"])["KeyId"] == key["Arn"]
    meta = kms.describe_key(KeyId=key["KeyId"])["KeyMetadata"]
    assert (meta["Enabled"], meta["KeyState"]) == (False, "Disabled")
    assert "DeletionDate" not in meta
    kms.enable_key(KeyId=key["KeyId"])
    kms.encrypt(KeyId=key["KeyId"], Plaintext=b"usable again")

    with raises("KMSInvalidStateException") as exc:
        kms.cancel_key_deletion(KeyId=key["KeyId"])
    assert exc.value.response["Error"]["Message"] == f"{key['Arn']} is not pending deletion (or pending replica deletion)."


def test_pending_window_must_be_between_7_and_30_days(kms: Any, make_key: KeyFactory) -> None:
    key = make_key()
    for days in (6, 31):
        with raises("ValidationException"):
            kms.schedule_key_deletion(KeyId=key["KeyId"], PendingWindowInDays=days)


def test_key_and_its_aliases_disappear_when_the_deletion_window_passes(
    kms: Any, make_key: KeyFactory, clock: Admin
) -> None:
    alias = alias_name()
    key = make_key(alias=alias)
    kms.schedule_key_deletion(KeyId=key["KeyId"], PendingWindowInDays=7)

    clock.advance(7 * 86400 - 60)
    assert kms.describe_key(KeyId=key["KeyId"])["KeyMetadata"]["KeyState"] == "PendingDeletion"

    clock.advance(120)
    with raises("NotFoundException"):
        kms.describe_key(KeyId=key["KeyId"])
    with raises("NotFoundException"):
        kms.describe_key(KeyId=alias)
    assert alias not in [a["AliasName"] for page in kms.get_paginator("list_aliases").paginate() for a in page["Aliases"]]
