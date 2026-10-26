// The admin API's error envelope, {"error": {"code", "message", "hint"}}
// (ADR 0017).

import { isRecord } from "./journal";

export type AdminError = {
  readonly code: string;
  readonly message: string;
  readonly hint?: string;
};

// adminError returns the error an admin error body carries, or null when
// the body is not the envelope.
export function adminError(body: string): AdminError | null {
  let v: unknown;
  try {
    v = JSON.parse(body);
  } catch {
    // Not JSON, such as a proxy's HTML page: no admin error.
    return null;
  }
  if (!isRecord(v) || !isRecord(v.error)) {
    return null;
  }
  const { code, message, hint } = v.error;
  if (typeof code !== "string" || typeof message !== "string" || (hint !== undefined && typeof hint !== "string")) {
    return null;
  }
  return { code, message, ...(hint === undefined ? {} : { hint }) };
}

// needsToken reports an answer that asks for the admin token: status 401
// with code unauthorized (ADR 0017).
export function needsToken(status: number, error: AdminError | null): boolean {
  return status === 401 && error?.code === "unauthorized";
}
