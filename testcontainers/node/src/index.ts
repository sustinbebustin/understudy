/**
 * Testcontainers module for understudy, the local AWS emulator.
 *
 * `UnderstudyContainer` publishes port 4566 on 127.0.0.1 only, waits for
 * `GET /_understudy/v1/health` to answer 200 with `"ready": true`, labels
 * every container understudy starts with the session's Ryuk filter label,
 * and stops with a 10 s grace so understudy removes its own containers first
 * (testcontainers.md 8).
 *
 * Not implemented yet: every method rejects or throws "not implemented yet".
 */
import {
  AbstractStartedContainer,
  GenericContainer,
  type StartedTestContainer,
  type StopOptions,
  type StoppedTestContainer,
} from "testcontainers";

/** The package version; the default image and the npm version move in lockstep (testcontainers.md 8.8). */
const VERSION = "0.0.0";

/** The release image of the understudy version this package is released with. */
export const UNDERSTUDY_IMAGE = `ghcr.io/sustinbebustin/understudy:${VERSION}`;

const NOT_IMPLEMENTED = "understudy: not implemented yet";

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

/** understudy in a container, configured by the with* methods below. */
export class UnderstudyContainer extends GenericContainer {
  withDockerSocket(gid?: number): this {
    void gid;
    throw new Error(NOT_IMPLEMENTED);
  }

  withClock(clock: ClockOptions): this {
    void clock;
    throw new Error(NOT_IMPLEMENTED);
  }

  withSeed(seed: bigint): this {
    void seed;
    throw new Error(NOT_IMPLEMENTED);
  }

  withSigV4Verify(): this {
    throw new Error(NOT_IMPLEMENTED);
  }

  withSnapshot(path: string): this {
    void path;
    throw new Error(NOT_IMPLEMENTED);
  }

  withAdminToken(): this {
    throw new Error(NOT_IMPLEMENTED);
  }

  withPublishedOn(addr: string): this {
    void addr;
    throw new Error(NOT_IMPLEMENTED);
  }

  override async start(): Promise<StartedUnderstudyContainer> {
    throw new Error(NOT_IMPLEMENTED);
  }
}

/** A started understudy container. */
export class StartedUnderstudyContainer extends AbstractStartedContainer {
  constructor(startedTestContainer: StartedTestContainer) {
    super(startedTestContainer);
  }

  getEndpoint(): string {
    throw new Error(NOT_IMPLEMENTED);
  }

  getEnv(): Record<string, string> {
    throw new Error(NOT_IMPLEMENTED);
  }

  async settle(timeoutMs: number): Promise<void> {
    void timeoutMs;
    throw new Error(NOT_IMPLEMENTED);
  }

  async advanceClock(byMs: number): Promise<void> {
    void byMs;
    throw new Error(NOT_IMPLEMENTED);
  }

  async reset(): Promise<void> {
    throw new Error(NOT_IMPLEMENTED);
  }

  override async stop(options?: Partial<StopOptions>): Promise<StoppedTestContainer> {
    void options;
    throw new Error(NOT_IMPLEMENTED);
  }
}
