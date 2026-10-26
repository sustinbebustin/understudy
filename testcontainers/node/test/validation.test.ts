// What the module refuses before creating anything, its version and its
// texts. A misconfiguration fails at once rather than after the startup
// timeout, with the texts of testcontainers.md 9.5 in Node's option names.

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  UNDERSTUDY_IMAGE,
  UnderstudyAdminError,
  UnderstudyConfigError,
  UnderstudyContainer,
  UnderstudyDockerError,
} from "../src/index.ts";
import { image, imageContainers } from "./support.ts";

test("the default image is the release image at the package version", () => {
  // The release image is ghcr.io/sustinbebustin/understudy tagged with the
  // version (testcontainers.md 3.2); the package moves in lockstep with
  // understudy and is 0.0.0 until the first release (testcontainers.md 8.8).
  const pkg = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8")) as { version: string };
  assert.equal(pkg.version, "0.0.0");
  assert.equal(UNDERSTUDY_IMAGE, "ghcr.io/sustinbebustin/understudy:0.0.0");
  assert.ok(UNDERSTUDY_IMAGE.endsWith(`:${pkg.version}`));
});

test("the docker text names withDockerSocket", () => {
  const e = new UnderstudyDockerError("degraded", "the Docker Engine does not answer");
  assert.ok(e instanceof Error);
  assert.equal(e.status, "degraded");
  assert.equal(e.reason, "the Docker Engine does not answer");
  assert.equal(
    e.message,
    "understudy: the Docker socket is mounted but understudy reports Docker degraded: the Docker Engine does not answer; " +
      "Lambda and DynamoDB will not run. Check the socket group (withDockerSocket(gid)) or run without withDockerSocket().",
  );
});

test("the admin error carries the envelope", () => {
  // The admin API's error envelope is {"error": {"code", "message", "hint"}}
  // (testcontainers.md 3.7).
  const e = new UnderstudyAdminError(412, "condition_not_met", "not settled after 1s", "raise ?timeout=");
  assert.ok(e instanceof Error);
  assert.deepEqual([e.status, e.code, e.hint], [412, "condition_not_met", "raise ?timeout="]);
  assert.ok(e.message.includes("not settled after 1s"), e.message);
  assert.equal(new UnderstudyAdminError(500, "", "boom").hint, "");
});

test("the socket group must be a non-negative integer group ID", () => {
  // A group name cannot resolve in a scratch image, so only a numeric GID
  // works (testcontainers.md 8.5); argument shapes are checked when the
  // option is called (testcontainers.md 9.5). A GID is 32-bit unsigned.
  for (const gid of [-1, 1.5, 4294967296, Number.NaN, Number.POSITIVE_INFINITY]) {
    assert.throws(
      () => new UnderstudyContainer(image()).withDockerSocket(gid),
      (e: unknown) =>
        e instanceof UnderstudyConfigError &&
        e.message === `understudy: the Docker socket group must be a non-negative integer group ID, not ${String(gid)}.`,
      `withDockerSocket(${String(gid)})`,
    );
  }
  for (const gid of [0, 4294967295]) {
    assert.doesNotThrow(() => new UnderstudyContainer(image()).withDockerSocket(gid), `withDockerSocket(${gid})`);
  }
});

test("the publish address must be an IP address", () => {
  // The publish address is the binding's HostIp (testcontainers.md 8.2, 8.4).
  for (const addr of ["localhost", "not-an-ip", "", "127.0.0.1:4566", "256.0.0.1"]) {
    assert.throws(
      () => new UnderstudyContainer(image()).withPublishedOn(addr),
      (e: unknown) =>
        e instanceof UnderstudyConfigError &&
        e.message === `understudy: withPublishedOn(${JSON.stringify(addr)}) is not an IP address; pass one such as 127.0.0.1.`,
      `withPublishedOn(${JSON.stringify(addr)})`,
    );
  }
});

test("a missing snapshot fails when the option is applied", () => {
  // testcontainers.md 8.2: the archive is copied in from the host.
  const missing = join(tmpdir(), `understudy-absent-${process.pid}-${Date.now()}.tar`);
  assert.throws(() => new UnderstudyContainer(image()).withSnapshot(missing), { code: "ENOENT" });
});

test("publishing beyond loopback needs the admin token", async () => {
  // The image's admin API has no token, so publishing beyond loopback, every
  // interface included, is refused at start, before any container, whatever
  // the option order (testcontainers.md 8.4, 9.5, 9.7).
  const before = (await imageContainers()).length;
  for (const addr of ["0.0.0.0", "::", "192.0.2.10", "2001:db8::1"]) {
    const want = `understudy: publishing port 4566 on ${addr} exposes the admin API, which has no token in the container; add withAdminToken() or publish on 127.0.0.1.`;
    for (const c of [
      new UnderstudyContainer(image()).withPublishedOn(addr),
      new UnderstudyContainer(image()).withPublishedOn(addr).withSeed(1n),
      new UnderstudyContainer(image()).withSeed(1n).withPublishedOn(addr),
    ]) {
      await assert.rejects(c.start(), (e: unknown) => e instanceof UnderstudyConfigError && e.message === want, addr);
    }
  }
  assert.equal((await imageContainers()).length, before);
});
