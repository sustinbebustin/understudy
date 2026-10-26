"""Helpers for the testcontainers-understudy suite.

The suite runs a real understudy image through scripts/sealed and passes the
container's endpoint to every client explicitly (testcontainers.md 8.7). The
module's design is testcontainers.md sections 8 and 9.
"""

from __future__ import annotations

import io
import json
import os
import urllib.error
import urllib.request
import zipfile
from datetime import datetime
from typing import Any

import boto3
import docker
from docker.models.containers import Container
from testcontainers.core.labels import LABEL_SESSION_ID, SESSION_ID

from testcontainers_understudy import UnderstudyContainer

REGION = "us-east-1"


def image() -> str:
    """The image under test: UNDERSTUDY_IMAGE, else the local build `just image` tags (testcontainers.md 8.7)."""
    return os.environ.get("UNDERSTUDY_IMAGE") or "understudy:dev"


def client(u: UnderstudyContainer, service: str) -> Any:
    """A boto3 client for the container, signed with the test pair understudytest.Env uses (testcontainers.md 3.8)."""
    return boto3.client(
        service,
        endpoint_url=u.get_endpoint(),
        region_name=REGION,
        aws_access_key_id="test",
        aws_secret_access_key="test",
    )


def docker_client() -> docker.DockerClient:
    return docker.from_env()


def attrs(u: UnderstudyContainer) -> dict[str, Any]:
    """The container's docker inspect document."""
    wrapped = u.get_wrapped_container()
    wrapped.reload()
    return wrapped.attrs


def container_env(u: UnderstudyContainer) -> dict[str, str]:
    """The container's environment; Docker keeps the last value of a repeated name."""
    env: dict[str, str] = {}
    for kv in attrs(u)["Config"]["Env"] or []:
        k, _, v = kv.partition("=")
        env[k] = v
    return env


def managed_session_containers() -> list[Container]:
    """understudy's own containers carrying this tc-python session's label.

    understudy labels what it creates understudy.dev/managed=true (testcontainers.md 3.6);
    tc-python's session label is org.testcontainers.session-id (testcontainers.md 5.4).
    """
    return docker_client().containers.list(
        all=True, filters={"label": ["understudy.dev/managed=true", f"{LABEL_SESSION_ID}={SESSION_ID}"]}
    )


def image_containers() -> list[Container]:
    """This session's containers of the image under test."""
    return docker_client().containers.list(
        all=True, filters={"ancestor": image(), "label": f"{LABEL_SESSION_ID}={SESSION_ID}"}
    )


def docker_socket_gid() -> int:
    """The group owning the local Docker socket the client uses (testcontainers.md 8.5)."""
    host = os.environ.get("DOCKER_HOST", "unix:///var/run/docker.sock")
    if not host.startswith("unix://"):
        raise AssertionError(f"the Docker daemon is at {host}, not a local unix socket; this test needs one")
    return os.stat(host.removeprefix("unix://")).st_gid


class Answer:
    """One admin API answer: the status, the raw body and the decoded JSON body (None when not JSON)."""

    def __init__(self, status: int, raw: bytes, body: Any) -> None:
        self.status = status
        self.raw = raw
        self.body = body


def admin_call(u: UnderstudyContainer, method: str, path: str, token: str = "", body: Any = None) -> Answer:
    """Call /_understudy/v1<path> on the container, with the bearer token when given (testcontainers.md 3.3, 3.7)."""
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(u.get_endpoint() + "/_understudy/v1" + path, data=data, method=method)
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            raw = resp.read()
            status = resp.status
    except urllib.error.HTTPError as e:
        raw = e.read()
        status = e.code
    try:
        return Answer(status, raw, json.loads(raw))
    except ValueError:
        return Answer(status, raw, None)


def parse_time(s: str) -> datetime:
    """An RFC 3339 instant as the admin API writes it."""
    return datetime.fromisoformat(s.replace("Z", "+00:00"))


def lambda_zip(files: dict[str, str]) -> bytes:
    """A zip archive of text files for CreateFunction."""
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
        for name, text in files.items():
            info = zipfile.ZipInfo(name)
            info.external_attr = 0o644 << 16
            zf.writestr(info, text)
    return buf.getvalue()


# The trust policy Lambda's execution role needs
# (https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html).
LAMBDA_TRUST_POLICY = json.dumps(
    {
        "Version": "2012-10-17",
        "Statement": [
            {"Effect": "Allow", "Principal": {"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}
        ],
    }
)


def invoke_echo(u: UnderstudyContainer) -> None:
    """Create a python3.13 function echoing its event and invoke it once.

    The runtime image is in the local cache and understudy pulls only a missing image, so
    nothing is pulled from public.ecr.aws.
    """
    role = client(u, "iam").create_role(RoleName="echo-role", AssumeRolePolicyDocument=LAMBDA_TRUST_POLICY)
    fn = client(u, "lambda")
    fn.create_function(
        FunctionName="echo",
        Role=role["Role"]["Arn"],
        Runtime="python3.13",
        Handler="handler.echo",
        Code={"ZipFile": lambda_zip({"handler.py": "def echo(event, context):\n    return event\n"})},
    )
    fn.get_waiter("function_active_v2").wait(FunctionName="echo")
    out = fn.invoke(FunctionName="echo", Payload=b'{"ok": 1}')
    # A synchronous invoke answers 200, and a handler error would set FunctionError
    # (https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html).
    payload = out["Payload"].read()
    assert out["StatusCode"] == 200, out
    assert "FunctionError" not in out, payload
    assert json.loads(payload) == {"ok": 1}
