"""Secrets Manager (docs/research/phase1/secrets-logs-events-alarms.md section 2)."""

from __future__ import annotations

import datetime as dt
import json
import re
import uuid
from collections.abc import Callable
from typing import Any

import pytest
from botocore.exceptions import ClientError

from support import Admin, raises, status_of, uniq

DAY = 86400


@pytest.fixture
def make_secret(secretsmanager: Any, cleanup: Any) -> Callable[..., dict[str, Any]]:
    def make(prefix: str = "conf/secret", **args: Any) -> dict[str, Any]:
        name = uniq(prefix)
        out = secretsmanager.create_secret(Name=name, **args)
        cleanup.add(secretsmanager.delete_secret, SecretId=name, ForceDeleteWithoutRecovery=True)
        return out

    return make


def stages(secretsmanager: Any, secret_id: str) -> dict[str, list[str]]:
    return secretsmanager.describe_secret(SecretId=secret_id).get("VersionIdsToStages", {})


# --- Create, read, versions ----------------------------------------------------------------------------------


def test_create_secret_returns_arn_name_and_version(make_secret: Callable[..., dict[str, Any]]) -> None:
    out = make_secret(SecretString="v1")
    assert re.fullmatch(
        rf"arn:aws:secretsmanager:us-east-1:\d{{12}}:secret:{re.escape(out['Name'])}-[A-Za-z0-9]{{6}}", out["ARN"]
    )
    assert re.fullmatch(r"[0-9a-f-]{36}", out["VersionId"])
    assert "VersionStages" not in out


def test_get_secret_value_by_name_and_arn(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString='{"user":"u","pass":"p"}')
    for secret_id in (created["Name"], created["ARN"]):
        got = secretsmanager.get_secret_value(SecretId=secret_id)
        assert got["SecretString"] == '{"user":"u","pass":"p"}'
        assert got["Name"] == created["Name"]
        assert got["ARN"] == created["ARN"]
        assert got["VersionId"] == created["VersionId"]
        assert got["VersionStages"] == ["AWSCURRENT"]
        assert "SecretBinary" not in got
        assert abs((got["CreatedDate"] - dt.datetime.now(dt.UTC)).total_seconds()) < 120


def test_binary_secrets_round_trip(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretBinary=b"\x00\x01binary\xff")
    got = secretsmanager.get_secret_value(SecretId=created["Name"])
    assert got["SecretBinary"] == b"\x00\x01binary\xff"
    assert "SecretString" not in got


def test_put_secret_value_moves_current_and_previous(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    v1 = created["VersionId"]
    put = secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="v2")
    v2 = put["VersionId"]
    assert put["VersionStages"] == ["AWSCURRENT"]
    assert put["ARN"] == created["ARN"] and put["Name"] == created["Name"]

    assert secretsmanager.get_secret_value(SecretId=created["Name"])["SecretString"] == "v2"
    previous = secretsmanager.get_secret_value(SecretId=created["Name"], VersionStage="AWSPREVIOUS")
    assert (previous["SecretString"], previous["VersionId"]) == ("v1", v1)
    assert secretsmanager.get_secret_value(SecretId=created["Name"], VersionId=v1)["SecretString"] == "v1"
    assert stages(secretsmanager, created["Name"]) == {v1: ["AWSPREVIOUS"], v2: ["AWSCURRENT"]}

    v3 = secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="v3")["VersionId"]
    # The version that held AWSPREVIOUS has no label left and is deprecated.
    assert stages(secretsmanager, created["Name"]) == {v2: ["AWSPREVIOUS"], v3: ["AWSCURRENT"]}


def test_get_by_version_id_and_stage_must_agree(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="v2")
    with raises("InvalidRequestException") as exc:
        secretsmanager.get_secret_value(SecretId=created["Name"], VersionId=created["VersionId"], VersionStage="AWSCURRENT")
    assert exc.value.response["Error"]["Message"] == "You provided a VersionStage that is not associated to the provided VersionId."


def test_secret_created_without_a_value_has_no_version(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret()
    assert "VersionId" not in created
    assert stages(secretsmanager, created["Name"]) == {}
    with raises("ResourceNotFoundException") as exc:
        secretsmanager.get_secret_value(SecretId=created["Name"])
    assert exc.value.response["Error"]["Message"] == "Secrets Manager can't find the specified secret value for staging label: AWSCURRENT"
    first = secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="first")
    assert first["VersionStages"] == ["AWSCURRENT"]


def test_custom_stages_leave_current_alone_and_are_sorted(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    put = secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="v2", VersionStages=["two", "one", "three"])
    assert put["VersionStages"] == ["one", "three", "two"]
    assert secretsmanager.get_secret_value(SecretId=created["Name"])["SecretString"] == "v1"

    # A later write that reuses a label steals it; the donor version keeps its other labels.
    second = secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="v3", VersionStages=["one"])
    assert stages(secretsmanager, created["Name"]) == {
        created["VersionId"]: ["AWSCURRENT"],
        put["VersionId"]: ["three", "two"],
        second["VersionId"]: ["one"],
    }


def test_rotation_style_stage_moves(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    v1 = created["VersionId"]
    v2 = secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="v2", VersionStages=["AWSPENDING"])["VersionId"]
    assert stages(secretsmanager, created["Name"]) == {v1: ["AWSCURRENT"], v2: ["AWSPENDING"]}

    out = secretsmanager.update_secret_version_stage(
        SecretId=created["Name"], VersionStage="AWSCURRENT", MoveToVersionId=v2, RemoveFromVersionId=v1
    )
    assert set(out) - {"ResponseMetadata"} == {"ARN", "Name"}
    assert stages(secretsmanager, created["Name"]) == {v1: ["AWSPREVIOUS"], v2: ["AWSCURRENT", "AWSPENDING"]}
    assert secretsmanager.get_secret_value(SecretId=created["Name"])["SecretString"] == "v2"

    secretsmanager.update_secret_version_stage(SecretId=created["Name"], VersionStage="AWSPENDING", RemoveFromVersionId=v2)
    assert stages(secretsmanager, created["Name"])[v2] == ["AWSCURRENT"]


def test_awscurrent_cannot_be_removed_outright(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    with raises("InvalidParameterException") as exc:
        secretsmanager.update_secret_version_stage(
            SecretId=created["Name"], VersionStage="AWSCURRENT", RemoveFromVersionId=created["VersionId"]
        )
    assert exc.value.response["Error"]["Message"] == (
        "You can only move staging label AWSCURRENT to a different secret version. It can't be completely removed."
    )


def test_list_secret_version_ids_hides_deprecated_versions(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    ids = [created["VersionId"]]
    for value in ("v2", "v3"):
        ids.append(secretsmanager.put_secret_value(SecretId=created["Name"], SecretString=value)["VersionId"])

    default = secretsmanager.list_secret_version_ids(SecretId=created["Name"])
    assert [v["VersionId"] for v in default["Versions"]] == ids[1:]
    everything = secretsmanager.list_secret_version_ids(SecretId=created["Name"], IncludeDeprecated=True)
    assert [v["VersionId"] for v in everything["Versions"]] == ids
    assert "VersionStages" not in everything["Versions"][0]
    assert default["ARN"] == created["ARN"] and default["Name"] == created["Name"]


def test_client_request_token_makes_writes_idempotent(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    token = str(uuid.uuid4())
    first = secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="v2", ClientRequestToken=token)
    assert first["VersionId"] == token
    again = secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="v2", ClientRequestToken=token)
    assert again["VersionId"] == token
    assert len(secretsmanager.list_secret_version_ids(SecretId=created["Name"], IncludeDeprecated=True)["Versions"]) == 2

    with pytest.raises(ClientError) as exc:
        secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="different", ClientRequestToken=token)
    assert status_of(exc.value) == 400
    assert secretsmanager.get_secret_value(SecretId=created["Name"])["SecretString"] == "v2"


# --- Errors ----------------------------------------------------------------------------------------------------------


def test_get_missing_secret(secretsmanager: Any) -> None:
    with raises("ResourceNotFoundException") as exc:
        secretsmanager.get_secret_value(SecretId=uniq("conf/missing"))
    assert status_of(exc.value) == 400
    assert exc.value.response["Error"]["Message"] == "Secrets Manager can't find the specified secret."


def test_get_unknown_version_and_stage(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    unknown = str(uuid.uuid4())
    with raises("ResourceNotFoundException") as exc:
        secretsmanager.get_secret_value(SecretId=created["Name"], VersionId=unknown)
    assert exc.value.response["Error"]["Message"] == f"Secrets Manager can't find the specified secret value for VersionId: {unknown}"
    with raises("ResourceNotFoundException") as exc:
        secretsmanager.get_secret_value(SecretId=created["Name"], VersionStage="AWSPREVIOUS")
    assert exc.value.response["Error"]["Message"] == "Secrets Manager can't find the specified secret value for staging label: AWSPREVIOUS"


def test_create_secret_that_exists(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    with raises("ResourceExistsException") as exc:
        secretsmanager.create_secret(Name=created["Name"], SecretString="other")
    assert status_of(exc.value) == 400
    assert exc.value.response["Error"]["Message"] == f"The operation failed because the secret {created['Name']} already exists."


@pytest.mark.parametrize("name", ["has space", "star*", "question?", "bracket]"])
def test_invalid_secret_names(secretsmanager: Any, name: str) -> None:
    with raises("ValidationException") as exc:
        secretsmanager.create_secret(Name=name, SecretString="x")
    assert exc.value.response["Error"]["Message"] == (
        "Invalid name. Must be a valid name containing alphanumeric characters, or any of the following: -/_+=.@!"
    )


def test_secret_name_characters_round_trip(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret("conf/_+=.@-Name", SecretString="x")
    assert secretsmanager.get_secret_value(SecretId=created["Name"])["Name"] == created["Name"]


# --- Describe, update, list ------------------------------------------------------------------------------------------


def test_describe_secret_fields(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    described = secretsmanager.describe_secret(SecretId=created["Name"])
    assert set(described) - {"ResponseMetadata"} == {"ARN", "Name", "CreatedDate", "LastChangedDate", "VersionIdsToStages"}
    assert described["VersionIdsToStages"] == {created["VersionId"]: ["AWSCURRENT"]}

    secretsmanager.get_secret_value(SecretId=created["Name"])
    after = secretsmanager.describe_secret(SecretId=created["Name"])
    assert "LastAccessedDate" in after


def test_update_secret_description_creates_no_version(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    out = secretsmanager.update_secret(SecretId=created["Name"], Description="docs")
    assert set(out) - {"ResponseMetadata"} == {"ARN", "Name"}
    described = secretsmanager.describe_secret(SecretId=created["Name"])
    assert described["Description"] == "docs"
    assert list(described["VersionIdsToStages"]) == [created["VersionId"]]


def test_update_secret_value_creates_a_current_version(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="v1")
    out = secretsmanager.update_secret(SecretId=created["Name"], SecretString="v2")
    assert out["VersionId"] != created["VersionId"]
    assert secretsmanager.get_secret_value(SecretId=created["Name"])["VersionId"] == out["VersionId"]
    assert stages(secretsmanager, created["Name"])[created["VersionId"]] == ["AWSPREVIOUS"]


def test_list_secrets_filters_sorts_and_paginates(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    prefix = uniq("conf/list")
    names = [make_secret(f"{prefix}/{i}", SecretString="x", Description=f"d{i}")["Name"] for i in range(5)]
    make_secret("conf/other", SecretString="x")

    pages = list(
        secretsmanager.get_paginator("list_secrets").paginate(
            Filters=[{"Key": "name", "Values": [prefix]}],
            SortOrder="asc",
            PaginationConfig={"PageSize": 2},
        )
    )
    assert [len(p["SecretList"]) for p in pages] == [2, 2, 1]
    listed = [s["Name"] for p in pages for s in p["SecretList"]]
    assert sorted(listed) == sorted(names)
    entry = pages[0]["SecretList"][0]
    assert {"ARN", "Name", "CreatedDate", "LastChangedDate", "SecretVersionsToStages"} <= set(entry)
    assert "VersionIdsToStages" not in entry

    by_description = secretsmanager.list_secrets(Filters=[{"Key": "description", "Values": ["D3"]}])
    assert [s["Name"] for s in by_description["SecretList"] if s["Name"].startswith(prefix)] == [names[3]]


def test_list_secrets_by_tag(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    tag_key = uniq("conf-tagkey")
    created = make_secret(SecretString="x", Tags=[{"Key": tag_key, "Value": "yes"}])
    make_secret(SecretString="x")
    out = secretsmanager.list_secrets(Filters=[{"Key": "tag-key", "Values": [tag_key]}])
    assert [s["Name"] for s in out["SecretList"]] == [created["Name"]]
    assert out["SecretList"][0]["Tags"] == [{"Key": tag_key, "Value": "yes"}]


# --- Tags and resource policy -----------------------------------------------------------------------------------------


def test_secret_tags(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="x", Tags=[{"Key": "env", "Value": "test"}])
    secretsmanager.tag_resource(SecretId=created["Name"], Tags=[{"Key": "Env", "Value": "case"}, {"Key": "team", "Value": "a"}])
    tags = {t["Key"]: t["Value"] for t in secretsmanager.describe_secret(SecretId=created["Name"])["Tags"]}
    assert tags == {"env": "test", "Env": "case", "team": "a"}, "tag keys are case sensitive"

    secretsmanager.untag_resource(SecretId=created["Name"], TagKeys=["env", "Env", "team"])
    assert secretsmanager.describe_secret(SecretId=created["Name"])["Tags"] == []


def test_resource_policy_round_trip(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="x")
    assert "ResourcePolicy" not in secretsmanager.get_resource_policy(SecretId=created["Name"])
    policy = {
        "Version": "2012-10-17",
        "Statement": [
            {
                "Effect": "Allow",
                "Principal": {"AWS": "arn:aws:iam::000000000000:root"},
                "Action": "secretsmanager:GetSecretValue",
                "Resource": "*",
            }
        ],
    }
    put = secretsmanager.put_resource_policy(SecretId=created["Name"], ResourcePolicy=json.dumps(policy))
    assert put["ARN"] == created["ARN"] and put["Name"] == created["Name"]
    got = secretsmanager.get_resource_policy(SecretId=created["Name"])
    assert json.loads(got["ResourcePolicy"]) == policy
    assert got["ARN"] == created["ARN"]
    secretsmanager.delete_resource_policy(SecretId=created["Name"])
    assert "ResourcePolicy" not in secretsmanager.get_resource_policy(SecretId=created["Name"])


# --- Deletion ------------------------------------------------------------------------------------------------------------


def test_delete_secret_schedules_deletion(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="x")
    out = secretsmanager.delete_secret(SecretId=created["Name"])
    assert out["ARN"] == created["ARN"] and out["Name"] == created["Name"]
    assert abs((out["DeletionDate"] - dt.datetime.now(dt.UTC)).total_seconds() - 30 * DAY) < 300, "the default window is 30 days"


def test_scheduled_secret_is_visible_to_describe_but_not_to_reads_or_writes(
    secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]
) -> None:
    created = make_secret(SecretString="x")
    out = secretsmanager.delete_secret(SecretId=created["Name"], RecoveryWindowInDays=7)
    assert abs((out["DeletionDate"] - dt.datetime.now(dt.UTC)).total_seconds() - 7 * DAY) < 300

    described = secretsmanager.describe_secret(SecretId=created["Name"])
    assert described["DeletedDate"]

    marked = "You can't perform this operation on the secret because it was marked for deletion."
    for call in (
        lambda: secretsmanager.get_secret_value(SecretId=created["Name"]),
        lambda: secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="y"),
        lambda: secretsmanager.update_secret(SecretId=created["Name"], Description="y"),
        lambda: secretsmanager.tag_resource(SecretId=created["Name"], Tags=[{"Key": "a", "Value": "b"}]),
    ):
        with raises("InvalidRequestException") as exc:
            call()
        assert exc.value.response["Error"]["Message"] == marked

    with raises("InvalidRequestException") as exc:
        secretsmanager.create_secret(Name=created["Name"], SecretString="new")
    assert exc.value.response["Error"]["Message"] == (
        "You can't create this secret because a secret with this name is already scheduled for deletion."
    )


def test_scheduled_secrets_are_listed_only_when_asked(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="x")
    secretsmanager.delete_secret(SecretId=created["Name"], RecoveryWindowInDays=7)
    flt = [{"Key": "name", "Values": [created["Name"]]}]
    assert secretsmanager.list_secrets(Filters=flt)["SecretList"] == []
    planned = secretsmanager.list_secrets(Filters=flt, IncludePlannedDeletion=True)["SecretList"]
    assert [s["Name"] for s in planned] == [created["Name"]]
    assert planned[0]["DeletedDate"]


def test_restore_secret(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="kept")
    secretsmanager.delete_secret(SecretId=created["Name"], RecoveryWindowInDays=7)
    out = secretsmanager.restore_secret(SecretId=created["Name"])
    assert out["ARN"] == created["ARN"] and out["Name"] == created["Name"]
    assert "DeletedDate" not in secretsmanager.describe_secret(SecretId=created["Name"])
    assert secretsmanager.get_secret_value(SecretId=created["Name"])["SecretString"] == "kept"


def test_force_delete_removes_the_secret_at_once(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="x")
    secretsmanager.delete_secret(SecretId=created["Name"], ForceDeleteWithoutRecovery=True)
    with raises("ResourceNotFoundException"):
        secretsmanager.describe_secret(SecretId=created["Name"])
    recreated = secretsmanager.create_secret(Name=created["Name"], SecretString="again")
    assert recreated["ARN"] != created["ARN"], "a recreated secret gets a new ARN suffix"
    secretsmanager.delete_secret(SecretId=created["Name"], ForceDeleteWithoutRecovery=True)


def test_force_delete_of_a_missing_secret_is_not_an_error(secretsmanager: Any) -> None:
    secretsmanager.delete_secret(SecretId=uniq("conf/never-existed"), ForceDeleteWithoutRecovery=True)


def test_delete_secret_option_validation(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="x")
    with raises("InvalidParameterException"):
        secretsmanager.delete_secret(SecretId=created["Name"], ForceDeleteWithoutRecovery=True, RecoveryWindowInDays=7)
    for days in (6, 31):
        with raises("InvalidParameterException"):
            secretsmanager.delete_secret(SecretId=created["Name"], RecoveryWindowInDays=days)
    assert "DeletedDate" not in secretsmanager.describe_secret(SecretId=created["Name"])


def test_secret_is_purged_when_the_recovery_window_ends_on_the_emulator_clock(
    secretsmanager: Any, make_secret: Callable[..., dict[str, Any]], clock: Admin
) -> None:
    created = make_secret(SecretString="x")
    secretsmanager.delete_secret(SecretId=created["Name"], RecoveryWindowInDays=7)

    clock.advance(7 * DAY - 120)
    assert secretsmanager.describe_secret(SecretId=created["Name"])["DeletedDate"]
    with raises("InvalidRequestException"):
        secretsmanager.create_secret(Name=created["Name"], SecretString="too early")

    clock.advance(240)
    with raises("ResourceNotFoundException"):
        secretsmanager.describe_secret(SecretId=created["Name"])
    reborn = secretsmanager.create_secret(Name=created["Name"], SecretString="after the window")
    assert reborn["ARN"] != created["ARN"]
    secretsmanager.delete_secret(SecretId=created["Name"], ForceDeleteWithoutRecovery=True)


# --- Customer managed keys --------------------------------------------------------------------------------------------------


def test_secret_with_a_customer_managed_key(
    secretsmanager: Any, make_secret: Callable[..., dict[str, Any]], make_key: Callable[..., dict[str, Any]]
) -> None:
    key = make_key("secret key")
    created = make_secret(SecretString="encrypted", KmsKeyId=key["KeyId"])
    described = secretsmanager.describe_secret(SecretId=created["Name"])
    assert described["KmsKeyId"] in (key["KeyId"], key["Arn"])
    assert secretsmanager.get_secret_value(SecretId=created["Name"])["SecretString"] == "encrypted"


def test_disabled_customer_key_blocks_reads_and_writes(
    secretsmanager: Any, kms: Any, make_secret: Callable[..., dict[str, Any]], make_key: Callable[..., dict[str, Any]]
) -> None:
    key = make_key("secret key")
    created = make_secret(SecretString="encrypted", KmsKeyId=key["KeyId"])
    kms.disable_key(KeyId=key["KeyId"])

    with raises("DecryptionFailure") as read:
        secretsmanager.get_secret_value(SecretId=created["Name"])
    assert status_of(read.value) == 400
    with raises("EncryptionFailure"):
        secretsmanager.put_secret_value(SecretId=created["Name"], SecretString="new")

    kms.enable_key(KeyId=key["KeyId"])
    assert secretsmanager.get_secret_value(SecretId=created["Name"])["SecretString"] == "encrypted"


def test_default_key_is_not_reported(secretsmanager: Any, make_secret: Callable[..., dict[str, Any]]) -> None:
    created = make_secret(SecretString="x")
    assert "KmsKeyId" not in secretsmanager.describe_secret(SecretId=created["Name"])
