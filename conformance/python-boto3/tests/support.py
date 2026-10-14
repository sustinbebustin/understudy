"""Helpers shared by the conformance tests.

Nothing here encodes understudy behavior. The helpers wrap the admin API,
build inputs, and poll for eventual outcomes with a bounded wait.
"""

from __future__ import annotations

import contextlib
import hashlib
import http.client
import io
import json
import os
import struct
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
import zipfile
from collections.abc import Callable, Iterator
from typing import Any, TypeVar

import pytest
from botocore.auth import S3SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials
from botocore.exceptions import ClientError

T = TypeVar("T")

ADMIN_PREFIX = "/_understudy/v1"


def uniq(prefix: str) -> str:
    """A name unique to this run, short enough for the strictest name limits."""
    return f"{prefix}-{uuid.uuid4().hex[:10]}"


REGION = os.environ.get("AWS_REGION", "us-east-1")


def error_code(err: ClientError) -> str:
    return err.response["Error"]["Code"]


@contextlib.contextmanager
def raises(code: str) -> Iterator[pytest.ExceptionInfo[ClientError]]:
    """Expect a ClientError whose error code is `code`; the block receives the exception info."""
    with pytest.raises(ClientError) as info:
        yield info
    assert error_code(info.value) == code, info.value.response


def status_of(err: ClientError) -> int:
    return err.response["ResponseMetadata"]["HTTPStatusCode"]


def eventually(
    produce: Callable[[], T],
    done: Callable[[T], bool],
    *,
    timeout: float = 15.0,
    interval: float = 0.2,
    what: str = "condition",
) -> T:
    """Call produce until done(result) holds, and return that result.

    Raises AssertionError naming `what` and the last result when the wait
    runs out, so a failing test shows what the service last answered.
    """
    deadline = time.monotonic() + timeout
    while True:
        result = produce()
        if done(result):
            return result
        if time.monotonic() >= deadline:
            raise AssertionError(f"timed out after {timeout}s waiting for {what}; last result: {result!r}")
        time.sleep(interval)


class AdminError(Exception):
    """An admin API call that did not return 2xx."""

    def __init__(self, method: str, path: str, status: int, body: str) -> None:
        super().__init__(f"{method} {path} answered {status}: {body}")
        self.status = status
        self.body = body


class Admin:
    """The understudy admin API: clock, settle and reset."""

    def __init__(self, endpoint: str) -> None:
        self._base = endpoint.rstrip("/") + ADMIN_PREFIX

    def _call(self, method: str, path: str, body: dict[str, Any] | None = None, timeout: float = 60.0) -> dict[str, Any]:
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self._base + path, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as err:
            with err:
                raise AdminError(method, path, err.code, err.read().decode(errors="replace")) from err
        return json.loads(raw) if raw else {}

    def health(self) -> dict[str, Any]:
        return self._call("GET", "/health")

    def clock(self) -> dict[str, Any]:
        return self._call("GET", "/clock")

    def freeze(self) -> dict[str, Any]:
        """Stop emulator time, so only advance moves it."""
        return self._call("POST", "/clock", {"op": "freeze"})

    def advance(self, seconds: float) -> dict[str, Any]:
        """Move emulator time forward, firing the timers due on the way, then settle."""
        result = self._call("POST", "/clock", {"op": "advance", "by": f"{seconds}s"})
        self.settle()
        return result

    def settle(self, timeout_seconds: int = 30) -> dict[str, Any]:
        """Wait until the emulator has no queued work (deliveries, invocations, due timers)."""
        result = self._call("GET", f"/settle?timeout={timeout_seconds}s", timeout=timeout_seconds + 10)
        if not result.get("settled", False):
            raise AssertionError(f"emulator did not settle within {timeout_seconds}s: {result}")
        return result

    def restore_clock(self) -> None:
        """Return the clock to how serve started it, keeping the state of the services that matter.

        Reset needs a service selection to keep other state; STS holds none.
        """
        self._call("POST", "/reset", {"services": ["sts"], "clock": True})


def lambda_zip(files: dict[str, str]) -> bytes:
    """A zip archive of text files for CreateFunction."""
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
        for name, text in files.items():
            info = zipfile.ZipInfo(name)
            info.external_attr = 0o644 << 16
            zf.writestr(info, text)
    return buf.getvalue()


def sqs_attribute_md5(attributes: dict[str, dict[str, Any]]) -> str:
    """MD5OfMessageAttributes as AWS computes it (sqs.md 5.4).

    Attributes are sorted by name; each contributes the length-prefixed name,
    length-prefixed data type, a transport byte (1 string, 2 binary) and the
    length-prefixed value. Lengths are 4-byte big-endian.
    """
    h = hashlib.md5()  # noqa: S324 -- the protocol mandates MD5

    def part(data: bytes) -> None:
        h.update(struct.pack(">I", len(data)))
        h.update(data)

    for name in sorted(attributes):
        attr = attributes[name]
        part(name.encode())
        part(attr["DataType"].encode())
        if "BinaryValue" in attr:
            h.update(b"\x02")
            part(attr["BinaryValue"])
        else:
            h.update(b"\x01")
            part(attr["StringValue"].encode())
    return h.hexdigest()


def md5_hex(data: bytes) -> str:
    return hashlib.md5(data).hexdigest()  # noqa: S324 -- S3 ETags are MD5


def http_get(url: str, headers: dict[str, str] | None = None) -> tuple[int, dict[str, str], bytes]:
    """GET without any AWS client: status, lower-cased headers, body. Never raises on 4xx/5xx."""
    return http_request("GET", url, headers=headers)


def http_request(
    method: str,
    url: str,
    *,
    data: bytes | None = None,
    headers: dict[str, str] | None = None,
) -> tuple[int, dict[str, str], bytes]:
    req = urllib.request.Request(url, data=data, method=method)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, {k.lower(): v for k, v in resp.headers.items()}, resp.read()
    except urllib.error.HTTPError as err:
        with err:
            return err.code, {k.lower(): v for k, v in err.headers.items()}, err.read()


def url_host_port(url: str) -> tuple[str, int]:
    parts = urllib.parse.urlsplit(url)
    assert parts.hostname is not None and parts.port is not None
    return parts.hostname, parts.port


def signed_http(
    method: str,
    url: str,
    *,
    connect_to: tuple[str, int],
    region: str,
    headers: dict[str, str] | None = None,
    body: bytes = b"",
    trailer_checksum: bool = False,
    access_key: str = "test",
    secret_key: str = "test",
) -> tuple[int, dict[str, str], bytes]:
    """Send one SigV4-signed S3 request whose Host is taken from `url`.

    The TCP connection goes to `connect_to`, so a virtual-hosted URL such as
    http://bucket.s3.localhost:PORT/key works without DNS. With
    `trailer_checksum` the request is signed as STREAMING-UNSIGNED-PAYLOAD-TRAILER,
    the form current SDKs use for streamed uploads over TLS; the caller supplies the
    aws-chunked body.
    """
    request = AWSRequest(method=method, url=url, data=body, headers=headers or {})
    if trailer_checksum:
        request.context["checksum"] = {"request_algorithm": {"algorithm": "crc32", "in": "trailer", "name": "x-amz-checksum-crc32"}}
    S3SigV4Auth(Credentials(access_key, secret_key), "s3", region).add_auth(request)
    prepared = request.prepare()

    split = urllib.parse.urlsplit(url)
    target = split.path + (f"?{split.query}" if split.query else "")
    conn = http.client.HTTPConnection(*connect_to, timeout=30)
    try:
        conn.putrequest(method, target, skip_host=True, skip_accept_encoding=True)
        # botocore signs the Host header without adding it to the request.
        conn.putheader("Host", split.netloc)
        for name, value in prepared.headers.items():
            if name.lower() != "host":
                conn.putheader(name, value)
        conn.endheaders(prepared.body)
        resp = conn.getresponse()
        return resp.status, {k.lower(): v for k, v in resp.getheaders()}, resp.read()
    finally:
        conn.close()
