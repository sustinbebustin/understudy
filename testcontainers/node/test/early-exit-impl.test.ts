// A container that exits before tc-node first reads its ports. tc-node waits
// up to 30 s for Docker to bind them, and an exited container never does, so
// the module removes it and reports its log (testcontainers.md 9.5, 8.3).
// Docker drops an exited container's port mapping, but on a quiet host
// tc-node's first inspect beats understudy's exit; delaying every inspect
// through tc-node's client stands in for a loaded host. node --test runs each
// file in its own process, so the delay reaches no other file.

import assert from "node:assert/strict";
import { test } from "node:test";
import { getContainerRuntimeClient } from "testcontainers";
import { UnderstudyConfigError, UnderstudyContainer } from "../src/index.ts";
import { image, imageContainers, removeContainers } from "./support.ts";

const INSPECT_DELAY_MS = 300;

test("a container that exits before its port is bound fails the start at once", async () => {
  const client = await getContainerRuntimeClient();
  const inspect = client.container.inspect.bind(client.container);
  client.container.inspect = async (container) => {
    await new Promise((done) => setTimeout(done, INSPECT_DELAY_MS));
    return inspect(container);
  };
  const before = new Set(await imageContainers());
  const started = Date.now();
  try {
    await assert.rejects(
      new UnderstudyContainer(image()).withClock({ frozenAt: new Date("2026-01-01T00:00:00Z"), scale: 60 }).start(),
      (e: unknown) => {
        assert.ok(e instanceof Error && !(e instanceof UnderstudyConfigError), String(e));
        assert.match(e.message, /^understudy: the container exited \(code 2\) before it reported ready/);
        assert.match(e.message, /the container was removed/);
        assert.match(e.message, /UNDERSTUDY_CLOCK_SCALE: scales the clock while UNDERSTUDY_CLOCK freezes it/);
        return true;
      },
    );
    assert.ok(Date.now() - started < 15_000, `start failed after ${Date.now() - started} ms`);
    assert.deepEqual(
      (await imageContainers()).filter((id) => !before.has(id)),
      [],
      "the failed start left its container",
    );
  } finally {
    removeContainers((await imageContainers()).filter((id) => !before.has(id)));
  }
});
