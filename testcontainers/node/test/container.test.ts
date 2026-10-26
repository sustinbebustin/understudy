// The module against a real understudy container (testcontainers.md 8.7).
// Each test starts the image under test through the module and checks what
// testcontainers.md sections 8 and 9 say the container is, does and leaves
// behind.

import { mkdtempSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import assert from "node:assert/strict";
import { test } from "node:test";
import { getContainerRuntimeClient, LABEL_TESTCONTAINERS_SESSION_ID } from "testcontainers";
import {
  CreateQueueCommand,
  ListQueuesCommand,
  ReceiveMessageCommand,
  SendMessageCommand,
  SQSClient,
} from "@aws-sdk/client-sqs";
import {
  UnderstudyAdminError,
  UnderstudyConfigError,
  UnderstudyContainer,
  UnderstudyDockerError,
  type StartedUnderstudyContainer,
} from "../src/index.ts";
import {
  adminBytes,
  adminCall,
  clientConfig,
  clock,
  containerEnv,
  dockerSocketGid,
  image,
  imageContainers,
  inspect,
  invokeEcho,
  managedSessionContainers,
  removeContainers,
  sessionId,
} from "./support.ts";

async function withUnderstudy(c: UnderstudyContainer, body: (u: StartedUnderstudyContainer) => Promise<void>) {
  const u = await c.start();
  try {
    await body(u);
  } finally {
    await u.stop();
  }
}

test("SQS round trip", async () => {
  // Each module test does a real client round trip (testcontainers.md 8.7).
  await withUnderstudy(new UnderstudyContainer(image()), async (u) => {
    const sqs = new SQSClient(clientConfig(u));
    const { QueueUrl } = await sqs.send(new CreateQueueCommand({ QueueName: "orders" }));
    await sqs.send(new SendMessageCommand({ QueueUrl, MessageBody: "hello from testcontainers" }));
    const got = await sqs.send(new ReceiveMessageCommand({ QueueUrl, WaitTimeSeconds: 1 }));
    assert.deepEqual(
      (got.Messages ?? []).map((m) => m.Body),
      ["hello from testcontainers"],
    );
  });
});

test("getEnv mirrors understudytest.Env", async () => {
  // The endpoint names 127.0.0.1, never localhost, so SDKs address S3 path
  // style, and the environment is understudytest.Env's five variables
  // (testcontainers.md 8.2, 3.8, 9.7).
  await withUnderstudy(new UnderstudyContainer(image()), async (u) => {
    const endpoint = u.getEndpoint();
    assert.match(endpoint, /^http:\/\/127\.0\.0\.1:[0-9]+$/);
    assert.equal(endpoint, `http://127.0.0.1:${u.getMappedPort(4566)}`);
    assert.deepEqual(u.getEnv(), {
      AWS_ENDPOINT_URL: endpoint,
      AWS_ACCESS_KEY_ID: "test",
      AWS_SECRET_ACCESS_KEY: "test",
      AWS_REGION: "us-east-1",
      AWS_DEFAULT_REGION: "us-east-1",
    });
  });
});

test("port 4566 is published on loopback only", async () => {
  // 127.0.0.1 with an ephemeral port, since the image's admin API has no
  // token (testcontainers.md 8.4, finding 2), even after withExposedPorts;
  // the module creates no network (testcontainers.md 10.2).
  await withUnderstudy(new UnderstudyContainer(image()).withExposedPorts(4566), async (u) => {
    const doc = inspect(u);
    assert.deepEqual(doc.HostConfig.PortBindings, { "4566/tcp": [{ HostIp: "127.0.0.1", HostPort: "0" }] });
    for (const bindings of Object.values(doc.NetworkSettings.Ports ?? {})) {
      for (const b of bindings ?? []) assert.equal(b.HostIp, "127.0.0.1");
    }
    assert.deepEqual(Object.keys(doc.NetworkSettings.Networks), ["bridge"]);
    // Ready is reported only once health answers 200 with ready true
    // (testcontainers.md 8.3, 9.7).
    const health = await adminCall(u, "GET", "/health");
    assert.equal(health.status, 200);
    assert.equal((JSON.parse(health.text) as { ready: boolean }).ready, true);
  });
});

test("the child labels are the session label, unless the user sets them", async () => {
  // tc-node's Ryuk filter is the session label alone (testcontainers.md 8.6,
  // 6.4), and a value the user sets through the environment option wins.
  await withUnderstudy(new UnderstudyContainer(image()), async (u) => {
    assert.deepEqual(JSON.parse(containerEnv(u).UNDERSTUDY_DOCKER_LABELS ?? "null"), {
      [LABEL_TESTCONTAINERS_SESSION_ID]: await sessionId(),
    });
  });
  const mine = '{"com.example/team": "payments"}';
  await withUnderstudy(
    new UnderstudyContainer(image()).withEnvironment({ UNDERSTUDY_DOCKER_LABELS: mine }),
    async (u) => {
      assert.equal(containerEnv(u).UNDERSTUDY_DOCKER_LABELS, mine);
    },
  );
});

test("each option sets its variable", async () => {
  // Each option sets the variable testcontainers.md 8.2 names.
  const frozenAt = new Date("2026-01-02T03:04:05.006Z");
  const cases: [string, (c: UnderstudyContainer) => UnderstudyContainer, Record<string, string>][] = [
    ["frozen", (c) => c.withClock({ frozenAt }), { UNDERSTUDY_CLOCK: `frozen@${frozenAt.toISOString()}` }],
    ["scale", (c) => c.withClock({ scale: 60 }), { UNDERSTUDY_CLOCK_SCALE: "60" }],
    ["scale by a fraction", (c) => c.withClock({ scale: 2.5 }), { UNDERSTUDY_CLOCK_SCALE: "2.5" }],
    ["seed", (c) => c.withSeed(18446744073709551615n), { UNDERSTUDY_SEED: "18446744073709551615" }],
    ["sigv4", (c) => c.withSigV4Verify(), { UNDERSTUDY_SIGV4: "verify" }],
  ];
  for (const [name, configure, want] of cases) {
    await withUnderstudy(configure(new UnderstudyContainer(image())), async (u) => {
      const env = containerEnv(u);
      assert.deepEqual(Object.fromEntries(Object.keys(want).map((k) => [k, env[k]])), want, name);
    });
  }
});

test("the helpers advance, settle and reset", async () => {
  // The helpers call the endpoints of testcontainers.md 3.7: advance moves
  // the clock, settle answers once idle, reset drops service state but keeps
  // the clock.
  const at = new Date("2026-01-01T00:00:00Z");
  await withUnderstudy(new UnderstudyContainer(image()).withClock({ frozenAt: at }), async (u) => {
    let now = await clock(u);
    assert.equal(now.mode, "frozen");
    assert.equal(now.now.getTime(), at.getTime());
    const sqs = new SQSClient(clientConfig(u));
    await sqs.send(new CreateQueueCommand({ QueueName: "before-reset" }));
    await u.advanceClock(90_000);
    assert.equal((await clock(u)).now.getTime(), at.getTime() + 90_000);
    await u.settle(5_000);
    await u.reset();
    assert.deepEqual((await sqs.send(new ListQueuesCommand({}))).QueueUrls ?? [], []);
    now = await clock(u);
    assert.equal(now.mode, "frozen");
    assert.equal(now.now.getTime(), at.getTime() + 90_000);
  });
});

test("the helpers reject with the admin error", async () => {
  // A non-2xx answer rejects with UnderstudyAdminError carrying the
  // envelope's code. understudy refuses a clock moved backwards and a settle
  // timeout that is not positive as invalid_request, 400 (admin/errors.go;
  // internal/server/admin/settle.go).
  await withUnderstudy(new UnderstudyContainer(image()), async (u) => {
    await assert.rejects(
      u.advanceClock(-1_000),
      (e: unknown) => e instanceof UnderstudyAdminError && e.status === 400 && e.code === "invalid_request",
    );
    // settle sends "<timeoutMs>ms".
    await assert.rejects(
      u.settle(0),
      (e: unknown) =>
        e instanceof UnderstudyAdminError && e.status === 400 && e.code === "invalid_request" && e.message.includes('"0ms"'),
    );
  });
});

test("the admin token guards the admin API", async () => {
  // With the token option the container holds a random 32-byte token in hex
  // and accepts the open AWS API; health without it answers 401, and the
  // helpers send it (testcontainers.md 8.2, 3.3). Publishing beyond loopback
  // is then accepted, in either option order (testcontainers.md 8.4, 9.5).
  const tokens = new Set<string>();
  for (const configure of [
    (c: UnderstudyContainer) => c.withPublishedOn("0.0.0.0").withAdminToken(),
    (c: UnderstudyContainer) => c.withAdminToken().withPublishedOn("0.0.0.0"),
  ]) {
    await withUnderstudy(configure(new UnderstudyContainer(image())), async (u) => {
      const env = containerEnv(u);
      const token = env.UNDERSTUDY_ADMIN_TOKEN ?? "";
      assert.match(token, /^[0-9a-f]{64}$/);
      tokens.add(token);
      assert.equal(env.UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API, "true");
      assert.deepEqual(inspect(u).HostConfig.PortBindings?.["4566/tcp"], [{ HostIp: "0.0.0.0", HostPort: "0" }]);
      assert.equal((await adminCall(u, "GET", "/health")).status, 401);
      const health = await adminCall(u, "GET", "/health", token);
      assert.equal(health.status, 200);
      assert.equal((JSON.parse(health.text) as { ready: boolean }).ready, true);
      await u.settle(5_000);
      await u.advanceClock(1_000);
      await u.reset();
    });
  }
  assert.equal(tokens.size, 2, "want a fresh random token per container");
});

test("a snapshot is loaded at start", async () => {
  // The archive is copied in and loaded at start (testcontainers.md 8.2).
  const archive = join(mkdtempSync(join(tmpdir(), "understudy-snapshot-")), "state.tar");
  await withUnderstudy(new UnderstudyContainer(image()), async (src) => {
    await new SQSClient(clientConfig(src)).send(new CreateQueueCommand({ QueueName: "from-snapshot" }));
    writeFileSync(archive, await adminBytes(src, "POST", "/snapshots"));
  });
  await withUnderstudy(new UnderstudyContainer(image()).withSnapshot(archive), async (dst) => {
    assert.equal(containerEnv(dst).UNDERSTUDY_LOAD_SNAPSHOT, "/snapshot/archive");
    const urls = (await new SQSClient(clientConfig(dst)).send(new ListQueuesCommand({}))).QueueUrls ?? [];
    assert.equal(urls.length, 1);
    assert.ok(urls[0]?.endsWith("/from-snapshot"), String(urls));
  });
});

test("start fails fast when understudy exits", async () => {
  // understudy refuses a frozen clock with a scale itself; the module does not
  // re-validate it, and abortOnContainerExit surfaces the exit well before the
  // 60 s startup timeout (testcontainers.md 9.5, 8.3).
  const before = new Set(await imageContainers());
  const started = Date.now();
  try {
    await assert.rejects(
      new UnderstudyContainer(image()).withClock({ frozenAt: new Date("2026-01-01T00:00:00Z"), scale: 60 }).start(),
      (e: unknown) => e instanceof Error && !(e instanceof UnderstudyConfigError) && !e.message.includes("not implemented"),
    );
    assert.ok(Date.now() - started < 30_000, `start failed after ${Date.now() - started} ms`);
  } finally {
    removeContainers((await imageContainers()).filter((id) => !before.has(id)));
  }
});

// --- Docker socket ---

test("Lambda runs with the Docker socket", async () => {
  // The socket is a bind mount, never Binds, of the daemon-side path tc-node
  // reports, with the socket's group added, and the post-start check found
  // Docker enabled (testcontainers.md 8.5, 3.4).
  const gid = dockerSocketGid();
  const socket = (await getContainerRuntimeClient()).info.containerRuntime.remoteSocketPath;
  await withUnderstudy(new UnderstudyContainer(image()).withDockerSocket(), async (u) => {
    const host = inspect(u).HostConfig;
    assert.ok(
      (host.Mounts ?? []).some(
        (m) => m.Type === "bind" && m.Source === socket && m.Target === "/var/run/docker.sock" && !m.ReadOnly,
      ),
      JSON.stringify(host.Mounts),
    );
    assert.ok(!(host.Binds ?? []).some((b) => b.includes("docker.sock")), JSON.stringify(host.Binds));
    assert.ok((host.GroupAdd ?? []).includes(String(gid)), JSON.stringify(host.GroupAdd));
    const health = JSON.parse((await adminCall(u, "GET", "/health")).text) as { docker: { status: string } };
    assert.equal(health.docker.status, "enabled");
    await invokeEcho(u);
  });
});

test("an unusable Docker socket fails the start", async () => {
  // A socket understudy cannot use fails the start with understudy's status
  // and reason, and the container is stopped (testcontainers.md 8.5, 9.5,
  // 9.7). A group that does not own the socket leaves the image's non-root
  // user without access.
  const wrong = dockerSocketGid() + 1;
  const before = new Set(await imageContainers());
  try {
    await assert.rejects(new UnderstudyContainer(image()).withDockerSocket(wrong).start(), (e: unknown) => {
      assert.ok(e instanceof UnderstudyDockerError, String(e));
      assert.notEqual(e.status, "enabled");
      assert.equal(
        e.message,
        `understudy: the Docker socket is mounted but understudy reports Docker ${e.status}: ${e.reason}; ` +
          "Lambda and DynamoDB will not run. Check the socket group (withDockerSocket(gid)) or run without withDockerSocket().",
      );
      return true;
    });
    const running = (await imageContainers(false)).filter((id) => !before.has(id));
    assert.deepEqual(running, [], "the failed start left its container running");
  } finally {
    removeContainers((await imageContainers()).filter((id) => !before.has(id)));
  }
});

test("teardown leaves no child container", async () => {
  // After `await using` disposes of the container, which calls stop(), no
  // container understudy started for this session is left: the children carry
  // the session label, and the stop's grace lets understudy drain them
  // (testcontainers.md 8.6, 8.7, 9.3; findings 3 and 4).
  try {
    {
      await using u = await new UnderstudyContainer(image()).withDockerSocket().start();
      await invokeEcho(u);
      const kids = await managedSessionContainers();
      assert.ok(kids.length > 0, "no understudy child carries this session's label: the child labels did not reach its containers");
    }
    assert.deepEqual(await managedSessionContainers(), []);
  } finally {
    removeContainers(await managedSessionContainers());
  }
});
