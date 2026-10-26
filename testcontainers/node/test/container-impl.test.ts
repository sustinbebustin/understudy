// Module behavior beyond container.test.ts and validation.test.ts: what a
// failed start leaves behind, the user's own mounts and groups beside the
// socket's, the stop's grace, and the call-time refusals the blind tests do
// not reach (testcontainers.md 8.2, 8.5, 8.6, 9.5).

import { execFileSync } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import assert from "node:assert/strict";
import { test } from "node:test";
import { UnderstudyConfigError, UnderstudyContainer, UnderstudyDockerError } from "../src/index.ts";
import { adminCall, dockerSocketGid, image, imageContainers, inspect, removeContainers } from "./support.ts";

test("a container that exits before ready is removed and its log reported", async () => {
  // understudy refuses a frozen clock with a scale and exits at once, before
  // Docker binds its port; start() reports understudy's own refusal and
  // removes the container rather than leaving it to Ryuk (testcontainers.md
  // 9.5, 8.3).
  const before = new Set(await imageContainers());
  try {
    await assert.rejects(
      new UnderstudyContainer(image()).withClock({ frozenAt: new Date("2026-01-01T00:00:00Z"), scale: 60 }).start(),
      (e: unknown) => {
        assert.ok(e instanceof Error && !(e instanceof UnderstudyConfigError), String(e));
        assert.match(e.message, /UNDERSTUDY_CLOCK_SCALE: scales the clock while UNDERSTUDY_CLOCK freezes it/);
        return true;
      },
    );
    assert.deepEqual(
      (await imageContainers()).filter((id) => !before.has(id)),
      [],
      "the failed start left its container",
    );
  } finally {
    removeContainers((await imageContainers()).filter((id) => !before.has(id)));
  }
});

test("a failed start settles in a process with nothing else pending", () => {
  // tc-node reads the exited container's log over a socket it unrefs; without
  // the module holding the event loop, Node would exit with start() pending
  // ("unsettled top-level await", exit code 13) instead of rejecting.
  const script = `
    import { UnderstudyContainer } from ${JSON.stringify(new URL("../src/index.ts", import.meta.url).href)};
    try {
      await new UnderstudyContainer(${JSON.stringify(image())}).withClock({ frozenAt: new Date(0), scale: 2 }).start();
      console.log("started");
    } catch (e) {
      console.log("rejected: " + String(e));
    }`;
  for (let run = 0; run < 3; run++) {
    const out = execFileSync(process.execPath, ["--input-type=module", "-e", script], {
      encoding: "utf8",
      timeout: 90_000,
    });
    assert.match(out, /^rejected: .*UNDERSTUDY_CLOCK_SCALE: scales the clock while UNDERSTUDY_CLOCK freezes it/m, out);
  }
});

test("a group given earlier is kept, and a failed Docker check removes the container", async () => {
  // withDockerSocket() after withDockerSocket(gid) keeps gid, so a group that
  // does not own the socket fails the Docker check; the failed start then
  // leaves no container, stopped or running (testcontainers.md 8.5, 9.7).
  const wrong = dockerSocketGid() + 1;
  const before = new Set(await imageContainers());
  try {
    await assert.rejects(
      new UnderstudyContainer(image()).withDockerSocket(wrong).withDockerSocket().start(),
      (e: unknown) => e instanceof UnderstudyDockerError && e.status !== "enabled",
    );
    assert.deepEqual(
      (await imageContainers()).filter((id) => !before.has(id)),
      [],
      "the failed start left its container",
    );
  } finally {
    removeContainers((await imageContainers()).filter((id) => !before.has(id)));
  }
});

class WithOwnMounts extends UnderstudyContainer {
  constructor(img: string) {
    super(img);
    this.hostConfig.Mounts = [{ Type: "tmpfs", Source: "", Target: "/scratch" }];
    this.hostConfig.GroupAdd = ["12345"];
  }
}

test("the socket keeps a subclass's own mounts and groups", async () => {
  // The socket's bind mount and group are appended, never replacing what is
  // there (testcontainers.md 8.5).
  const u = await new WithOwnMounts(image()).withDockerSocket().start();
  try {
    const host = inspect(u).HostConfig;
    assert.deepEqual((host.Mounts ?? []).map((m) => m.Target).sort(), ["/scratch", "/var/run/docker.sock"]);
    assert.deepEqual([...(host.GroupAdd ?? [])].sort(), ["12345", String(dockerSocketGid())].sort());
    const health = JSON.parse((await adminCall(u, "GET", "/health")).text) as { docker: { status: string } };
    assert.equal(health.docker.status, "enabled");
  } finally {
    await u.stop();
  }
});

test("stop keeps the grace when given other options", async () => {
  // tc-node's own stop sends SIGKILL with no drain; the module's grace lets
  // understudy handle SIGTERM and exit 0 (testcontainers.md 8.6, finding 4).
  // A partial options object keeps the 10 s default.
  const u = await new UnderstudyContainer(image()).start();
  const id = u.getId();
  try {
    await u.stop({ remove: false });
    const code = execFileSync("docker", ["inspect", "--format", "{{.State.ExitCode}}", id], { encoding: "utf8" });
    assert.equal(code.trim(), "0");
  } finally {
    removeContainers([id]);
  }
});

test("a snapshot directory is refused when the option is applied", () => {
  // The archive is one file copied to /snapshot/archive (testcontainers.md 8.2).
  const dir = mkdtempSync(join(tmpdir(), "understudy-snapshot-dir-"));
  assert.throws(
    () => new UnderstudyContainer(image()).withSnapshot(dir),
    (e: unknown) =>
      e instanceof UnderstudyConfigError &&
      e.message ===
        `understudy: withSnapshot(${JSON.stringify(dir)}) names a directory; ` +
          "pass the archive file, such as one POST /_understudy/v1/snapshots answered with.",
  );
});

test("an invalid frozenAt is refused when the option is applied", () => {
  // Argument shapes are checked when the option is called (testcontainers.md 9.5).
  assert.throws(
    () => new UnderstudyContainer(image()).withClock({ frozenAt: new Date("not a date") }),
    (e: unknown) =>
      e instanceof UnderstudyConfigError &&
      e.message ===
        'understudy: withClock({frozenAt}) got an invalid Date; pass one such as new Date("2026-01-01T00:00:00Z").',
  );
});
