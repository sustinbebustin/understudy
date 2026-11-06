"""The module against a real understudy container (testcontainers.md 8.7).

Each test starts the image under test through the module and checks what testcontainers.md
sections 8 and 9 say the container is, does and leaves behind.
"""

from __future__ import annotations

import contextlib
import json
import re
import time
from collections.abc import Callable, Iterator
from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest
from testcontainers.core.config import testcontainers_config
from testcontainers.core.exceptions import ContainerStartException
from testcontainers.core.labels import LABEL_SESSION_ID, SESSION_ID

from support import (
    admin_call,
    attrs,
    client,
    container_env,
    docker_socket_gid,
    image,
    image_containers,
    invoke_echo,
    managed_session_containers,
    parse_time,
)
from testcontainers_understudy import (
    UnderstudyAdminError,
    UnderstudyContainer,
    UnderstudyDockerError,
)


@pytest.fixture
def understudy() -> Iterator[UnderstudyContainer]:
    with UnderstudyContainer(image()) as u:
        yield u


def test_sqs_round_trip(understudy: UnderstudyContainer) -> None:
    # Each module test does a real client round trip (testcontainers.md 8.7).
    sqs = client(understudy, "sqs")
    url = sqs.create_queue(QueueName="orders")["QueueUrl"]
    sqs.send_message(QueueUrl=url, MessageBody="hello from testcontainers")
    got = sqs.receive_message(QueueUrl=url, WaitTimeSeconds=1)
    assert [m["Body"] for m in got.get("Messages", [])] == ["hello from testcontainers"]


def test_env_mirrors_understudytest(understudy: UnderstudyContainer) -> None:
    # The endpoint names 127.0.0.1, never localhost, so SDKs address S3 path style, and the
    # environment is understudytest.Env's five variables (testcontainers.md 8.2, 3.8, 9.7).
    endpoint = understudy.get_endpoint()
    assert re.fullmatch(r"http://127\.0\.0\.1:[0-9]+", endpoint), endpoint
    assert endpoint == f"http://127.0.0.1:{understudy.get_exposed_port(4566)}"
    assert understudy.get_env() == {
        "AWS_ENDPOINT_URL": endpoint,
        "AWS_ACCESS_KEY_ID": "test",
        "AWS_SECRET_ACCESS_KEY": "test",
        "AWS_REGION": "us-east-1",
        "AWS_DEFAULT_REGION": "us-east-1",
    }


def test_port_is_published_on_loopback_only(understudy: UnderstudyContainer) -> None:
    # 127.0.0.1 with an ephemeral port, since the image's admin API has no token
    # (testcontainers.md 8.4, finding 2); the module creates no network (testcontainers.md
    # 10.2).
    a = attrs(understudy)
    assert a["HostConfig"]["PortBindings"] == {"4566/tcp": [{"HostIp": "127.0.0.1", "HostPort": "0"}]}
    for port, bindings in (a["NetworkSettings"]["Ports"] or {}).items():
        for b in bindings or []:
            assert b["HostIp"] == "127.0.0.1", (port, b)
    assert sorted(a["NetworkSettings"]["Networks"]) == ["bridge"]
    # Ready is reported only once health answers 200 with ready true (testcontainers.md
    # 8.3, 9.7).
    health = admin_call(understudy, "GET", "/health")
    assert health.status == 200 and health.body["ready"] is True


def test_child_labels_are_the_session_label() -> None:
    # tc-python's Ryuk filter is the session label alone (testcontainers.md 8.6, 5.4), and a
    # value the user sets through the env option wins.
    with UnderstudyContainer(image()) as u:
        assert json.loads(container_env(u)["UNDERSTUDY_DOCKER_LABELS"]) == {LABEL_SESSION_ID: SESSION_ID}
    mine = '{"com.example/team": "payments"}'
    with UnderstudyContainer(image()).with_env("UNDERSTUDY_DOCKER_LABELS", mine) as u:
        assert container_env(u)["UNDERSTUDY_DOCKER_LABELS"] == mine


FROZEN = datetime(2026, 1, 2, 3, 4, 5, 6, tzinfo=timezone.utc)


@pytest.mark.parametrize(
    ("configure", "want"),
    [
        (lambda u: u.with_clock(frozen_at=FROZEN), {"UNDERSTUDY_CLOCK": "frozen@" + FROZEN.isoformat()}),
        (lambda u: u.with_clock(scale=60), {"UNDERSTUDY_CLOCK_SCALE": "60"}),
        (lambda u: u.with_clock(scale=2.5), {"UNDERSTUDY_CLOCK_SCALE": "2.5"}),
        (lambda u: u.with_seed(2**64 - 1), {"UNDERSTUDY_SEED": "18446744073709551615"}),
        (lambda u: u.with_sigv4_verify(), {"UNDERSTUDY_SIGV4": "verify"}),
    ],
    ids=["frozen", "scale", "scale-fraction", "seed", "sigv4"],
)
def test_options_set_their_variables(
    configure: Callable[[UnderstudyContainer], UnderstudyContainer], want: dict[str, str]
) -> None:
    # Each option sets the variable testcontainers.md 8.2 names.
    with configure(UnderstudyContainer(image())) as u:
        env = container_env(u)
        assert {k: env.get(k) for k in want} == want


def test_with_clock_alone_sets_nothing() -> None:
    with UnderstudyContainer(image()).with_clock() as u:
        env = container_env(u)
        assert "UNDERSTUDY_CLOCK" not in env and "UNDERSTUDY_CLOCK_SCALE" not in env


def test_helpers() -> None:
    # The helpers call the endpoints of testcontainers.md 3.7: advance moves the clock,
    # settle answers once idle, reset drops service state but keeps the clock.
    at = datetime(2026, 1, 1, 2, 0, 0, tzinfo=timezone(timedelta(hours=2)))
    with UnderstudyContainer(image()).with_clock(frozen_at=at) as u:
        clock = admin_call(u, "GET", "/clock").body
        assert clock["mode"] == "frozen" and parse_time(clock["now"]) == at
        sqs = client(u, "sqs")
        sqs.create_queue(QueueName="before-reset")
        u.advance_clock(timedelta(seconds=90))
        assert parse_time(admin_call(u, "GET", "/clock").body["now"]) == at + timedelta(seconds=90)
        # Sent in whole microseconds, so a sub-millisecond advance is not rounded away.
        u.advance_clock(timedelta(microseconds=1500))
        assert parse_time(admin_call(u, "GET", "/clock").body["now"]) == at + timedelta(seconds=90, microseconds=1500)
        u.settle(5)
        u.reset()
        assert sqs.list_queues().get("QueueUrls", []) == []
        clock = admin_call(u, "GET", "/clock").body
        assert clock["mode"] == "frozen" and parse_time(clock["now"]) == at + timedelta(seconds=90, microseconds=1500)


def test_helpers_raise_the_admin_error() -> None:
    # A non-2xx answer raises UnderstudyAdminError with the envelope's code. understudy
    # refuses a clock moved backwards and a settle timeout that is not positive as
    # invalid_request, 400 (admin/errors.go; internal/server/admin/settle.go).
    with UnderstudyContainer(image()) as u:
        with pytest.raises(UnderstudyAdminError) as raised:
            u.advance_clock(timedelta(seconds=-1))
        assert (raised.value.status, raised.value.code) == (400, "invalid_request")
        # settle sends whole milliseconds, so 0.1 ms is "0ms".
        with pytest.raises(UnderstudyAdminError) as raised:
            u.settle(0.0001)
        assert (raised.value.status, raised.value.code) == (400, "invalid_request")
        assert '"0ms"' in raised.value.message


@pytest.mark.parametrize("order", ["publish-then-token", "token-then-publish"])
def test_admin_token_guards_the_admin_api(order: str) -> None:
    # With the token option the container holds a random 32-byte token in hex and accepts
    # the open AWS API; health without it answers 401, and the helpers send it
    # (testcontainers.md 8.2, 3.3). Publishing beyond loopback is then accepted, in either
    # option order (testcontainers.md 8.4, 9.5).
    u = UnderstudyContainer(image())
    if order == "publish-then-token":
        u = u.with_published_on("0.0.0.0").with_admin_token()
    else:
        u = u.with_admin_token().with_published_on("0.0.0.0")
    with u:
        env = container_env(u)
        token = env["UNDERSTUDY_ADMIN_TOKEN"]
        assert re.fullmatch(r"[0-9a-f]{64}", token), token
        assert env["UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API"] == "true"
        assert attrs(u)["HostConfig"]["PortBindings"]["4566/tcp"] == [{"HostIp": "0.0.0.0", "HostPort": "0"}]
        assert admin_call(u, "GET", "/health").status == 401
        health = admin_call(u, "GET", "/health", token=token)
        assert health.status == 200 and health.body["ready"] is True
        u.settle(5)
        u.advance_clock(timedelta(seconds=1))
        u.reset()


def test_tokens_are_fresh_per_container() -> None:
    with UnderstudyContainer(image()).with_admin_token() as a, UnderstudyContainer(image()).with_admin_token() as b:
        assert container_env(a)["UNDERSTUDY_ADMIN_TOKEN"] != container_env(b)["UNDERSTUDY_ADMIN_TOKEN"]


def test_snapshot_is_loaded_at_start(tmp_path: Path) -> None:
    # The archive is copied in and loaded at start (testcontainers.md 8.2).
    archive = tmp_path / "state.tar"
    with UnderstudyContainer(image()) as src:
        client(src, "sqs").create_queue(QueueName="from-snapshot")
        saved = admin_call(src, "POST", "/snapshots")
        assert saved.status == 200
        archive.write_bytes(saved.raw)
    with UnderstudyContainer(image()).with_snapshot(archive) as dst:
        assert container_env(dst)["UNDERSTUDY_LOAD_SNAPSHOT"] == "/snapshot/archive"
        urls = client(dst, "sqs").list_queues().get("QueueUrls", [])
        assert len(urls) == 1 and urls[0].endswith("/from-snapshot")


def test_start_fails_fast_when_understudy_exits() -> None:
    # understudy refuses a frozen clock with a scale itself; the module does not re-validate
    # it, and the status check on every poll surfaces the exit with the container's log well
    # before the 60 s startup timeout (testcontainers.md 9.5, 8.3).
    u = UnderstudyContainer(image()).with_clock(frozen_at=FROZEN, scale=60)
    start = time.monotonic()
    try:
        with pytest.raises(ContainerStartException) as raised:
            u.start()
    finally:
        # The container may already be gone; stopping is only cleanup here.
        with contextlib.suppress(Exception):
            u.stop()
    assert time.monotonic() - start < 30
    assert "UNDERSTUDY_CLOCK" in str(raised.value)


# --- Docker socket ---


def test_lambda_runs_with_the_docker_socket() -> None:
    # The socket is a bind mount, never Binds, of the path tc-python's Ryuk uses, with the
    # socket's group added, and the post-start check found Docker enabled
    # (testcontainers.md 8.5, 3.4).
    gid = docker_socket_gid()
    with UnderstudyContainer(image()).with_docker_socket() as u:
        host = attrs(u)["HostConfig"]
        socket = testcontainers_config.ryuk_docker_socket
        assert any(
            m.get("Type") == "bind"
            and m.get("Source") == socket
            and m.get("Target") == "/var/run/docker.sock"
            and not m.get("ReadOnly")
            for m in host.get("Mounts") or []
        ), host.get("Mounts")
        assert not any("docker.sock" in b for b in host.get("Binds") or [])
        assert str(gid) in (host.get("GroupAdd") or [])
        health = admin_call(u, "GET", "/health").body
        assert health["docker"]["status"] == "enabled", health
        invoke_echo(u)


def test_an_unusable_docker_socket_fails_the_start() -> None:
    # A socket understudy cannot use fails the start with understudy's status and reason,
    # and the container is stopped and removed (testcontainers.md 8.5, 9.5, 9.7). A group
    # that does not own the socket leaves the image's non-root user without access.
    wrong = docker_socket_gid() + 1
    before = len(image_containers())
    u = UnderstudyContainer(image()).with_docker_socket(gid=wrong)
    try:
        with pytest.raises(UnderstudyDockerError) as raised:
            u.start()
    except BaseException:
        # A start that did not fail as it should leaves a container to remove.
        with contextlib.suppress(Exception):
            u.stop()
        raise
    e = raised.value
    assert e.status != "enabled"
    assert str(e) == (
        f"understudy: the Docker socket is mounted but understudy reports Docker {e.status}: {e.reason}; "
        "Lambda and DynamoDB will not run. Check the socket group (with_docker_socket(gid=...)) "
        "or run without with_docker_socket()."
    )
    assert len(image_containers()) == before


def test_teardown_leaves_no_child_container() -> None:
    # After the context manager's stop, no container understudy started for this session is
    # left: the children carry the session label, and the stop's grace lets understudy drain
    # them (testcontainers.md 8.6, 8.7, 9.3; findings 3 and 4).
    try:
        with UnderstudyContainer(image()).with_docker_socket() as u:
            invoke_echo(u)
            kids = managed_session_containers()
            assert kids, (
                "no understudy child carries this session's label: the child labels did not reach its containers"
            )
            assert all(k.labels.get(LABEL_SESSION_ID) == SESSION_ID for k in kids)
        left = managed_session_containers()
        assert left == [], [f"{k.short_id} ({k.status})" for k in left]
    finally:
        for k in managed_session_containers():
            k.remove(force=True, v=True)
