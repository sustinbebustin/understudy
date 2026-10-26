// Following the journal: subscribe to the stream first, then query the
// newest events, so nothing recorded between the two is lost.

import type { AdminError } from "./api";
import type { JournalEvent } from "./journal";

export type FollowOptions = {
  readonly fetch: (input: string, init: RequestInit) => Promise<Response>;
  readonly headers: Readonly<Record<string, string>>;
  readonly tail: number;
  readonly signal: AbortSignal;
  readonly onEvent: (e: JournalEvent) => void;
  readonly onBadLine: (line: string, reason: string) => void;
};

export type FollowEnd =
  | { readonly kind: "aborted" }
  | { readonly kind: "ended" }
  | {
      readonly kind: "http";
      readonly request: "stream" | "tail";
      readonly status: number;
      readonly error: AdminError | null;
    }
  | { readonly kind: "network"; readonly message: string };

// follow subscribes, queries the tail, then reads the stream until it ends.
// Not implemented yet: it sends nothing and reports a network end.
export function follow(_o: FollowOptions): Promise<FollowEnd> {
  return Promise.resolve({ kind: "network", message: "follow is not implemented yet" });
}
