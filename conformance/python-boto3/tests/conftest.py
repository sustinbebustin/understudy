"""Fixtures for the understudy boto3 conformance suite.

The suite talks only to the endpoint in AWS_ENDPOINT_URL, which run.sh sets
through scripts/sealed. A non-loopback endpoint stops the run before any
request is sent, so no test can reach real AWS.
"""

from __future__ import annotations

import ipaddress
import os
import urllib.parse
from collections.abc import Callable, Iterator
from typing import Any

import boto3
import botocore
import pytest
from botocore.config import Config
from botocore.exceptions import ClientError

from support import REGION, Admin, uniq


def _require_loopback_endpoint() -> str:
    endpoint = os.environ.get("AWS_ENDPOINT_URL", "")
    if not endpoint:
        raise pytest.UsageError(
            "AWS_ENDPOINT_URL is not set. Run the suite through ./run.sh, or through "
            "scripts/sealed --endpoint http://127.0.0.1:<port> against a running understudy."
        )
    host = (urllib.parse.urlsplit(endpoint).hostname or "").lower()
    try:
        loopback = ipaddress.ip_address(host).is_loopback
    except ValueError:
        loopback = host == "localhost" or host.endswith(".localhost")
    if not loopback:
        raise pytest.UsageError(
            f"AWS_ENDPOINT_URL={endpoint!r} is not a loopback address. The suite refuses to run "
            "against anything but a local emulator."
        )
    return endpoint


def pytest_report_header(config: pytest.Config) -> list[str]:
    protocol = boto3.client(
        "sqs",
        region_name=REGION,
        endpoint_url="http://127.0.0.1:1",
        aws_access_key_id="unused",
        aws_secret_access_key="unused",
    ).meta.service_model.protocol
    return [
        f"boto3 {boto3.__version__}, botocore {botocore.__version__}, SQS wire protocol: {protocol}",
        f"endpoint: {os.environ.get('AWS_ENDPOINT_URL', '(unset)')}",
    ]


@pytest.fixture(scope="session")
def endpoint() -> str:
    return _require_loopback_endpoint()


@pytest.fixture(scope="session")
def admin(endpoint: str) -> Admin:
    return Admin(endpoint)


@pytest.fixture(scope="session")
def sigv4_verify() -> None:
    """Skip unless understudy runs with signature verification.

    This build's serve refuses --sigv4 verify: verification is deferred
    (docs/plan/DEFERRED.md, "SigV4 verification"), so these tests skip.
    """
    if os.environ.get("CONFORMANCE_SIGV4") != "verify":
        pytest.skip(
            "needs signature verification, which this build of understudy does not apply yet "
            "(docs/plan/DEFERRED.md, 'SigV4 verification')"
        )


@pytest.fixture(scope="session")
def boto_config() -> Config:
    # One attempt: a retry would hide a flaky or wrong answer from the report.
    return Config(
        retries={"max_attempts": 1, "mode": "standard"},
        connect_timeout=5,
        read_timeout=60,
        s3={"addressing_style": "path"},
    )


@pytest.fixture(scope="session")
def make_client(endpoint: str, boto_config: Config) -> Callable[..., Any]:
    def build(
        service: str,
        *,
        region: str = REGION,
        access_key: str = "test",
        secret_key: str = "test",
        session_token: str | None = None,
        config: Config | None = None,
        **extra: Any,
    ) -> Any:
        return boto3.client(
            service,
            region_name=region,
            endpoint_url=endpoint,
            aws_access_key_id=access_key,
            aws_secret_access_key=secret_key,
            aws_session_token=session_token,
            config=boto_config if config is None else boto_config.merge(config),
            **extra,
        )

    return build


def _client_fixture(service: str) -> Callable[..., Any]:
    @pytest.fixture(scope="session", name=service)
    def fixture(make_client: Callable[..., Any]) -> Any:
        return make_client(service)

    return fixture


s3 = _client_fixture("s3")
sqs = _client_fixture("sqs")
sns = _client_fixture("sns")
dynamodb = _client_fixture("dynamodb")
secretsmanager = _client_fixture("secretsmanager")
kms = _client_fixture("kms")
logs = _client_fixture("logs")
sts = _client_fixture("sts")
iam = _client_fixture("iam")


@pytest.fixture(scope="session", name="lambda_")
def lambda_client(make_client: Callable[..., Any]) -> Any:
    return make_client("lambda")


class Cleanup:
    """Deferred deletions, run last-in first-out after a test.

    Service errors during cleanup are ignored: the test already decided
    pass or fail, and a resource the test deleted itself is fine to miss.
    """

    def __init__(self) -> None:
        self._actions: list[Callable[[], object]] = []

    def add(self, fn: Callable[..., object], *args: Any, **kwargs: Any) -> None:
        self._actions.append(lambda: fn(*args, **kwargs))

    def run(self) -> None:
        while self._actions:
            action = self._actions.pop()
            try:
                action()
            except ClientError:
                pass


@pytest.fixture
def cleanup() -> Iterator[Cleanup]:
    c = Cleanup()
    try:
        yield c
    finally:
        c.run()


@pytest.fixture
def clock(admin: Admin) -> Iterator[Admin]:
    """The admin client, with the emulator clock put back after the test."""
    try:
        yield admin
    finally:
        admin.restore_clock()


@pytest.fixture
def make_bucket(s3: Any, cleanup: Cleanup) -> Callable[..., str]:
    def make(prefix: str = "conf", **create_args: Any) -> str:
        name = uniq(prefix)
        s3.create_bucket(Bucket=name, **create_args)
        cleanup.add(_empty_and_delete_bucket, s3, name)
        return name

    return make


def _empty_and_delete_bucket(s3_client: Any, bucket: str) -> None:
    paginator = s3_client.get_paginator("list_object_versions")
    for page in paginator.paginate(Bucket=bucket):
        for item in [*page.get("Versions", []), *page.get("DeleteMarkers", [])]:
            s3_client.delete_object(Bucket=bucket, Key=item["Key"], VersionId=item["VersionId"])
    pager = s3_client.get_paginator("list_objects_v2")
    for page in pager.paginate(Bucket=bucket):
        for obj in page.get("Contents", []):
            s3_client.delete_object(Bucket=bucket, Key=obj["Key"])
    s3_client.delete_bucket(Bucket=bucket)


@pytest.fixture
def make_key(kms: Any, cleanup: Cleanup) -> Callable[..., dict[str, Any]]:
    """Create a customer managed symmetric key; returns its KeyMetadata."""

    def make(description: str = "conformance", alias: str | None = None, **create_args: Any) -> dict[str, Any]:
        meta = kms.create_key(Description=description, **create_args)["KeyMetadata"]
        cleanup.add(kms.schedule_key_deletion, KeyId=meta["KeyId"], PendingWindowInDays=7)
        if alias is not None:
            kms.create_alias(AliasName=alias, TargetKeyId=meta["KeyId"])
            cleanup.add(kms.delete_alias, AliasName=alias)
        return meta

    return make


@pytest.fixture
def make_queue(sqs: Any, cleanup: Cleanup) -> Callable[..., str]:
    def make(
        prefix: str = "conf",
        *,
        fifo: bool = False,
        attributes: dict[str, str] | None = None,
        tags: dict[str, str] | None = None,
    ) -> str:
        name = uniq(prefix) + (".fifo" if fifo else "")
        attrs = dict(attributes or {})
        if fifo:
            attrs["FifoQueue"] = "true"
        args: dict[str, Any] = {"QueueName": name}
        if attrs:
            args["Attributes"] = attrs
        if tags:
            args["tags"] = tags
        url = sqs.create_queue(**args)["QueueUrl"]
        cleanup.add(sqs.delete_queue, QueueUrl=url)
        return url

    return make


@pytest.fixture
def queue_arn(sqs: Any) -> Callable[[str], str]:
    def get(url: str) -> str:
        return sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]

    return get
