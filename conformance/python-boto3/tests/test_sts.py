"""STS: GetCallerIdentity and AssumeRole (iam-sts-kms.md section 4)."""

from __future__ import annotations

import datetime as dt
import json
import re
from collections.abc import Callable
from typing import Any

import pytest
from botocore.exceptions import ClientError

from support import error_code, uniq

TRUST_ANY_ACCOUNT_PRINCIPAL = json.dumps(
    {
        "Version": "2012-10-17",
        "Statement": [
            {
                "Effect": "Allow",
                "Principal": {"AWS": "arn:aws:iam::000000000000:root"},
                "Action": "sts:AssumeRole",
            }
        ],
    }
)


@pytest.fixture
def role(iam: Any, cleanup: Any) -> dict[str, Any]:
    name = uniq("conf-sts-role")
    created = iam.create_role(RoleName=name, AssumeRolePolicyDocument=TRUST_ANY_ACCOUNT_PRINCIPAL)["Role"]
    cleanup.add(iam.delete_role, RoleName=name)
    return created


def test_get_caller_identity_is_the_account_root(sts: Any) -> None:
    ident = sts.get_caller_identity()
    assert re.fullmatch(r"\d{12}", ident["Account"])
    assert ident["Arn"] == f"arn:aws:iam::{ident['Account']}:root"
    # The principal table: the root user's id is the account id.
    assert ident["UserId"] == ident["Account"]
    assert ident["ResponseMetadata"]["HTTPStatusCode"] == 200


@pytest.mark.parametrize("account", ["123456789012", "000000000001", "999999999999"])
def test_account_is_derived_from_the_access_key(make_client: Callable[..., Any], account: str) -> None:
    # A 12-digit access key names its account (ADR 0012). Keys that merely look like real
    # AKIA keys are deliberately not decoded, so the suite does not assert that.
    client = make_client("sts", access_key=account)
    ident = client.get_caller_identity()
    assert ident["Account"] == account
    assert ident["Arn"] == f"arn:aws:iam::{account}:root"


def test_get_caller_identity_ignores_the_region(make_client: Callable[..., Any]) -> None:
    ident = make_client("sts", region="eu-west-1").get_caller_identity()
    assert re.fullmatch(r"\d{12}", ident["Account"])


def test_assume_role_returns_temporary_credentials(sts: Any, role: dict[str, Any]) -> None:
    before = dt.datetime.now(dt.UTC)
    out = sts.assume_role(RoleArn=role["Arn"], RoleSessionName="conformance-session")

    creds = out["Credentials"]
    assert creds["AccessKeyId"].startswith("ASIA")
    assert len(creds["AccessKeyId"]) == 20
    assert creds["SecretAccessKey"]
    assert creds["SessionToken"]
    # DurationSeconds defaults to 3600.
    lifetime = (creds["Expiration"] - before).total_seconds()
    assert 3500 <= lifetime <= 3700, lifetime

    account = role["Arn"].split(":")[4]
    user = out["AssumedRoleUser"]
    assert user["Arn"] == f"arn:aws:sts::{account}:assumed-role/{role['RoleName']}/conformance-session"
    assert user["AssumedRoleId"] == f"{role['RoleId']}:conformance-session"


def test_assumed_credentials_identify_the_assumed_role(
    sts: Any, make_client: Callable[..., Any], role: dict[str, Any]
) -> None:
    creds = sts.assume_role(RoleArn=role["Arn"], RoleSessionName="who-am-i")["Credentials"]
    assumed = make_client(
        "sts",
        access_key=creds["AccessKeyId"],
        secret_key=creds["SecretAccessKey"],
        session_token=creds["SessionToken"],
    )
    ident = assumed.get_caller_identity()
    account = role["Arn"].split(":")[4]
    assert ident["Account"] == account
    assert ident["Arn"] == f"arn:aws:sts::{account}:assumed-role/{role['RoleName']}/who-am-i"
    assert ident["UserId"] == f"{role['RoleId']}:who-am-i"


def test_assume_role_with_duration_and_source_identity(sts: Any, role: dict[str, Any]) -> None:
    out = sts.assume_role(
        RoleArn=role["Arn"],
        RoleSessionName="short",
        DurationSeconds=900,
        SourceIdentity="conformance-user",
    )
    assert out["SourceIdentity"] == "conformance-user"
    lifetime = (out["Credentials"]["Expiration"] - dt.datetime.now(dt.UTC)).total_seconds()
    assert 800 <= lifetime <= 1000, lifetime


def test_assume_role_rejects_a_duration_above_the_role_maximum(sts: Any, role: dict[str, Any]) -> None:
    # A new role has MaxSessionDuration 3600.
    with pytest.raises(ClientError) as exc:
        sts.assume_role(RoleArn=role["Arn"], RoleSessionName="too-long", DurationSeconds=7200)
    assert error_code(exc.value) == "ValidationError"
    assert exc.value.response["Error"]["Message"] == "The requested DurationSeconds exceeds the MaxSessionDuration set for this role."


def test_assume_role_rejects_a_duration_above_the_service_maximum(sts: Any, role: dict[str, Any]) -> None:
    with pytest.raises(ClientError) as exc:
        sts.assume_role(RoleArn=role["Arn"], RoleSessionName="too-long", DurationSeconds=43201)
    assert error_code(exc.value) == "ValidationError"
    assert "durationSeconds" in exc.value.response["Error"]["Message"]


def test_assume_role_rejects_an_invalid_session_name(sts: Any, role: dict[str, Any]) -> None:
    with pytest.raises(ClientError) as exc:
        sts.assume_role(RoleArn=role["Arn"], RoleSessionName="has space")
    assert error_code(exc.value) == "ValidationError"
    assert "roleSessionName" in exc.value.response["Error"]["Message"]


def test_assume_role_on_a_missing_role_is_access_denied(sts: Any) -> None:
    arn = f"arn:aws:iam::000000000000:role/{uniq('conf-missing')}"
    with pytest.raises(ClientError) as exc:
        sts.assume_role(RoleArn=arn, RoleSessionName="nobody")
    assert error_code(exc.value) == "AccessDenied"
    assert exc.value.response["ResponseMetadata"]["HTTPStatusCode"] == 403
    assert "is not authorized to perform: sts:AssumeRole" in exc.value.response["Error"]["Message"]
    assert arn in exc.value.response["Error"]["Message"]
