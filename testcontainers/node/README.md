# testcontainers-understudy

Runs [understudy](https://github.com/sustinbebustin/understudy), the local AWS emulator, as a [Testcontainers for Node](https://node.testcontainers.org/) module.

It is released with understudy: version `X.Y.Z` of this package exports `UNDERSTUDY_IMAGE`, `ghcr.io/sustinbebustin/understudy:X.Y.Z`. Nothing is published yet, so build the image with `just image`, pass `understudy:dev`, and install the package from a clone, here at `../understudy`, with your own `testcontainers` (a peer dependency, `^12.2.1`):

```sh
(cd ../understudy/testcontainers/node && bun install --frozen-lockfile && bun run build && bun pm pack --destination "$OLDPWD")
bun add -d ./testcontainers-understudy-0.0.0.tgz testcontainers@^12.2.1
```

The package is ESM and needs Node 22.22 or later. Its tests run on Node; Bun is untested.

## Use

```ts
import { after, before, test } from "node:test";
import { type StartedUnderstudyContainer, UnderstudyContainer } from "testcontainers-understudy";

let understudy: StartedUnderstudyContainer;

before(async () => {
  understudy = await new UnderstudyContainer("understudy:dev").start();
  Object.assign(process.env, understudy.getEnv()); // AWS_ENDPOINT_URL, test credentials, us-east-1
});

after(async () => {
  await understudy?.stop();
});

test("orders", async () => {
  // new SQSClient({}) and the other v3 clients now reach the container.
});
```

`start()` publishes port 4566 on `127.0.0.1` only and resolves once `GET /_understudy/v1/health` answers 200 with `"ready": true`; if understudy exits first, it rejects with understudy's log. `stop()`, and `await using`, stops understudy with a 10-second grace period unless you pass another `timeout`, which lets it remove the Lambda and DynamoDB Local containers it started, and every such container carries the session's label, so Ryuk removes them too.

## Options

| Method | Effect |
|---|---|
| `withDockerSocket()` | mounts the Docker socket and adds its group, for Lambda and DynamoDB Local; `start()` rejects with `UnderstudyDockerError` unless understudy then reports Docker enabled |
| `withDockerSocket(gid)` | the same, with the group given, for a daemon that is not a local unix socket |
| `withClock({ frozenAt, scale })` | the emulator clock |
| `withSeed(n)` | repeatable generated identifiers, as a `bigint` |
| `withSigV4Verify()` | verify request signatures |
| `withSnapshot(path)` | load a snapshot archive at start |
| `withAdminToken()` | a random admin token, sent by the wait and the helpers |
| `withPublishedOn(addr)` | publish on another address; beyond loopback only with `withAdminToken()`; a loopback address other than `127.0.0.1` is refused, since the endpoint never dials it |

`withEnvironment` sets any other `UNDERSTUDY_*` setting. `settle(timeoutMs)`, `advanceClock(byMs)` and `reset()` wrap the common admin calls and reject with `UnderstudyAdminError` on an error answer. Misconfigurations throw or reject with `UnderstudyConfigError`, before any container is created. The exception is a missing snapshot archive: `withSnapshot` throws Node's own `ENOENT` error for it.

The [Testcontainers guide](https://github.com/sustinbebustin/understudy/blob/main/docs/guide/testcontainers-modules.md) covers Lambda, Terraform and cleanup in more detail.

## Development

```sh
just image
testcontainers/node/run.sh    # bun install, typecheck, build, sealed node --test against understudy:dev
```

`UNDERSTUDY_IMAGE` selects another image; extra arguments are `node --test` options.
