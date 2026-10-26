// Admin calls from the page (ADR 0017): every call sends the session secret
// and, once the server has asked for it, the admin token (ADR 0020). The
// token is asked for in the page and kept in memory only, like the secret.

import { type AdminError, adminError, needsToken } from "./api";
import { type Credentials, headersFor } from "./session";

export const adminPrefix = "/_understudy/v1";

export type Failure = { readonly status: number; readonly error: AdminError | null; readonly message: string };

export type Answer = { readonly ok: true; readonly body: unknown } | { readonly ok: false; readonly failure: Failure };

export type Method = "GET" | "POST" | "PUT" | "DELETE";

// AskToken shows why the token is needed and resolves with the one the
// user enters.
export type AskToken = (why: string) => Promise<string>;

// describe renders an admin error for the page: its message, then its hint.
export function describe(status: number, error: AdminError | null): string {
  if (error === null) {
    return `the server answered HTTP ${String(status)} without an admin error`;
  }
  return error.hint === undefined ? error.message : `${error.message} (${error.hint})`;
}

export class Admin {
  #credentials: Credentials;
  readonly #askToken: AskToken;
  // #asking is the token prompt in progress, shared by the calls waiting on it.
  #asking: Promise<string> | null = null;

  constructor(secret: string, askToken: AskToken) {
    this.#credentials = { secret, token: null };
    this.#askToken = askToken;
  }

  // headers returns the headers every admin call sends now.
  headers(): Record<string, string> {
    return headersFor(this.#credentials);
  }

  // token asks for the admin token once, however many calls need it, and
  // keeps it for every later call.
  async token(why: string): Promise<void> {
    if (this.#asking === null) {
      this.#asking = this.#askToken(why).finally(() => {
        this.#asking = null;
      });
    }
    const token = await this.#asking;
    this.#credentials = { ...this.#credentials, token };
  }

  // call sends method to the admin path with body as JSON, asking for the
  // token and sending the call again while the server asks for it.
  async call(method: Method, path: string, body?: unknown): Promise<Answer> {
    for (;;) {
      let resp: Response;
      try {
        resp = await fetch(adminPrefix + path, {
          method,
          headers: { ...this.headers(), ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
          ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        });
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        return { ok: false, failure: { status: 0, error: null, message: `the server did not answer (${message}); is understudy serve still running?` } };
      }
      const text = await resp.text();
      if (resp.ok) {
        return answerOf(text);
      }
      const error = adminError(text);
      if (needsToken(resp.status, error)) {
        await this.token(describe(resp.status, error));
        continue;
      }
      return { ok: false, failure: { status: resp.status, error, message: describe(resp.status, error) } };
    }
  }
}

function answerOf(text: string): Answer {
  if (text === "") {
    return { ok: true, body: null };
  }
  try {
    return { ok: true, body: JSON.parse(text) };
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return { ok: false, failure: { status: 200, error: null, message: `the answer is not JSON: ${message}` } };
  }
}
