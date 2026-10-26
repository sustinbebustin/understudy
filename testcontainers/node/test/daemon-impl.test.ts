// The socket group on a daemon that is not a local unix socket
// (testcontainers.md 8.5, 9.5). tc-node asks the daemon for /info before it
// hands out a client, so a stand-in daemon on loopback TCP answers that one
// call; every other request fails. node --test runs each file in its own
// process, so DOCKER_HOST here reaches no other file's client.

import { createServer } from "node:http";
import assert from "node:assert/strict";
import { after, before, test } from "node:test";
import { UnderstudyConfigError, UnderstudyContainer } from "../src/index.ts";
import { image } from "./support.ts";

const requests: string[] = [];
const daemon = createServer((req, res) => {
  requests.push(`${req.method} ${req.url}`);
  if (req.method === "GET" && /^(\/v[0-9.]+)?\/info$/.test(req.url ?? "")) {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ OperatingSystem: "stand-in", OSType: "linux", ServerVersion: "29.0.0" }));
    return;
  }
  res.writeHead(500, { "Content-Type": "application/json" });
  res.end(JSON.stringify({ message: "the stand-in daemon answers only /info" }));
});
let address = "";

before(async () => {
  await new Promise<void>((done) => daemon.listen(0, "127.0.0.1", done));
  const bound = daemon.address();
  assert.ok(bound !== null && typeof bound === "object", `the stand-in daemon listens on ${String(bound)}`);
  address = `127.0.0.1:${bound.port}`;
  process.env.DOCKER_HOST = `tcp://${address}`;
});

after(() => {
  daemon.close();
});

test("the socket group is refused on a TCP daemon", async () => {
  // Only a local unix socket can be stat'ed for its group; start() refuses any
  // other daemon before creating anything, naming the address tc-node's client
  // holds (docker-modem dials TCP as http).
  await assert.rejects(
    new UnderstudyContainer(image()).withDockerSocket().start(),
    (e: unknown) =>
      e instanceof UnderstudyConfigError &&
      e.message ===
        `understudy: cannot read the group of the Docker socket on http://${address}, which is not a local unix socket; ` +
          "pass withDockerSocket(<gid>), the numeric group owning /var/run/docker.sock on the daemon's host.",
  );
  assert.deepEqual(
    requests.filter((r) => !/\/info$/.test(r)),
    [],
    "start() called the daemon beyond /info",
  );
});

test("an explicit group needs no local socket", async () => {
  // With the group given, start() goes past validation to the daemon, which
  // here fails the image lookup.
  requests.length = 0;
  await assert.rejects(new UnderstudyContainer(image()).withDockerSocket(999).start(), (e: unknown) => {
    assert.ok(!(e instanceof UnderstudyConfigError), String(e));
    return true;
  });
  assert.ok(
    requests.some((r) => r.includes("/images/")),
    `start() never reached the daemon's image API: ${JSON.stringify(requests)}`,
  );
});
