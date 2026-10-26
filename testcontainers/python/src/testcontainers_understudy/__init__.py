"""Testcontainers module for understudy, the local AWS emulator.

``UnderstudyContainer`` publishes port 4566 on 127.0.0.1 only, waits for
``GET /_understudy/v1/health`` to answer 200 with ``"ready": true``, labels
every container understudy starts with the session's Ryuk filter label, and
stops with a 10 s grace so understudy removes its own containers first
(testcontainers.md 8).

The module's methods are not implemented yet: each raises NotImplementedError.
"""

from __future__ import annotations

import os
from datetime import datetime, timedelta
from typing import Any

from testcontainers.core.container import DockerContainer

__all__ = [
    "DEFAULT_IMAGE",
    "UnderstudyAdminError",
    "UnderstudyConfigError",
    "UnderstudyContainer",
    "UnderstudyDockerError",
    "__version__",
]

__version__ = "0.0.0"

# The release image of the understudy version this package is released
# with, in lockstep (testcontainers.md 8.8).
DEFAULT_IMAGE = f"ghcr.io/sustinbebustin/understudy:{__version__}"

_NOT_IMPLEMENTED = "understudy: not implemented yet"


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


class UnderstudyContainer(DockerContainer):
    """understudy in a container, configured by the with_* methods below."""

    def __init__(self, image: str = DEFAULT_IMAGE, **kwargs: Any) -> None:
        super().__init__(image=image, **kwargs)

    def with_docker_socket(self, gid: int | None = None) -> UnderstudyContainer:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def with_clock(self, *, frozen_at: datetime | None = None, scale: float | None = None) -> UnderstudyContainer:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def with_seed(self, seed: int) -> UnderstudyContainer:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def with_sigv4_verify(self) -> UnderstudyContainer:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def with_snapshot(self, path: str | os.PathLike[str]) -> UnderstudyContainer:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def with_admin_token(self) -> UnderstudyContainer:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def with_published_on(self, addr: str) -> UnderstudyContainer:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def start(self) -> UnderstudyContainer:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def stop(self, force: bool = True, delete_volume: bool = True) -> None:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def get_endpoint(self) -> str:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def get_env(self) -> dict[str, str]:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def settle(self, timeout: float) -> None:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def advance_clock(self, by: timedelta) -> None:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def reset(self) -> None:
        raise NotImplementedError(_NOT_IMPLEMENTED)
