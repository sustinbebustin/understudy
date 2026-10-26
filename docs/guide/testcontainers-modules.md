# Testcontainers

The modules in [testcontainers/](../../testcontainers/) start the understudy image from a Go, Python or Node test suite on [Testcontainers](https://testcontainers.com/), and remove it when the tests end. Each one:

- publishes port 4566 on `127.0.0.1` only, at a free host port;
- waits until `GET /_understudy/v1/health` answers 200 with `"ready": true`, and fails at once if understudy exits first: Python and Node put understudy's log in the error, and Go reports the exit code and returns the container, whose `Logs` has it;
- returns the endpoint and the environment [`understudytest`](sdks.md#go-tests-with-understudytest) returns: `AWS_ENDPOINT_URL`, `AWS_ACCESS_KEY_ID=test`, `AWS_SECRET_ACCESS_KEY=test`, and `AWS_REGION` and `AWS_DEFAULT_REGION` set to `us-east-1`;
- gives every container understudy starts, Lambda functions and DynamoDB Local, the test session's Testcontainers labels, so the session's reaper removes them too;
- stops understudy with a 10-second grace period, so it removes its own containers before it exits.

Nothing is published yet, neither the packages nor the image `ghcr.io/sustinbebustin/understudy:0.0.0` their image constants name. Build the image with `just image`, pass `understudy:dev`, and install a module from a clone of this repository, here at `../understudy`. Every snippet on this page was run that way against `understudy:dev`, through `scripts/sealed`.

## Go

### `understudytest` first

A Go test does not need a container: [`understudytest`](sdks.md#go-tests-with-understudytest) runs understudy inside the test process, and needs Docker only for Lambda and DynamoDB. Use the module when the suite already runs on testcontainers-go and needs understudy as a container, for example next to an application container on a Docker network.

Both return the same environment, so one test body runs against either:

```go
func TestOrdersInProcess(t *testing.T) {
	emu := understudytest.Start(t, understudytest.Options{Docker: understudytest.DockerDisabled{}})
	testOrders(t, emu.Env())
}

func TestOrdersInContainer(t *testing.T) {
	ctx := t.Context()
	ctr, err := understudy.Run(ctx, "understudy:dev")
	if ctr != nil {
		testcontainers.CleanupContainer(t, ctr.Container)
	}
	if err != nil {
		t.Fatal(err)
	}
	env, err := ctr.Env(ctx)
	if err != nil {
		t.Fatal(err)
	}
	testOrders(t, env)
}

// testOrders runs against whichever understudy env points at.
func testOrders(t *testing.T, env map[string]string) {
	for k, v := range env {
		t.Setenv(k, v)
	}
	ctx := t.Context()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client := sqs.NewFromConfig(cfg)
	q, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("orders")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: q.QueueUrl, MessageBody: aws.String("order-1")}); err != nil {
		t.Fatal(err)
	}
	out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || aws.ToString(out.Messages[0].Body) != "order-1" {
		t.Fatalf("received %v, want one message order-1", out.Messages)
	}
}
```

Here the in-process test took 10 to 20 ms, and the container test 1 to 2.5 s with the image already local.

`understudy.Run` returns the container together with any error that comes after it was created, so register the cleanup before checking the error. Pass `ctr.Container`, the embedded testcontainers-go container, to `CleanupContainer`: the module's `Endpoint(ctx)` replaces testcontainers-go's `Endpoint(ctx, proto)`, so `*understudy.Container` itself is not a `testcontainers.Container`. `ctr.Admin(ctx)` returns an `admin.Client` with the whole admin API; `Settle`, `AdvanceClock` and `Reset` wrap the common calls.

Add the module from the clone:

```sh
go mod edit \
  -replace github.com/sustinbebustin/understudy=../understudy \
  -replace github.com/sustinbebustin/understudy/testcontainers/go=../understudy/testcontainers/go
go get github.com/sustinbebustin/understudy@v0.0.0 github.com/sustinbebustin/understudy/testcontainers/go@v0.0.0
go mod tidy
```

Both replacements are needed, because a `replace` in a dependency's own `go.mod` does not apply to the modules that require it. The module needs Go 1.27, since it imports understudy's `admin` package, and testcontainers-go v0.44.0.

## Python

```sh
uv add --dev ../understudy/testcontainers/python boto3 pytest
```

The package `testcontainers-understudy` needs Python 3.10 or later and `testcontainers>=4.15,<5`. A session fixture starts one container for the run, and an autouse fixture points each test at it and resets understudy's state afterwards:

```python
# tests/conftest.py
import pytest
from testcontainers_understudy import UnderstudyContainer


@pytest.fixture(scope="session")
def understudy():
    with UnderstudyContainer("understudy:dev") as u:
        yield u


@pytest.fixture(autouse=True)
def aws_env(understudy, monkeypatch):
    for name, value in understudy.get_env().items():
        monkeypatch.setenv(name, value)
    yield
    understudy.reset()
```

```python
# tests/test_orders.py
import boto3


def test_orders_queue():
    sqs = boto3.client("sqs")
    url = sqs.create_queue(QueueName="orders")["QueueUrl"]
    sqs.send_message(QueueUrl=url, MessageBody="order-1")
    messages = sqs.receive_message(QueueUrl=url)["Messages"]
    assert [m["Body"] for m in messages] == ["order-1"]


def test_reset_dropped_the_queue():
    assert boto3.client("sqs").list_queues().get("QueueUrls", []) == []
```

`reset()` drops every service's state and keeps the clock and the fault rules, so tests that share a container still start empty. Leaving the `with` block stops understudy with the 10-second grace and removes the container. `settle(timeout)` and `advance_clock(by)` wrap the other common admin calls.

## Node

Build and pack the package in the clone, then install the tarball with the project's own `testcontainers`, a peer dependency (`^12.2.1`):

```sh
(cd ../understudy/testcontainers/node && bun install --frozen-lockfile && bun run build && bun pm pack --destination "$OLDPWD")
bun add -d ./testcontainers-understudy-0.0.0.tgz testcontainers@^12.2.1 @aws-sdk/client-sqs
```

Installed from the tarball, the package uses the project's own copy of `testcontainers`, as it will when installed from the registry. The package is ESM and needs Node 22.22 or later; its tests run on Node, and Bun is untested.

```ts
// orders.test.ts
import assert from "node:assert/strict";
import { after, before, test } from "node:test";
import { CreateQueueCommand, ReceiveMessageCommand, SendMessageCommand, SQSClient } from "@aws-sdk/client-sqs";
import { type StartedUnderstudyContainer, UnderstudyContainer } from "testcontainers-understudy";

let understudy: StartedUnderstudyContainer;

before(async () => {
  understudy = await new UnderstudyContainer("understudy:dev").start();
  Object.assign(process.env, understudy.getEnv());
});

after(async () => {
  await understudy?.stop();
});

test("orders queue", async () => {
  const sqs = new SQSClient({});
  const { QueueUrl } = await sqs.send(new CreateQueueCommand({ QueueName: "orders" }));
  await sqs.send(new SendMessageCommand({ QueueUrl, MessageBody: "order-1" }));
  const { Messages } = await sqs.send(new ReceiveMessageCommand({ QueueUrl }));
  assert.deepEqual(Messages?.map((m) => m.Body), ["order-1"]);
});
```

```sh
node --test orders.test.ts
```

`stop()` uses the 10-second grace unless you pass another `timeout`, and so does `await using`. `settle(timeoutMs)`, `advanceClock(byMs)` and `reset()` wrap the common admin calls.

## Lambda and DynamoDB

Lambda functions and DynamoDB Local need the Docker socket inside the container:

| | Go | Python | Node |
|---|---|---|---|
| Mount the socket | `WithDockerSocket()` | `with_docker_socket()` | `withDockerSocket()` |
| With an explicit group | `WithDockerSocketGroup(gid)` | `with_docker_socket(gid=gid)` | `withDockerSocket(gid)` |

The option bind-mounts the daemon's socket at `/var/run/docker.sock` and adds the socket's group to the container's user, since the image does not run as root. When the library reaches Docker through a local unix socket, the module reads that group from the socket file. For any other daemon, pass the numeric group that owns `/var/run/docker.sock` on the daemon's host; without it, start refuses before creating anything. Once understudy is ready, the module checks that it reports Docker `enabled`, and fails the start otherwise, so a socket understudy cannot use never looks like a working one. With a wrong group, Python's start failed with:

```text
understudy: the Docker socket is mounted but understudy reports Docker degraded: Docker at unix:///var/run/docker.sock does not answer, so Lambda functions cannot run and DynamoDB Local cannot start: docker daemon unavailable: permission denied on unix:///var/run/docker.sock; add your user to the docker group and log in again, or set DOCKER_HOST to a socket you can use; Lambda and DynamoDB will not run. Check the socket group (with_docker_socket(gid=...)) or run without with_docker_socket().
```

Mounting the socket grants root on the host to whoever controls understudy; see [SECURITY.md](../../SECURITY.md). It is tested on Linux; Docker Desktop's socket group is untested.

## Other settings

| Setting | Go | Python | Node |
|---|---|---|---|
| Freeze the [clock](clock.md) | `WithClock(Frozen{At: t})` | `with_clock(frozen_at=dt)` | `withClock({ frozenAt: date })` |
| Scale the clock | `WithClockScale(60)` | `with_clock(scale=60)` | `withClock({ scale: 60 })` |
| Seed generated IDs | `WithSeed(1)` | `with_seed(1)` | `withSeed(1n)` |
| Verify SigV4 | `WithSigV4Verify()` | `with_sigv4_verify()` | `withSigV4Verify()` |
| Load a [snapshot](snapshots.md) | `WithSnapshot(path)` | `with_snapshot(path)` | `withSnapshot(path)` |
| Admin token | `WithAdminToken()` | `with_admin_token()` | `withAdminToken()` |
| Publish address | `WithPublishedOn(addr)` | `with_published_on(addr)` | `withPublishedOn(addr)` |

Any other setting goes in as its `UNDERSTUDY_*` variable ([reference](../configuration.md)) through the library's own environment option: `testcontainers.WithEnv`, `with_env` or `withEnvironment`. understudy checks the values itself; one it refuses makes it exit, and the start fails as above.

The admin token option sets a random token, which the wait and the helpers send. Inside the container the admin API has no token otherwise, so publishing port 4566 beyond loopback, `0.0.0.0` included, needs the token option, and start refuses before creating anything:

```text
understudy: publishing port 4566 on 0.0.0.0 exposes the admin API, which has no token in the container; add with_admin_token() or publish on 127.0.0.1.
```

The endpoint's host is the daemon host the library reports, `127.0.0.1` for a local daemon. A loopback address other than `127.0.0.1`, such as `127.0.0.2`, therefore does not work yet: Python's start dialed `127.0.0.1` and timed out after 60 s.

## Terraform

The endpoint the modules return is an IP address, such as `http://127.0.0.1:32848`. At an IP address the AWS provider needs this block:

```hcl
# The endpoint and credentials come from the module's environment.
provider "aws" {
  s3_use_path_style           = true
  skip_credentials_validation = true
  skip_requesting_account_id  = true
}
```

With an account id, the provider reads S3 bucket tags through S3 Control at `<account>.<endpoint host>`, which cannot resolve for an IP address. It has no account id only with both skip settings: with credential validation on, it takes the account from its startup `sts:GetCallerIdentity`, whatever `skip_requesting_account_id` says. With `skip_requesting_account_id` alone, the first `aws_s3_bucket` failed with `dial tcp: lookup 000000000000.127.0.0.1: no such host`. [Terraform](terraform.md) has the rest of the provider setup.

A test can run Terraform in the environment the fixtures above set, with the configuration in `infra/`:

```python
# tests/test_infra.py
import subprocess

import boto3


def test_infra():
    subprocess.run(["terraform", "-chdir=infra", "init", "-input=false"], check=True)
    subprocess.run(["terraform", "-chdir=infra", "apply", "-auto-approve", "-input=false"], check=True)
    assert [b["Name"] for b in boto3.client("s3").list_buckets()["Buckets"]] == ["orders-data"]
```

## Cleanup

The module's stop gives understudy 10 seconds to remove the containers it started; with DynamoDB Local running, the whole stop took under a second here. If the test process dies first, Testcontainers' reaper, Ryuk, removes understudy's container and, through the session labels, the containers understudy started. After a `kill -9` of a Python test process, both understudy and its DynamoDB Local container were gone about 10 seconds later.

With `TESTCONTAINERS_RYUK_DISABLED=true` there is no reaper, and a killed test process leaves understudy running with every container it started. `docker stop` on the understudy container (`docker ps --filter ancestor=understudy:dev`) still lets it remove them before it exits. Once the understudy container is gone without that stop, for example after `docker rm -f`, its containers stay until you run:

```sh
understudy cleanup --all
```

```text
removed 1 container(s) and 0 network(s)
```

`cleanup --all` removes the containers of every understudy on the Docker daemon, including those of servers still running, so keep it for between test runs. It needs the `understudy` binary on the host ([install](../../README.md#install)).
