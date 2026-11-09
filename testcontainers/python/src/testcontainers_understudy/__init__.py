"""Testcontainers module for understudy, the local AWS emulator.

``UnderstudyContainer`` publishes port 4566 on 127.0.0.1 only, waits for
``GET /_understudy/v1/health`` to answer 200 with ``"ready": true``, labels
every container understudy starts with the session's Ryuk filter label, and
stops with a 10 s grace so understudy removes its own containers first
(testcontainers.md 8).
"""

from __future__ import annotations

import ipaddress
import json
import os
import secrets
import stat
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

from docker.errors import NotFound
from docker.transport import UnixHTTPAdapter
from docker.types import Mount
from testcontainers.core.config import testcontainers_config
from testcontainers.core.container import DockerContainer
from testcontainers.core.docker_client import get_docker_host
from testcontainers.core.exceptions import ContainerStartException
from testcontainers.core.labels import LABEL_SESSION_ID, SESSION_ID
from testcontainers.core.wait_strategies import HttpWaitStrategy
from testcontainers.core.waiting_utils import WaitStrategyTarget

__all__ = [
    "DEFAULT_IMAGE",
    "UnderstudyAdminError",
    "UnderstudyConfigError",
    "UnderstudyContainer",
    "UnderstudyDockerError",
    "__version__",
]

__version__ = "0.1.0"

# The release image of the understudy version this package is released
# with, in lockstep (testcontainers.md 8.8).
DEFAULT_IMAGE = f"ghcr.io/sustinbebustin/understudy:{__version__}"

# understudy's listener in the container (testcontainers.md 3.2).
_API_PORT = 4566
_API_PORT_KEY = f"{_API_PORT}/tcp"
_ADMIN_PREFIX = "/_understudy/v1"
# The wait's bound, though understudy is ready in milliseconds: it bounds only
# image pulls and failures. tc-py's own default is 120 s (testcontainers.md 8.3).
_STARTUP_TIMEOUT_SECONDS = 60
_POLL_INTERVAL_SECONDS = 0.1
# tc-py's stop is a force remove with no drain; understudy needs the grace to
# remove its own containers (testcontainers.md 8.6, finding 4).
_STOP_GRACE_SECONDS = 10
# How long an admin call may take beyond the time it asks understudy to wait.
_ADMIN_CALL_SECONDS = 30.0
_LOOPBACK = ipaddress.ip_address("127.0.0.1")
_SOCKET_TARGET = "/var/run/docker.sock"
_SNAPSHOT_PATH = "/snapshot/archive"


class UnderstudyConfigError(ValueError):
    """A module setting refused before any container is created (testcontainers.md 9.5)."""


class UnderstudyDockerError(RuntimeError):
    """A mounted Docker socket understudy reports it cannot use after start (testcontainers.md 8.5)."""

    def __init__(self, status: str, reason: str) -> None:
        self.status = status
        self.reason = reason
        super().__init__(
            f"understudy: the Docker socket is mounted but understudy reports Docker {status}: {reason}; "
            "Lambda and DynamoDB will not run. Check the socket group (with_docker_socket(gid=...)) "
            "or run without with_docker_socket()."
        )


class UnderstudyAdminError(RuntimeError):
    """An admin API call understudy answered outside 2xx, with its error body."""

    def __init__(self, status: int, code: str, message: str, hint: str = "") -> None:
        self.status = status
        self.code = code
        self.message = message
        self.hint = hint
        text = f"understudy: admin API answered {status} {code}: {message}"
        super().__init__(f"{text}; {hint}" if hint else text)


@dataclass(frozen=True)
class _DockerSocket:
    """A with_docker_socket request: the group to add, or None to read the local socket's."""

    gid: int | None


@dataclass(frozen=True)
class _SocketMount:
    """A resolved with_docker_socket request: the daemon-side socket path and its numeric group."""

    source: str
    gid: int


class UnderstudyContainer(DockerContainer):
    """understudy in a container, configured by the with_* methods below.

    ``start()`` refuses, before creating anything, publishing beyond loopback
    without ``with_admin_token()`` and a Docker socket whose group it cannot
    read; both raise UnderstudyConfigError. Argument shapes are checked when
    each option is called, so option order never changes the outcome
    (testcontainers.md 9.5).
    """

    def __init__(self, image: str = DEFAULT_IMAGE, **kwargs: Any) -> None:
        super().__init__(image=image, **kwargs)
        self._token: str | None = None
        self._published_on: ipaddress.IPv4Address | ipaddress.IPv6Address = _LOOPBACK
        self._socket: _DockerSocket | None = None
        # Set by start() once the socket request is resolved, for _configure().
        self._mount: _SocketMount | None = None

    def with_docker_socket(self, gid: int | None = None) -> UnderstudyContainer:
        """Mount the daemon's Docker socket at /var/run/docker.sock and add its numeric group.

        Lambda and DynamoDB Local need it (testcontainers.md 8.5). Without
        ``gid``, start() reads the group of the local unix socket the Docker
        client uses, and refuses any other daemon. A group name cannot be
        given: the image has no /etc/group to resolve it. A later call
        without ``gid`` keeps a group given earlier. After start, a socket
        understudy does not report enabled raises UnderstudyDockerError.
        """
        if gid is not None and (isinstance(gid, bool) or not isinstance(gid, int) or gid < 0):
            msg = f"understudy: the Docker socket group must be a non-negative integer group ID, not {gid!r}."
            raise UnderstudyConfigError(msg)
        if gid is not None or self._socket is None:
            self._socket = _DockerSocket(gid)
        return self

    def with_clock(self, *, frozen_at: datetime | None = None, scale: float | None = None) -> UnderstudyContainer:
        """Freeze the emulator clock at ``frozen_at``, or scale it by ``scale``.

        Sets UNDERSTUDY_CLOCK=frozen@<RFC 3339> and UNDERSTUDY_CLOCK_SCALE
        (testcontainers.md 8.2). understudy checks the scale's range and
        refuses a frozen clock with a scale itself; start() then raises with
        the container's log (testcontainers.md 9.5).
        """
        if frozen_at is not None and frozen_at.utcoffset() is None:
            msg = (
                "understudy: frozen_at must be a timezone-aware datetime, "
                "such as datetime(2026, 1, 1, tzinfo=timezone.utc)."
            )
            raise UnderstudyConfigError(msg)
        if frozen_at is not None:
            self.with_env("UNDERSTUDY_CLOCK", "frozen@" + frozen_at.isoformat())
        if scale is not None:
            self.with_env("UNDERSTUDY_CLOCK_SCALE", str(scale))
        return self

    def with_seed(self, seed: int) -> UnderstudyContainer:
        """Set UNDERSTUDY_SEED; understudy checks its range itself (testcontainers.md 9.5)."""
        self.with_env("UNDERSTUDY_SEED", str(seed))
        return self

    def with_sigv4_verify(self) -> UnderstudyContainer:
        """Set UNDERSTUDY_SIGV4=verify, so understudy checks every request signature."""
        self.with_env("UNDERSTUDY_SIGV4", "verify")
        return self

    def with_snapshot(self, path: str | os.PathLike[str]) -> UnderstudyContainer:
        """Load the snapshot archive at ``path`` at start.

        The file is copied to /snapshot/archive in the container, mode 0644,
        and UNDERSTUDY_LOAD_SNAPSHOT names it (testcontainers.md 8.2). A
        missing file raises FileNotFoundError here, before any container.
        """
        archive = Path(path).absolute()
        if stat.S_ISDIR(archive.stat().st_mode):
            msg = (
                f"understudy: with_snapshot({os.fspath(path)!r}) names a directory; "
                "pass the archive file, such as one POST /_understudy/v1/snapshots answered with."
            )
            raise UnderstudyConfigError(msg)
        self._transferable_specs = [spec for spec in self._transferable_specs if spec[1] != _SNAPSHOT_PATH]
        self.with_copy_into_container(archive, _SNAPSHOT_PATH, 0o644)
        self.with_env("UNDERSTUDY_LOAD_SNAPSHOT", _SNAPSHOT_PATH)
        return self

    def with_admin_token(self) -> UnderstudyContainer:
        """Guard the admin API with a random token the wait and the helpers send.

        Sets UNDERSTUDY_ADMIN_TOKEN to 64 hex characters from 32 random bytes
        and UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API=true, which understudy
        requires beside the token; health then answers 401 without it
        (testcontainers.md 8.2, 3.3).
        """
        self._token = secrets.token_hex(32)
        self.with_env("UNDERSTUDY_ADMIN_TOKEN", self._token)
        self.with_env("UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API", "true")
        return self

    def with_published_on(self, addr: str) -> UnderstudyContainer:
        """Publish port 4566 on ``addr`` instead of 127.0.0.1.

        An address beyond loopback, 0.0.0.0 and :: included, needs
        with_admin_token(), or start() refuses it (testcontainers.md 8.4).
        Any other loopback address is refused here: the endpoint and the
        wait dial the daemon's host, 127.0.0.1 for a local daemon, so they
        could never reach it (testcontainers.md 8.2, 9.5).
        """
        try:
            published_on = ipaddress.ip_address(addr)
        except ValueError:
            msg = f"understudy: with_published_on({addr!r}) is not an IP address; pass one such as 127.0.0.1."
            raise UnderstudyConfigError(msg) from None
        if published_on.is_loopback and published_on != _LOOPBACK:
            msg = (
                f"understudy: with_published_on({addr!r}) is a loopback address the endpoint never dials: the "
                "endpoint and the readiness wait reach port 4566 at the Docker host's address, 127.0.0.1 for a "
                "local daemon; publish on 127.0.0.1, or on 0.0.0.0 with with_admin_token()."
            )
            raise UnderstudyConfigError(msg)
        self._published_on = published_on
        return self

    def start(self) -> UnderstudyContainer:
        """Start understudy and return once it reports ready (testcontainers.md 8.3).

        A container that exits first raises ContainerStartException with its
        log. With the Docker socket, a socket understudy does not report
        enabled raises UnderstudyDockerError. Whatever fails once the
        container exists, start() stops and removes it before raising, so an
        explicit start() outside a with block leaves nothing behind.
        """
        self._check_exposure()
        self._mount = self._resolve_socket()
        try:
            super().start()
            if self._mount is not None:
                self._check_docker()
        except BaseException:
            self.stop()
            raise
        return self

    def stop(self, force: bool = True, delete_volume: bool = True) -> None:
        """Stop with a 10 s grace, then remove the container and its volumes.

        The grace lets understudy remove the containers it started before it
        exits (testcontainers.md 8.6). A container already gone counts as
        removed.
        """
        container = self._container
        if container is not None:
            try:
                container.stop(timeout=_STOP_GRACE_SECONDS)
                container.remove(force=force, v=delete_volume)
            except NotFound:
                pass
            self._container = None
        self.get_docker_client().client.close()

    def get_endpoint(self) -> str:
        """``http://<host>:<mapped 4566>``, with localhost as 127.0.0.1 (testcontainers.md 8.2)."""
        return _endpoint_url(self.get_container_host_ip(), self.get_exposed_port(_API_PORT))

    def get_env(self) -> dict[str, str]:
        """The environment that points an SDK at the container, as understudytest.Env (testcontainers.md 3.8)."""
        return {
            "AWS_ENDPOINT_URL": self.get_endpoint(),
            "AWS_ACCESS_KEY_ID": "test",
            "AWS_SECRET_ACCESS_KEY": "test",
            "AWS_REGION": "us-east-1",
            "AWS_DEFAULT_REGION": "us-east-1",
        }

    def settle(self, timeout: float) -> None:
        """Wait up to ``timeout`` seconds for understudy's tracked work to finish.

        Sent as whole milliseconds to GET /_understudy/v1/settle
        (testcontainers.md 3.7); understudy answers 412 condition_not_met on
        timeout (admin.ConditionNotMet in admin/errors.go).
        """
        self._call("GET", f"/settle?timeout={int(timeout * 1000)}ms", timeout=max(timeout, 0) + _ADMIN_CALL_SECONDS)

    def advance_clock(self, by: timedelta) -> None:
        """Move the emulator clock forward by ``by``, sent in whole microseconds (testcontainers.md 3.7)."""
        self._call("POST", "/clock", {"op": "advance", "by": f"{by // timedelta(microseconds=1)}us"})

    def reset(self) -> None:
        """Drop all service state, keeping the clock and the fault rules (testcontainers.md 3.7)."""
        self._call("POST", "/reset", {})

    def _configure(self) -> None:
        # The module owns port 4566's binding; a bare "4566" key from ports=
        # or with_exposed_ports would otherwise publish it on every interface.
        self.ports.pop(str(_API_PORT), None)
        # SAFETY: docker-py binds a (host_ip, host_port) tuple as HostIp and
        # HostPort, and tc-py passes self.ports to it unchanged, but annotates
        # the values as Optional[int] (testcontainers.md 5.3, 8.4).
        self.ports[_API_PORT_KEY] = (str(self._published_on), 0)  # type: ignore[assignment]
        self._wait_strategy = _ReadyWait(self._token)
        # The session's Ryuk filter label goes on every container understudy
        # starts, so Ryuk reaps them too; a value the user set wins
        # (testcontainers.md 8.6, 5.4, 9.3).
        if "UNDERSTUDY_DOCKER_LABELS" not in self.env:
            self.env["UNDERSTUDY_DOCKER_LABELS"] = json.dumps({LABEL_SESSION_ID: SESSION_ID})
        if self._mount is not None:
            self._kwargs = {**self._kwargs, **self._mount_kwargs(self._mount)}

    def _mount_kwargs(self, mount: _SocketMount) -> dict[str, list[Any]]:
        """The user's mounts and groups with the socket's appended, never replaced.

        A bind mount, unlike a volume binding, fails on a missing source
        instead of creating a root-owned directory (testcontainers.md 3.4).
        """
        socket = Mount(target=_SOCKET_TARGET, source=mount.source, type="bind")
        mounts = list(self._kwargs.get("mounts") or [])
        if socket not in mounts:
            mounts.append(socket)
        groups = list(self._kwargs.get("group_add") or [])
        if str(mount.gid) not in [str(g) for g in groups]:
            groups.append(str(mount.gid))
        return {"mounts": mounts, "group_add": groups}

    def _check_exposure(self) -> None:
        """Refuse publishing beyond loopback without the token (testcontainers.md 8.4, 9.5)."""
        if not self._published_on.is_loopback and self._token is None:
            msg = (
                f"understudy: publishing port 4566 on {self._published_on} exposes the admin API, which has no "
                "token in the container; add with_admin_token() or publish on 127.0.0.1."
            )
            raise UnderstudyConfigError(msg)

    def _resolve_socket(self) -> _SocketMount | None:
        """The socket to mount and its group, or None without with_docker_socket (testcontainers.md 8.5).

        The source is the path tc-py mounts into Ryuk. The group is the one
        given, else 0 on Docker Desktop, whose VM socket is root:root 0660,
        else the st_gid of the local unix socket the client uses.
        """
        if self._socket is None:
            return None
        gid = self._socket.gid
        if gid is None:
            gid = self._local_socket_gid()
        return _SocketMount(source=testcontainers_config.ryuk_docker_socket, gid=gid)

    def _local_socket_gid(self) -> int:
        api = self.get_docker_client().client.api
        adapter = api.get_adapter(api.base_url)
        if not isinstance(adapter, UnixHTTPAdapter):
            daemon = get_docker_host() or api.base_url
            msg = (
                f"understudy: cannot read the group of the Docker socket on {daemon}, which is not a local unix "
                "socket; pass with_docker_socket(gid=<gid>), the numeric group owning /var/run/docker.sock on the "
                "daemon's host."
            )
            raise UnderstudyConfigError(msg)
        # Docker Desktop mounts its VM's socket, not the host's (TC-01).
        if "Docker Desktop" in self.get_docker_client().client.info().get("OperatingSystem", ""):
            return 0
        try:
            return os.stat(adapter.socket_path).st_gid
        except OSError as e:
            msg = (
                f"understudy: cannot read the group of the Docker socket {adapter.socket_path}: {e.strerror}; "
                "pass with_docker_socket(gid=<gid>), the numeric group owning the socket."
            )
            raise UnderstudyConfigError(msg) from e

    def _check_docker(self) -> None:
        """Raise unless understudy reports Docker enabled: a mounted socket never fails silently.

        (testcontainers.md 8.5, 9.7)
        """
        status, reason = _docker_state(self._call("GET", "/health"))
        if status != "enabled":
            raise UnderstudyDockerError(status, reason)

    def _call(
        self, method: str, path: str, body: dict[str, str] | None = None, timeout: float = _ADMIN_CALL_SECONDS
    ) -> object:
        """One admin API call, with the token when set; the decoded JSON answer, or None when empty."""
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.get_endpoint() + _ADMIN_PREFIX + path, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if self._token is not None:
            req.add_header("Authorization", f"Bearer {self._token}")
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as e:
            raise _admin_error(e.code, e.read()) from None
        return json.loads(raw) if raw else None


class _ReadyWait(HttpWaitStrategy):
    """GET /_understudy/v1/health until it answers 200 with "ready": true (testcontainers.md 8.3).

    Unlike HttpWaitStrategy, it checks the container on every poll, so an
    understudy that refuses its settings and exits fails the start at once,
    with its log, instead of after the timeout (testcontainers.md 5.2, 9.5).
    Never the image HEALTHCHECK, an open port or a sleep (testcontainers.md 9.7).
    """

    def __init__(self, token: str | None) -> None:
        super().__init__(_API_PORT, _ADMIN_PREFIX + "/health")
        self.for_response_predicate(_reports_ready)
        self.with_poll_interval(_POLL_INTERVAL_SECONDS)
        self.with_startup_timeout(_STARTUP_TIMEOUT_SECONDS)
        if token is not None:
            self.with_header("Authorization", f"Bearer {token}")

    def wait_until_ready(self, container: WaitStrategyTarget) -> None:
        deadline = time.monotonic() + self._startup_timeout
        headers = self._setup_headers()
        url: str | None = None
        while True:
            _raise_if_exited(container)
            if url is None:
                url = self._health_url(container)
            if url is not None and self._try_http_request(url, headers, None):
                return
            if time.monotonic() >= deadline:
                msg = (
                    f"understudy: not ready within {self._startup_timeout:g} s: GET {url or self._path} never "
                    'answered 200 with "ready": true. start() removes the container; tc-py attaches its log to '
                    "this error as a note (Python 3.11 and later)."
                )
                raise TimeoutError(msg)
            time.sleep(self._poll_interval)

    def _health_url(self, container: WaitStrategyTarget) -> str | None:
        """The health URL, or None while the port mapping is not readable yet."""
        try:
            host = container.get_container_host_ip()
            port = container.get_exposed_port(self._port)
        except (TimeoutError, ConnectionError):
            # The container left the running state between the exit check and
            # the port lookup; the next poll's exit check reports it.
            return None
        return _endpoint_url(host, port) + self._path


def _raise_if_exited(container: WaitStrategyTarget) -> None:
    """Raise ContainerStartException, with the container's log, once it is no longer running."""
    wrapped = container.get_wrapped_container()
    wrapped.reload()
    if wrapped.status in ("created", "running"):
        return
    code = wrapped.attrs.get("State", {}).get("ExitCode", "unknown")
    log = wrapped.logs().decode(errors="replace")
    msg = (
        f"understudy: the container stopped ({wrapped.status}, exit code {code}) before it reported ready; "
        f"understudy refused its settings or could not start. Its log:\n{log}"
    )
    raise ContainerStartException(msg)


def _reports_ready(body: str) -> bool:
    try:
        health = json.loads(body)
    except ValueError:
        return False
    return isinstance(health, dict) and health.get("ready") is True


def _docker_state(health: object) -> tuple[str, str]:
    """Docker's status and reason in a health answer (testcontainers.md 3.1)."""
    docker = health.get("docker") if isinstance(health, dict) else None
    if not isinstance(docker, dict):
        return "unreported", "the health answer has no docker field"
    return str(docker.get("status", "")), str(docker.get("reason", ""))


def _endpoint_url(host: str, port: int) -> str:
    """Join a daemon host, as tc-py reports it, and a port; an IPv6 host is bracketed."""
    host = host.removeprefix("[").removesuffix("]")
    if host == "localhost":
        # SDKs address S3 path style only at an IP endpoint (testcontainers.md 8.2).
        host = str(_LOOPBACK)
    if ":" in host:
        host = f"[{host}]"
    return f"http://{host}:{port}"


def _admin_error(status: int, raw: bytes) -> UnderstudyAdminError:
    """The error for a non-2xx admin answer: its envelope's fields, or the body text when it has none.

    The envelope is {"error": {"code", "message", "hint"}} (ARCHITECTURE.md 17).
    """
    text = raw.decode(errors="replace")
    try:
        doc = json.loads(text)
    except ValueError:
        doc = None
    err = doc.get("error") if isinstance(doc, dict) else None
    if isinstance(err, dict) and isinstance(err.get("code"), str) and isinstance(err.get("message"), str):
        hint = err.get("hint")
        return UnderstudyAdminError(status, err["code"], err["message"], hint if isinstance(hint, str) else "")
    return UnderstudyAdminError(status, "", text)
