/**
 * Testcontainers module for understudy, the local AWS emulator.
 *
 * `UnderstudyContainer` publishes port 4566 on 127.0.0.1 only, waits for
 * `GET /_understudy/v1/health` to answer 200 with `"ready": true`, labels
 * every container understudy starts with the session's Ryuk filter label,
 * and stops with a 10 s grace so understudy removes its own containers first
 * (testcontainers.md 8).
 */
import { randomBytes } from "node:crypto";
import { statSync } from "node:fs";
import { BlockList, isIP } from "node:net";
import { resolve } from "node:path";
import { text } from "node:stream/consumers";
import {
  AbstractStartedContainer,
  type ContainerRuntimeClient,
  GenericContainer,
  getContainerRuntimeClient,
  getReaper,
  LABEL_TESTCONTAINERS_SESSION_ID,
  type StartedTestContainer,
  type StopOptions,
  type StoppedTestContainer,
  Wait,
} from "testcontainers";

/** The package version; the default image and the npm version move in lockstep (testcontainers.md 8.8). */
const VERSION = "0.1.0";

/** The release image of the understudy version this package is released with. */
export const UNDERSTUDY_IMAGE = `ghcr.io/sustinbebustin/understudy:${VERSION}`;

// understudy's listener in the container (testcontainers.md 3.2).
const API_PORT = 4566;
const API_PORT_KEY = `${API_PORT}/tcp`;
const ADMIN_PREFIX = "/_understudy/v1";
// The wait's bound, though understudy is ready in milliseconds: it bounds only
// image pulls and failures. tc-node's module rule asks for 120 s
// (testcontainers.md 8.3).
const STARTUP_TIMEOUT_MS = 60_000;
// tc-node stops with no grace by default; understudy needs it to remove its
// own containers before it exits (testcontainers.md 8.6, finding 4).
const STOP_GRACE_MS = 10_000;
// How long an admin call may take beyond the time it asks understudy to wait.
const ADMIN_CALL_MS = 30_000;
// How often start() looks for a container that exited before Docker bound
// its port, and how many of its log lines it reports then.
const EXIT_POLL_MS = 100;
const EXIT_LOG_LINES = 50;
// The period of the no-op timer that keeps Node running during start().
const HOLD_INTERVAL_MS = 60_000;
const LOOPBACK = "127.0.0.1";
const SOCKET_TARGET = "/var/run/docker.sock";
const SNAPSHOT_PATH = "/snapshot/archive";
// A GID is a 32-bit unsigned integer.
const MAX_GID = 4294967295;

// 127.0.0.0/8 and ::1; BlockList also matches their IPv4-mapped IPv6 forms.
const loopback = new BlockList();
loopback.addSubnet("127.0.0.0", 8, "ipv4");
loopback.addAddress("::1", "ipv6");

/** A module setting refused before any container is created (testcontainers.md 9.5). */
export class UnderstudyConfigError extends Error {
  override name = "UnderstudyConfigError";
}

/** A mounted Docker socket understudy reports it cannot use after start (testcontainers.md 8.5). */
export class UnderstudyDockerError extends Error {
  override name = "UnderstudyDockerError";
  readonly status: string;
  readonly reason: string;

  constructor(status: string, reason: string) {
    super(
      `understudy: the Docker socket is mounted but understudy reports Docker ${status}: ${reason}; ` +
        "Lambda and DynamoDB will not run. Check the socket group (withDockerSocket(gid)) or run without withDockerSocket().",
    );
    this.status = status;
    this.reason = reason;
  }
}

/** An admin API call understudy answered outside 2xx, with its error body. */
export class UnderstudyAdminError extends Error {
  override name = "UnderstudyAdminError";
  readonly status: number;
  readonly code: string;
  readonly hint: string;

  constructor(status: number, code: string, message: string, hint = "") {
    const text = `understudy: admin API answered ${status} ${code}: ${message}`;
    super(hint === "" ? text : `${text}; ${hint}`);
    this.status = status;
    this.code = code;
    this.hint = hint;
  }
}

/** How the emulator clock starts: frozen at an instant, or running scaled. */
export interface ClockOptions {
  frozenAt?: Date;
  scale?: number;
}

/** A withDockerSocket request: read the local socket's group at start, or use the one given. */
type SocketRequest = { kind: "stat" } | { kind: "gid"; gid: number };

/** A resolved withDockerSocket request: the daemon-side socket path and its numeric group. */
interface SocketMount {
  source: string;
  gid: number;
}

/** Where tc-node's Docker client reaches the daemon. */
type Daemon = { kind: "unix"; socketPath: string } | { kind: "other"; address: string };

/** Watches one created container until stop(); `exited` holds start()'s message if it exited first. */
interface ExitWatch {
  stop(): void;
  readonly exited: Promise<string | undefined>;
}

/**
 * understudy in a container, configured by the with* methods below.
 *
 * The module owns port 4566's binding and the wait. `start()` refuses,
 * before creating anything, publishing beyond loopback without
 * `withAdminToken()` and a Docker socket whose group it cannot read; both
 * reject with UnderstudyConfigError. Argument shapes are checked when each
 * option is called, so option order never changes the outcome
 * (testcontainers.md 9.5).
 */
export class UnderstudyContainer extends GenericContainer {
  #token: string | undefined;
  #publishedOn = LOOPBACK;
  #socket: SocketRequest | undefined;
  // Set by start() for the hooks tc-node calls while it creates the container,
  // and by those hooks for start().
  #mount: SocketMount | undefined;
  #createdId: string | undefined;
  #exitWatch: ExitWatch | undefined;

  constructor(image: string) {
    super(image);
    this.withExposedPorts(API_PORT);
    this.withStartupTimeout(STARTUP_TIMEOUT_MS);
  }

  /**
   * Mount the daemon's Docker socket at /var/run/docker.sock and add its numeric group.
   *
   * Lambda and DynamoDB Local need it (testcontainers.md 8.5). Without
   * `gid`, start() reads the group of the local unix socket tc-node's client
   * uses, and refuses any other daemon. A group name cannot be given: the
   * image has no /etc/group to resolve it. A later call without `gid` keeps
   * a group given earlier. After start, a socket understudy does not report
   * enabled rejects with UnderstudyDockerError.
   */
  withDockerSocket(gid?: number): this {
    if (gid !== undefined && !(Number.isInteger(gid) && gid >= 0 && gid <= MAX_GID)) {
      throw new UnderstudyConfigError(
        `understudy: the Docker socket group must be a non-negative integer group ID, not ${String(gid)}.`,
      );
    }
    if (gid !== undefined) {
      this.#socket = { kind: "gid", gid };
    } else if (this.#socket === undefined) {
      this.#socket = { kind: "stat" };
    }
    return this;
  }

  /**
   * Freeze the emulator clock at `frozenAt`, or scale it by `scale`.
   *
   * Sets UNDERSTUDY_CLOCK=frozen@<RFC 3339> and UNDERSTUDY_CLOCK_SCALE
   * (testcontainers.md 8.2). understudy checks the scale's range and refuses
   * a frozen clock with a scale itself; start() then rejects with the
   * container's log (testcontainers.md 9.5).
   */
  withClock(clock: ClockOptions): this {
    const { frozenAt, scale } = clock;
    if (frozenAt !== undefined && Number.isNaN(frozenAt.getTime())) {
      throw new UnderstudyConfigError(
        'understudy: withClock({frozenAt}) got an invalid Date; pass one such as new Date("2026-01-01T00:00:00Z").',
      );
    }
    if (frozenAt !== undefined) {
      this.withEnvironment({ UNDERSTUDY_CLOCK: `frozen@${frozenAt.toISOString()}` });
    }
    if (scale !== undefined) {
      this.withEnvironment({ UNDERSTUDY_CLOCK_SCALE: String(scale) });
    }
    return this;
  }

  /** Set UNDERSTUDY_SEED; understudy checks its range itself (testcontainers.md 9.5). */
  withSeed(seed: bigint): this {
    return this.withEnvironment({ UNDERSTUDY_SEED: seed.toString() });
  }

  /** Set UNDERSTUDY_SIGV4=verify, so understudy checks every request signature. */
  withSigV4Verify(): this {
    return this.withEnvironment({ UNDERSTUDY_SIGV4: "verify" });
  }

  /**
   * Load the snapshot archive at `path` at start.
   *
   * The file is copied to /snapshot/archive in the container, mode 0644, and
   * UNDERSTUDY_LOAD_SNAPSHOT names it (testcontainers.md 8.2). A missing file
   * throws fs.statSync's error here, before any container; a later call
   * replaces the archive.
   */
  withSnapshot(path: string): this {
    const source = resolve(path);
    if (statSync(source).isDirectory()) {
      throw new UnderstudyConfigError(
        `understudy: withSnapshot(${JSON.stringify(path)}) names a directory; ` +
          "pass the archive file, such as one POST /_understudy/v1/snapshots answered with.",
      );
    }
    this.filesToCopy = [
      ...this.filesToCopy.filter((f) => f.target !== SNAPSHOT_PATH),
      { source, target: SNAPSHOT_PATH, mode: 0o644 },
    ];
    return this.withEnvironment({ UNDERSTUDY_LOAD_SNAPSHOT: SNAPSHOT_PATH });
  }

  /**
   * Guard the admin API with a random token the wait and the helpers send.
   *
   * Sets UNDERSTUDY_ADMIN_TOKEN to 64 hex characters from 32 random bytes
   * and UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API=true, which understudy
   * requires beside the token; health then answers 401 without it
   * (testcontainers.md 8.2, 3.3).
   */
  withAdminToken(): this {
    this.#token = randomBytes(32).toString("hex");
    return this.withEnvironment({
      UNDERSTUDY_ADMIN_TOKEN: this.#token,
      UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API: "true",
    });
  }

  /**
   * Publish port 4566 on `addr` instead of 127.0.0.1.
   *
   * An address beyond loopback, 0.0.0.0 and :: included, needs
   * withAdminToken(), or start() refuses it (testcontainers.md 8.4). Any
   * other loopback address is refused here: the endpoint and the wait dial
   * the daemon's host, 127.0.0.1 for a local daemon, so they could never
   * reach it (testcontainers.md 8.2, 9.5).
   */
  withPublishedOn(addr: string): this {
    if (isIP(addr) === 0) {
      throw new UnderstudyConfigError(
        `understudy: withPublishedOn(${JSON.stringify(addr)}) is not an IP address; pass one such as 127.0.0.1.`,
      );
    }
    if (isLoopback(addr) && addr !== LOOPBACK) {
      throw new UnderstudyConfigError(
        `understudy: withPublishedOn(${JSON.stringify(addr)}) is a loopback address the endpoint never dials: ` +
          `the endpoint and the readiness wait reach port 4566 at the Docker host's address, ${LOOPBACK} for a ` +
          `local daemon; publish on ${LOOPBACK}, or on 0.0.0.0 with withAdminToken().`,
      );
    }
    this.#publishedOn = addr;
    return this;
  }

  /**
   * Start understudy and resolve once it reports ready (testcontainers.md 8.3).
   *
   * A container that exits first rejects with its log. With the Docker
   * socket, a socket understudy does not report enabled rejects with
   * UnderstudyDockerError. A start that fails after creating the container
   * removes it.
   */
  override async start(): Promise<StartedUnderstudyContainer> {
    // tc-node unrefs the socket of the log it reads to report an exit, so a
    // process with nothing else pending would exit with start() unsettled;
    // this timer holds the event loop until start() settles.
    const hold = setInterval(() => undefined, HOLD_INTERVAL_MS);
    try {
      return await this.#start();
    } finally {
      clearInterval(hold);
    }
  }

  async #start(): Promise<StartedUnderstudyContainer> {
    this.#checkExposure();
    const client = await getContainerRuntimeClient();
    this.#mount = this.#socket === undefined ? undefined : resolveSocket(client, this.#socket);
    this.#createdId = undefined;
    let started: StartedTestContainer;
    try {
      started = await super.start();
    } catch (err) {
      // An exit the watch already saw still resolves after stop().
      this.#exitWatch?.stop();
      const exited = await this.#exitWatch?.exited;
      const failure = exited === undefined ? err : new Error(exited, { cause: err });
      if (this.#createdId !== undefined) await removeLeftover(client, this.#createdId, failure);
      throw failure;
    } finally {
      this.#exitWatch?.stop();
      this.#exitWatch = undefined;
    }
    const u = new StartedUnderstudyContainer(started, this.#token);
    if (this.#mount !== undefined) {
      try {
        const { status, reason } = dockerState(await adminCall(u.getEndpoint(), this.#token, "GET", "/health"));
        // A mounted socket never fails silently (testcontainers.md 8.5, 9.7).
        if (status !== "enabled") throw new UnderstudyDockerError(status, reason);
      } catch (err) {
        await stopAfterFailedStart(u, err);
        throw err;
      }
    }
    return u;
  }

  protected override async beforeContainerCreated(): Promise<void> {
    // After any withExposedPorts, which binds every interface (testcontainers.md 8.4, 6.3).
    this.hostConfig.PortBindings = {
      ...this.hostConfig.PortBindings,
      [API_PORT_KEY]: [{ HostIp: this.#publishedOn, HostPort: "0" }],
    };
    this.withWaitStrategy(readyWait(this.#token));
    // The session's Ryuk filter label goes on every container understudy
    // starts, so Ryuk reaps them too; a value the user set wins
    // (testcontainers.md 8.6, 6.4, 9.3).
    if (this.environment.UNDERSTUDY_DOCKER_LABELS === undefined) {
      const { sessionId } = await getReaper(await getContainerRuntimeClient());
      this.withEnvironment({
        UNDERSTUDY_DOCKER_LABELS: JSON.stringify({ [LABEL_TESTCONTAINERS_SESSION_ID]: sessionId }),
      });
    }
    if (this.#mount !== undefined) this.#addSocket(this.#mount);
  }

  protected override async containerCreated(containerId: string): Promise<void> {
    this.#createdId = containerId;
    this.#exitWatch = watchExit(await getContainerRuntimeClient(), containerId);
  }

  protected override async containerStarting(): Promise<void> {
    // Docker bound the port; from here the wait's abortOnContainerExit reports an exit.
    this.#exitWatch?.stop();
  }

  /**
   * Append the socket's bind mount and group to the user's, never replacing them.
   *
   * A bind mount, unlike a Binds entry, fails on a missing source instead of
   * creating a root-owned directory (testcontainers.md 3.4, 8.5).
   */
  #addSocket(mount: SocketMount): void {
    const mounts = this.hostConfig.Mounts ?? [];
    if (!mounts.some((m) => m.Type === "bind" && m.Source === mount.source && m.Target === SOCKET_TARGET)) {
      this.hostConfig.Mounts = [...mounts, { Type: "bind", Source: mount.source, Target: SOCKET_TARGET }];
    }
    const groups = this.hostConfig.GroupAdd ?? [];
    if (!groups.includes(String(mount.gid))) {
      this.hostConfig.GroupAdd = [...groups, String(mount.gid)];
    }
  }

  /** Refuse publishing beyond loopback without the token (testcontainers.md 8.4, 9.5). */
  #checkExposure(): void {
    if (this.#token === undefined && !isLoopback(this.#publishedOn)) {
      throw new UnderstudyConfigError(
        `understudy: publishing port 4566 on ${this.#publishedOn} exposes the admin API, which has no token in the container; ` +
          "add withAdminToken() or publish on 127.0.0.1.",
      );
    }
  }
}

/** A started understudy container. */
export class StartedUnderstudyContainer extends AbstractStartedContainer {
  readonly #token: string | undefined;

  constructor(startedTestContainer: StartedTestContainer, adminToken?: string) {
    super(startedTestContainer);
    this.#token = adminToken;
  }

  /** `http://<host>:<mapped 4566>`, with localhost as 127.0.0.1 (testcontainers.md 8.2). */
  getEndpoint(): string {
    return endpointUrl(this.getHost(), this.getMappedPort(API_PORT));
  }

  /** The environment that points an SDK at the container, as understudytest.Env (testcontainers.md 3.8). */
  getEnv(): Record<string, string> {
    return {
      AWS_ENDPOINT_URL: this.getEndpoint(),
      AWS_ACCESS_KEY_ID: "test",
      AWS_SECRET_ACCESS_KEY: "test",
      AWS_REGION: "us-east-1",
      AWS_DEFAULT_REGION: "us-east-1",
    };
  }

  /**
   * Wait up to `timeoutMs` for understudy's tracked work to finish.
   *
   * Sent as `<timeoutMs>ms` to GET /_understudy/v1/settle (testcontainers.md
   * 3.7); understudy answers 412 condition_not_met on timeout
   * (admin.ConditionNotMet in admin/errors.go).
   */
  async settle(timeoutMs: number): Promise<void> {
    const callMs = Math.max(timeoutMs, 0) + ADMIN_CALL_MS;
    await adminCall(this.getEndpoint(), this.#token, "GET", `/settle?timeout=${timeoutMs}ms`, undefined, callMs);
  }

  /** Move the emulator clock forward by `byMs` milliseconds (testcontainers.md 3.7). */
  async advanceClock(byMs: number): Promise<void> {
    await adminCall(this.getEndpoint(), this.#token, "POST", "/clock", { op: "advance", by: `${byMs}ms` });
  }

  /** Drop all service state, keeping the clock and the fault rules (testcontainers.md 3.7). */
  async reset(): Promise<void> {
    await adminCall(this.getEndpoint(), this.#token, "POST", "/reset", {});
  }

  /**
   * Stop with a 10 s grace unless `options.timeout` says otherwise, then remove the container and its volumes.
   *
   * The grace lets understudy remove the containers it started before it
   * exits (testcontainers.md 8.6). `await using` disposal calls this too.
   */
  override async stop(options?: Partial<StopOptions>): Promise<StoppedTestContainer> {
    return super.stop({ ...options, timeout: options?.timeout ?? STOP_GRACE_MS });
  }
}

/** The HTTP wait: health answers 200 with "ready": true, and an exit fails it at once (testcontainers.md 8.3). */
function readyWait(token: string | undefined) {
  const wait = Wait.forHttp(`${ADMIN_PREFIX}/health`, API_PORT, { abortOnContainerExit: true })
    .forStatusCode(200)
    .forResponsePredicate(reportsReady)
    .withStartupTimeout(STARTUP_TIMEOUT_MS);
  return token === undefined ? wait : wait.withHeaders({ Authorization: `Bearer ${token}` });
}

/**
 * One admin API call, with the token when set; the decoded JSON answer, or undefined when empty.
 *
 * A non-2xx answer rejects with UnderstudyAdminError.
 */
async function adminCall(
  endpoint: string,
  token: string | undefined,
  method: string,
  path: string,
  body?: object,
  timeoutMs = ADMIN_CALL_MS,
): Promise<unknown> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (token !== undefined) headers.Authorization = `Bearer ${token}`;
  const resp = await fetch(endpoint + ADMIN_PREFIX + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(timeoutMs),
  });
  const raw = await resp.text();
  if (!resp.ok) throw adminError(resp.status, raw);
  return raw === "" ? undefined : parseJson(raw);
}

/** Stop and remove a container whose post-start check failed; a failed stop names the container left behind. */
async function stopAfterFailedStart(u: StartedUnderstudyContainer, cause: unknown): Promise<void> {
  try {
    await u.stop();
  } catch (stopErr) {
    throw new Error(
      `understudy: start failed (${String(cause)}), and stopping container ${u.getId()} then failed too: ` +
        `${String(stopErr)}; remove it with 'docker rm -f -v ${u.getId()}'.`,
      { cause },
    );
  }
}

/**
 * Remove the container of a failed start unless it is already gone.
 *
 * tc-node removes it when the wait fails, but not when the port binding or
 * its lookup fails after the container started.
 */
async function removeLeftover(client: ContainerRuntimeClient, id: string, failure: unknown): Promise<void> {
  try {
    await client.container.getById(id).remove({ force: true, v: true });
  } catch (err) {
    if (isRecord(err) && err.statusCode === 404) return;
    throw new Error(
      `understudy: start failed (${String(failure)}), and removing container ${id} then failed too: ` +
        `${String(err)}; remove it with 'docker rm -f -v ${id}'.`,
      { cause: failure },
    );
  }
}

function isLoopback(addr: string): boolean {
  return loopback.check(addr, isIP(addr) === 6 ? "ipv6" : "ipv4");
}

/**
 * The socket to mount and its group (testcontainers.md 8.5).
 *
 * The source is the path tc-node mounts into Ryuk. The group is the one
 * given, else 0 on Docker Desktop, whose VM socket is root:root 0660, else
 * the gid of the local unix socket the client uses.
 */
function resolveSocket(client: ContainerRuntimeClient, socket: SocketRequest): SocketMount {
  const source = client.info.containerRuntime.remoteSocketPath;
  if (socket.kind === "gid") return { source, gid: socket.gid };
  const daemon = daemonOf(client);
  if (daemon.kind === "other") {
    throw new UnderstudyConfigError(
      `understudy: cannot read the group of the Docker socket on ${daemon.address}, which is not a local unix socket; ` +
        "pass withDockerSocket(<gid>), the numeric group owning /var/run/docker.sock on the daemon's host.",
    );
  }
  // Docker Desktop mounts its VM's socket, not the host's (TC-01).
  if (client.info.containerRuntime.operatingSystem.includes("Docker Desktop")) return { source, gid: 0 };
  try {
    return { source, gid: statSync(daemon.socketPath).gid };
  } catch (err) {
    throw new UnderstudyConfigError(
      `understudy: cannot read the group of the Docker socket ${daemon.socketPath}: ${String(err)}; ` +
        "pass withDockerSocket(<gid>), the numeric group owning the socket.",
      { cause: err },
    );
  }
}

/**
 * Where tc-node's client reaches the daemon, read from its dockerode modem.
 *
 * tc-node keeps the daemon URI to itself. docker-modem holds a socketPath
 * for a unix socket or a Windows named pipe, else a host, port and protocol;
 * its type declarations name none of them.
 */
function daemonOf(client: ContainerRuntimeClient): Daemon {
  const modem: object = client.container.dockerode.modem;
  const socketPath = "socketPath" in modem ? modem.socketPath : undefined;
  if (typeof socketPath === "string") {
    return process.platform === "win32"
      ? { kind: "other", address: `npipe://${socketPath}` }
      : { kind: "unix", socketPath };
  }
  const host = "host" in modem ? modem.host : undefined;
  const port = "port" in modem ? modem.port : undefined;
  const protocol = "protocol" in modem ? modem.protocol : undefined;
  if (typeof host !== "string") return { kind: "other", address: "the daemon tc-node is configured for" };
  const scheme = typeof protocol === "string" ? protocol : "tcp";
  const suffix = typeof port === "string" || typeof port === "number" ? `:${port}` : "";
  return { kind: "other", address: `${scheme}://${host}${suffix}` };
}

/**
 * Watch a created container until stop(); `exited` resolves with start()'s message if it exits first.
 *
 * tc-node waits up to 30 s for Docker to bind the published port before the
 * wait strategy runs, and a container that already exited never binds it.
 * Removing the container ends that wait at once, so understudy refusing its
 * settings fails start() in well under a second, with its log
 * (testcontainers.md 9.5).
 */
function watchExit(client: ContainerRuntimeClient, containerId: string): ExitWatch {
  const container = client.container.getById(containerId);
  let watching = true;
  const exited = (async (): Promise<string | undefined> => {
    while (watching) {
      await new Promise((done) => setTimeout(done, EXIT_POLL_MS));
      if (!watching) return undefined;
      // Not started yet, or already gone: tc-node reports what it sees.
      const state = await client.container.inspect(container).then(
        (info) => info.State,
        () => undefined,
      );
      if (!watching || state?.Status !== "exited") continue;
      let log: string;
      try {
        log = (await text(await client.container.logs(container, { tail: EXIT_LOG_LINES }))).trim();
      } catch (err) {
        log = `(the log could not be read: ${String(err)})`;
      }
      let removed = "the container was removed";
      try {
        await client.container.remove(container, { removeVolumes: true });
      } catch (err) {
        removed = `removing it failed (${String(err)}); remove it with 'docker rm -f -v ${containerId}'`;
      }
      return (
        `understudy: the container exited (code ${state.ExitCode}) before it reported ready, so understudy ` +
        `refused its settings or could not start; ${removed}. Its last ${EXIT_LOG_LINES} log lines:\n${log}`
      );
    }
    return undefined;
  })();
  return {
    stop: () => {
      watching = false;
    },
    exited,
  };
}

/** Join a daemon host, as tc-node reports it, and a port; an IPv6 host is bracketed. */
function endpointUrl(host: string, port: number): string {
  let h = host.replace(/^\[(.*)\]$/, "$1");
  // SDKs address S3 path style only at an IP endpoint (testcontainers.md 8.2).
  if (h === "localhost") h = LOOPBACK;
  if (h.includes(":")) h = `[${h}]`;
  return `http://${h}:${port}`;
}

function parseJson(raw: string): unknown {
  try {
    return JSON.parse(raw);
  } catch {
    return undefined;
  }
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function reportsReady(body: string): boolean {
  const health = parseJson(body);
  return isRecord(health) && health.ready === true;
}

/** Docker's status and reason in a health answer (testcontainers.md 3.1). */
function dockerState(health: unknown): { status: string; reason: string } {
  const docker = isRecord(health) ? health.docker : undefined;
  if (!isRecord(docker)) return { status: "unreported", reason: "the health answer has no docker field" };
  return {
    status: typeof docker.status === "string" ? docker.status : "",
    reason: typeof docker.reason === "string" ? docker.reason : "",
  };
}

/**
 * The error for a non-2xx admin answer: its envelope's fields, or the body text when it has none.
 *
 * The envelope is {"error": {"code", "message", "hint"}} (ARCHITECTURE.md 17).
 */
function adminError(status: number, raw: string): UnderstudyAdminError {
  const doc = parseJson(raw);
  const err = isRecord(doc) ? doc.error : undefined;
  if (isRecord(err) && typeof err.code === "string" && typeof err.message === "string") {
    return new UnderstudyAdminError(status, err.code, err.message, typeof err.hint === "string" ? err.hint : "");
  }
  return new UnderstudyAdminError(status, "", raw);
}
