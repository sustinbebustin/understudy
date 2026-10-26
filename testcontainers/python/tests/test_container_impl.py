"""Module behavior beyond test_container.py and test_validation.py.

The socket refusal on a daemon that is not a local unix socket, the user's
own mounts and groups beside the socket's, the binding of a 4566 the user
exposed, the stop's grace, and a snapshot path naming a directory
(testcontainers.md 8.4, 8.5, 8.6, 9.5).
"""

from __future__ import annotations

import time
from pathlib import Path

import pytest
from docker.types import Mount

from support import admin_call, attrs, docker_client, docker_socket_gid, image
from testcontainers_understudy import UnderstudyConfigError, UnderstudyContainer


def test_socket_group_is_refused_on_a_tcp_daemon(monkeypatch: pytest.MonkeyPatch) -> None:
    # A group can be stat'ed only on a local unix socket; on any other daemon
    # start() refuses before any Docker call, so the unreachable daemon here is
    # never dialed (testcontainers.md 8.5, 9.5). A pinned API version keeps the
    # client from probing the daemon when it is built.
    monkeypatch.setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
    u = UnderstudyContainer(image(), docker_client_kw={"version": "1.44"}).with_docker_socket()
    with pytest.raises(UnderstudyConfigError) as raised:
        u.start()
    assert str(raised.value) == (
        "understudy: cannot read the group of the Docker socket on tcp://127.0.0.1:1, which is not a local unix "
        "socket; pass with_docker_socket(gid=<gid>), the numeric group owning /var/run/docker.sock on the "
        "daemon's host."
    )


def test_exposure_is_checked_before_the_socket_group(monkeypatch: pytest.MonkeyPatch) -> None:
    # Both refusals apply; the exposure text comes first, as in the Go module.
    monkeypatch.setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
    u = UnderstudyContainer(image(), docker_client_kw={"version": "1.44"})
    with pytest.raises(UnderstudyConfigError, match="^understudy: publishing port 4566 on 0.0.0.0 "):
        u.with_docker_socket().with_published_on("0.0.0.0").start()


def test_socket_keeps_the_users_mounts_and_groups() -> None:
    # The socket's bind mount and group are appended to the user's, never
    # replace them, whatever the call order (testcontainers.md 8.5).
    scratch = Mount(target="/scratch", source="", type="tmpfs")
    u = UnderstudyContainer(image()).with_docker_socket().with_kwargs(mounts=[scratch], group_add=["12345"])
    with u:
        host = attrs(u)["HostConfig"]
        targets = sorted(m["Target"] for m in host["Mounts"])
        assert targets == ["/scratch", "/var/run/docker.sock"], host["Mounts"]
        assert sorted(host["GroupAdd"]) == sorted(["12345", str(docker_socket_gid())])
        assert admin_call(u, "GET", "/health").body["docker"]["status"] == "enabled"


@pytest.mark.parametrize("ports", [[4566], ["4566/tcp", 4566]], ids=["bare", "bare-after-tcp"])
def test_an_exposed_4566_stays_on_loopback(ports: list[int | str]) -> None:
    # docker-py turns a bare 4566 into 4566/tcp with no host IP, every
    # interface, and the later of the two keys wins; the module owns that port's
    # binding, so exposure never widens silently (testcontainers.md 8.4, 9.7).
    with UnderstudyContainer(image(), ports=ports) as u:
        assert attrs(u)["HostConfig"]["PortBindings"] == {"4566/tcp": [{"HostIp": "127.0.0.1", "HostPort": "0"}]}


def test_stop_lets_understudy_shut_down() -> None:
    # tc-py's own stop is a force remove, SIGKILL with no drain; the module stops
    # with a grace first, so understudy handles SIGTERM, drains and exits 0
    # rather than 137 (testcontainers.md 8.6, finding 4).
    since = int(time.time())
    u = UnderstudyContainer(image())
    u.start()
    cid = u.get_wrapped_container().id
    u.stop()
    # The die event of a synchronous stop is in the past; until bounds the read.
    events = docker_client().events(
        since=since, until=int(time.time()) + 1, filters={"container": cid, "event": "die"}, decode=True
    )
    codes = [e["Actor"]["Attributes"]["exitCode"] for e in events]
    assert codes == ["0"]


def test_stop_twice_and_before_start_is_harmless() -> None:
    UnderstudyContainer(image()).stop()
    u = UnderstudyContainer(image())
    u.start()
    u.stop()
    u.stop()


def test_snapshot_directory_is_refused(tmp_path: Path) -> None:
    # The archive is one file copied to /snapshot/archive (testcontainers.md 8.2).
    with pytest.raises(UnderstudyConfigError) as raised:
        UnderstudyContainer(image()).with_snapshot(tmp_path)
    assert str(raised.value) == (
        f"understudy: with_snapshot({str(tmp_path)!r}) names a directory; "
        "pass the archive file, such as one POST /_understudy/v1/snapshots answered with."
    )
