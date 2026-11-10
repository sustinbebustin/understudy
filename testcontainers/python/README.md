# testcontainers-understudy

Runs [understudy](https://github.com/sustinbebustin/understudy), the local AWS emulator, as a [Testcontainers for Python](https://testcontainers-python.readthedocs.io/) module.

It is released with understudy: version `X.Y.Z` of this package defaults to the image `ghcr.io/sustinbebustin/understudy:X.Y.Z`.

```sh
uv add --dev testcontainers-understudy    # or: pip install testcontainers-understudy
```

It needs Python 3.10 or later and `testcontainers>=4.15,<5`.

## Use

```python
import boto3
import pytest
from testcontainers_understudy import UnderstudyContainer


@pytest.fixture(scope="session")
def understudy():
    with UnderstudyContainer() as u:
        yield u


@pytest.fixture(autouse=True)
def aws_env(understudy, monkeypatch):
    for name, value in understudy.get_env().items():  # AWS_ENDPOINT_URL, test credentials, us-east-1
        monkeypatch.setenv(name, value)
    yield
    understudy.reset()


def test_orders_queue():
    sqs = boto3.client("sqs")
    url = sqs.create_queue(QueueName="orders")["QueueUrl"]
    sqs.send_message(QueueUrl=url, MessageBody="order-1")
    assert [m["Body"] for m in sqs.receive_message(QueueUrl=url)["Messages"]] == ["order-1"]
```

`start()` publishes port 4566 on `127.0.0.1` only and returns once `GET /_understudy/v1/health` answers 200 with `"ready": true`; if understudy exits first, it raises with understudy's log. A `start()` that raises once the container exists has already stopped and removed it. `stop()`, and leaving the `with` block, stops understudy with a 10-second grace period, which lets it remove the Lambda and DynamoDB Local containers it started, and every such container carries the session's label, so Ryuk removes them too.

## Options

| Method | Effect |
|---|---|
| `with_docker_socket()` | mounts the Docker socket and adds its group (0 on Docker Desktop, whose VM owns the socket), for Lambda and DynamoDB Local; `start()` raises `UnderstudyDockerError` unless understudy then reports Docker enabled |
| `with_docker_socket(gid=n)` | the same, with the group given, for a daemon that is not a local unix socket |
| `with_clock(frozen_at=dt, scale=f)` | the emulator clock; `frozen_at` must be timezone-aware |
| `with_seed(n)` | repeatable generated identifiers |
| `with_sigv4_verify()` | verify request signatures |
| `with_snapshot(path)` | load a snapshot archive at start |
| `with_admin_token()` | a random admin token, sent by the wait and the helpers |
| `with_published_on(addr)` | publish on another address; beyond loopback only with `with_admin_token()`; a loopback address other than `127.0.0.1` is refused, since the endpoint never dials it |

`with_env` sets any other `UNDERSTUDY_*` setting. `settle(timeout)`, `advance_clock(by)` and `reset()` wrap the common admin calls and raise `UnderstudyAdminError` on an error answer. Misconfigurations raise `UnderstudyConfigError`, before any container is created. The exception is a missing snapshot archive: `with_snapshot` raises `FileNotFoundError` for it.

The [Testcontainers guide](https://github.com/sustinbebustin/understudy/blob/main/docs/guide/testcontainers-modules.md) covers Lambda, Terraform and cleanup in more detail.

## Development

```sh
just image
testcontainers/python/run.sh    # uv sync, ruff check and format check, sealed pytest against understudy:dev
```

`UNDERSTUDY_IMAGE` selects another image; extra arguments go to pytest.
