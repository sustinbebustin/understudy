"""What the module refuses before creating anything, its version and its texts.

A misconfiguration fails at once rather than after the startup timeout, with the texts
of testcontainers.md 9.5 in Python's option names.
"""

from __future__ import annotations

import importlib.metadata
from datetime import datetime
from pathlib import Path

import pytest

import testcontainers_understudy
from support import image, image_containers
from testcontainers_understudy import (
    DEFAULT_IMAGE,
    UnderstudyAdminError,
    UnderstudyConfigError,
    UnderstudyContainer,
    UnderstudyDockerError,
    __version__,
)


def test_version_and_default_image() -> None:
    # The release image is ghcr.io/sustinbebustin/understudy tagged with the version
    # (testcontainers.md 3.2); the package moves in lockstep with understudy's releases
    # (testcontainers.md 8.8).
    assert importlib.metadata.version("testcontainers-understudy") == __version__
    assert DEFAULT_IMAGE == "ghcr.io/sustinbebustin/understudy:" + __version__


def test_public_names() -> None:
    # The module's surface (testcontainers.md 8.1, 8.2): the container, the image
    # constant, the version and the errors it raises.
    assert set(testcontainers_understudy.__all__) == {
        "DEFAULT_IMAGE",
        "UnderstudyContainer",
        "UnderstudyConfigError",
        "UnderstudyDockerError",
        "UnderstudyAdminError",
        "__version__",
    }
    assert issubclass(UnderstudyConfigError, ValueError)
    assert issubclass(UnderstudyDockerError, RuntimeError)
    assert issubclass(UnderstudyAdminError, RuntimeError)


def test_docker_text() -> None:
    # testcontainers.md 9.5, with with_docker_socket's names.
    e = UnderstudyDockerError("degraded", "the Docker Engine does not answer")
    assert (e.status, e.reason) == ("degraded", "the Docker Engine does not answer")
    assert str(e) == (
        "understudy: the Docker socket is mounted but understudy reports Docker degraded: "
        "the Docker Engine does not answer; Lambda and DynamoDB will not run. "
        "Check the socket group (with_docker_socket(gid=...)) or run without with_docker_socket()."
    )


def test_admin_error_carries_the_envelope() -> None:
    # The admin API's error envelope is {"error": {"code", "message", "hint"}} (testcontainers.md 3.7).
    e = UnderstudyAdminError(412, "condition_not_met", "not settled after 1s", "raise ?timeout=")
    assert (e.status, e.code, e.message, e.hint) == (
        412,
        "condition_not_met",
        "not settled after 1s",
        "raise ?timeout=",
    )
    assert UnderstudyAdminError(500, "", "boom").hint == ""


@pytest.mark.parametrize("gid", [True, False, -1, 1.5, 2.0, "988"])
def test_socket_group_must_be_a_non_negative_integer(gid: object) -> None:
    # A group name cannot resolve in a scratch image, so only a numeric GID works
    # (testcontainers.md 8.5); argument shapes are checked when the option is called
    # (testcontainers.md 9.5).
    with pytest.raises(UnderstudyConfigError) as raised:
        UnderstudyContainer(image()).with_docker_socket(gid=gid)  # type: ignore[arg-type]
    assert (
        str(raised.value)
        == f"understudy: the Docker socket group must be a non-negative integer group ID, not {gid!r}."
    )


def test_frozen_at_must_be_timezone_aware() -> None:
    # understudy freezes the clock at an RFC 3339 instant (testcontainers.md 8.2), which a
    # naive datetime does not name.
    with pytest.raises(UnderstudyConfigError) as raised:
        UnderstudyContainer(image()).with_clock(frozen_at=datetime(2026, 1, 1))
    assert str(raised.value) == (
        "understudy: frozen_at must be a timezone-aware datetime, such as datetime(2026, 1, 1, tzinfo=timezone.utc)."
    )


@pytest.mark.parametrize("addr", ["localhost", "not-an-ip", "", "127.0.0.1:4566", "256.0.0.1"])
def test_published_on_needs_an_ip_address(addr: str) -> None:
    # The publish address is the binding's HostIp (testcontainers.md 8.2, 8.4).
    with pytest.raises(UnderstudyConfigError) as raised:
        UnderstudyContainer(image()).with_published_on(addr)
    assert (
        str(raised.value)
        == f"understudy: with_published_on({addr!r}) is not an IP address; pass one such as 127.0.0.1."
    )


def test_missing_snapshot_fails_when_applied(tmp_path: Path) -> None:
    # testcontainers.md 8.2: the archive is copied in from the host.
    with pytest.raises(FileNotFoundError):
        UnderstudyContainer(image()).with_snapshot(tmp_path / "absent.tar")


@pytest.mark.parametrize("addr", ["0.0.0.0", "::", "192.0.2.10", "2001:db8::1"])
def test_publishing_beyond_loopback_needs_the_token(addr: str) -> None:
    # The image's admin API has no token, so publishing beyond loopback, every interface
    # included, is refused at start, before any container, whatever the option order
    # (testcontainers.md 8.4, 9.5, 9.7).
    before = len(image_containers())
    want = (
        f"understudy: publishing port 4566 on {addr} exposes the admin API, which has no token "
        "in the container; add with_admin_token() or publish on 127.0.0.1."
    )
    for u in (
        UnderstudyContainer(image()).with_published_on(addr),
        UnderstudyContainer(image()).with_published_on(addr).with_seed(1),
        UnderstudyContainer(image()).with_seed(1).with_published_on(addr),
    ):
        with pytest.raises(UnderstudyConfigError) as raised:
            u.start()
        assert str(raised.value) == want
    assert len(image_containers()) == before
