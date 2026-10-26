// The admin API's error envelope, {"error": {"code", "message", "hint"}}
// (ADR 0017).

export type AdminError = {
  readonly code: string;
  readonly message: string;
  readonly hint?: string;
};

// adminError returns the error an admin error body carries, or null. Not
// implemented yet: it returns null.
export function adminError(_body: string): AdminError | null {
  return null;
}

// needsToken reports an answer that asks for the admin token. Not
// implemented yet: it returns false.
export function needsToken(_status: number, _error: AdminError | null): boolean {
  return false;
}
