"""Shared fixtures, and a guard that keeps the tests off real AWS.

The tests run only when AWS_ENDPOINT_URL is a loopback URL, so a shell that
still points at AWS can never send them there.
"""

from __future__ import annotations

import ipaddress
import os
from typing import Any
from urllib.parse import urlsplit

import boto3
import pytest


def _is_loopback(url: str) -> bool:
    host = (urlsplit(url).hostname or "").lower()
    if host == "localhost" or host.endswith(".localhost"):
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def pytest_configure(config: pytest.Config) -> None:
    endpoint = os.environ.get("AWS_ENDPOINT_URL", "")
    if not _is_loopback(endpoint):
        pytest.exit(
            f"AWS_ENDPOINT_URL is {endpoint!r}, not a loopback URL, so the tests refuse to run "
            'rather than reach real AWS. Start understudy, then run: eval "$(understudy env)"',
            returncode=2,
        )


@pytest.fixture(scope="session")
def endpoint() -> str:
    return os.environ["AWS_ENDPOINT_URL"].rstrip("/")


@pytest.fixture(scope="session")
def s3() -> Any:
    return boto3.client("s3")


@pytest.fixture(scope="session")
def sqs() -> Any:
    return boto3.client("sqs")
