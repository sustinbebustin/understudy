// The admin answers the views read, parsed at the boundary (admin/client.go
// and admin/state.go name each form). A reader takes the decoded JSON body
// and returns the value or why the body is not that answer; members a view
// does not show are left out.

import { isCount, isRecord } from "./journal";
import type { Resource } from "./resources";

export type Read<T> = { readonly ok: true; readonly value: T } | { readonly ok: false; readonly reason: string };

const ok = <T>(value: T): Read<T> => ({ ok: true, value });
const refuse = <T>(reason: string): Read<T> => ({ ok: false, reason });

const optionalString = (v: unknown): v is string | undefined => v === undefined || typeof v === "string";

// listOf reads body[member] as an array of T, refusing the whole answer at
// the first element read refuses.
function listOf<T>(body: unknown, member: string, read: (v: unknown) => Read<T>): Read<T[]> {
  if (!isRecord(body) || !Array.isArray(body[member])) {
    return refuse(`the answer has no ${member} array`);
  }
  const out: T[] = [];
  for (const [i, v] of body[member].entries()) {
    const got = read(v);
    if (!got.ok) {
      return refuse(`${member}[${String(i)}]: ${got.reason}`);
    }
    out.push(got.value);
  }
  return ok(out);
}

// readResources reads GET /resources: {"resources": [admin.Resource...]}.
export function readResources(body: unknown): Read<Resource[]> {
  return listOf(body, "resources", (v): Read<Resource> => {
    if (!isRecord(v)) {
      return refuse("not an object");
    }
    const { service, account, region, arn, summary } = v;
    if (typeof service !== "string" || typeof account !== "string" || typeof region !== "string" || typeof arn !== "string" || !optionalString(summary)) {
      return refuse("want service, account, region and arn strings and an optional summary string");
    }
    return ok({ service, account, region, arn, ...(summary === undefined ? {} : { summary }) });
  });
}

// FaultView is one active rule of GET /faults (admin.Fault): its id, phase,
// what it does and when, its hits so far, the server's warning, and the
// rule's JSON form as sent.
export type FaultView = {
  readonly id: string;
  readonly phase: string;
  readonly action: string;
  readonly trigger: string;
  readonly hits: number;
  readonly warning?: string;
  readonly rule: Readonly<Record<string, unknown>>;
};

// describeTrigger renders a trigger's JSON form: always, next-n with n,
// probability with p.
function describeTrigger(t: Readonly<Record<string, unknown>>): string {
  const kind = typeof t.kind === "string" ? t.kind : "?";
  if (typeof t.n === "number") {
    return `${kind} ${String(t.n)}`;
  }
  if (typeof t.p === "number") {
    return `${kind} ${String(t.p)}`;
  }
  return kind;
}

// readFaults reads GET /faults: {"faults": [{"rule", "hits", "warning"}...]}.
export function readFaults(body: unknown): Read<FaultView[]> {
  return listOf(body, "faults", (v): Read<FaultView> => {
    if (!isRecord(v) || !isRecord(v.rule)) {
      return refuse("not an object with a rule");
    }
    const { rule, hits, warning } = v;
    const { id, phase, action, trigger } = rule;
    if (typeof id !== "string" || typeof phase !== "string" || !isRecord(action) || !isRecord(trigger)) {
      return refuse("the rule lacks an id, a phase, an action or a trigger");
    }
    if (!isCount(hits) || !optionalString(warning)) {
      return refuse("want a hits count and an optional warning string");
    }
    return ok({
      id,
      phase,
      action: typeof action.kind === "string" ? action.kind : "?",
      trigger: describeTrigger(trigger),
      hits,
      ...(warning === undefined || warning === "" ? {} : { warning }),
      rule,
    });
  });
}

// ClockView is GET /clock (admin.ClockState).
export type ClockView = {
  readonly now: string;
  readonly mode: string;
  readonly factor?: number;
  readonly wall: string;
};

// readClock reads GET /clock.
export function readClock(body: unknown): Read<ClockView> {
  if (!isRecord(body)) {
    return refuse("the clock is not an object");
  }
  const { now, mode, factor, wall } = body;
  if (typeof now !== "string" || typeof mode !== "string" || typeof wall !== "string" || (factor !== undefined && typeof factor !== "number")) {
    return refuse("want now, mode and wall strings and an optional factor number");
  }
  return ok({ now, mode, wall, ...(factor === undefined ? {} : { factor }) });
}

// Fired is one timer an advance fired (admin.Timer).
export type Fired = { readonly kind: string; readonly id: string };

// readClockResult reads POST /clock: {"clock", "fired"} (admin.ClockResult).
export function readClockResult(body: unknown): Read<{ readonly clock: ClockView; readonly fired: readonly Fired[] }> {
  if (!isRecord(body)) {
    return refuse("the answer is not an object");
  }
  const clock = readClock(body.clock);
  if (!clock.ok) {
    return clock;
  }
  if (body.fired === undefined) {
    return ok({ clock: clock.value, fired: [] });
  }
  const fired = listOf(body, "fired", (v): Read<Fired> =>
    isRecord(v) && typeof v.kind === "string" && typeof v.id === "string" ? ok({ kind: v.kind, id: v.id }) : refuse("want kind and id strings"),
  );
  return fired.ok ? ok({ clock: clock.value, fired: fired.value }) : fired;
}

// ResourcePolicyView is one statement a resource's own policy needs
// (admin.ResourcePolicy).
export type ResourcePolicyView = { readonly target: string; readonly principal: string; readonly action: string; readonly source: string };

// PolicyView is GET /iam/policy (admin.Policy).
export type PolicyView = {
  readonly principal: string;
  readonly document: unknown;
  readonly resourcePolicies: readonly ResourcePolicyView[];
};

// readPolicy reads GET /iam/policy.
export function readPolicy(body: unknown): Read<PolicyView> {
  if (!isRecord(body) || typeof body.principal !== "string" || !("document" in body)) {
    return refuse("want a principal string and a document");
  }
  const { principal, document } = body;
  if (body.resourcePolicies === undefined) {
    return ok({ principal, document, resourcePolicies: [] });
  }
  const rps = listOf(body, "resourcePolicies", (v): Read<ResourcePolicyView> => {
    if (!isRecord(v)) {
      return refuse("not an object");
    }
    const { target, principal: who, action, source } = v;
    return typeof target === "string" && typeof who === "string" && typeof action === "string" && typeof source === "string"
      ? ok({ target, principal: who, action, source })
      : refuse("want target, principal, action and source strings");
  });
  return rps.ok ? ok({ principal, document, resourcePolicies: rps.value }) : rps;
}

// readPrincipals reads GET /iam/usage ({"usage": [admin.Usage...]}) as the
// principals it names, each once, in order of first use in the answer.
export function readPrincipals(body: unknown): Read<string[]> {
  const usage = listOf(body, "usage", (v): Read<string> =>
    isRecord(v) && typeof v.principal === "string" ? ok(v.principal) : refuse("want a principal string"),
  );
  return usage.ok ? ok([...new Set(usage.value)]) : usage;
}

// The coverage statuses an operation can have (internal/coverage/json.go).
export const statusKinds = ["full", "partial", "stored-only", "unbound"] as const;
export type StatusKind = (typeof statusKinds)[number];

const isStatusKind = (v: unknown): v is StatusKind => statusKinds.some((k) => k === v);

// ServiceCoverage counts one service's model operations by status.
export type ServiceCoverage = { readonly service: string; readonly counts: Readonly<Record<StatusKind, number>> };

// SuiteCoverage is one protocol-test suite's result.
export type SuiteCoverage = { readonly protocol: string; readonly kind: string; readonly cases: number; readonly passed: number };

export type CoverageSummary = { readonly services: readonly ServiceCoverage[]; readonly protocols: readonly SuiteCoverage[] };

// readCoverage reads GET /coverage's JSON form (internal/coverage/report.go)
// as counts: each service's model operations by status, and each protocol
// suite's passed cases.
export function readCoverage(body: unknown): Read<CoverageSummary> {
  const services = listOf(body, "services", (v): Read<ServiceCoverage> => {
    if (!isRecord(v) || typeof v.service !== "string" || !Array.isArray(v.operations)) {
      return refuse("want a service string and an operations array");
    }
    const counts: Record<StatusKind, number> = { full: 0, partial: 0, "stored-only": 0, unbound: 0 };
    for (const op of v.operations) {
      const kind = isRecord(op) && isRecord(op.status) ? op.status.kind : undefined;
      if (!isStatusKind(kind)) {
        return refuse(`operation ${isRecord(op) && typeof op.name === "string" ? op.name : "?"} has no known status`);
      }
      counts[kind]++;
    }
    return ok({ service: v.service, counts });
  });
  if (!services.ok) {
    return services;
  }
  const protocols = listOf(body, "protocols", (v): Read<SuiteCoverage> => {
    if (!isRecord(v)) {
      return refuse("not an object");
    }
    const { protocol, kind, cases, passed } = v;
    return typeof protocol === "string" && typeof kind === "string" && isCount(cases) && isCount(passed)
      ? ok({ protocol, kind, cases, passed })
      : refuse("want protocol and kind strings and cases and passed counts");
  });
  return protocols.ok ? ok({ services: services.value, protocols: protocols.value }) : protocols;
}
