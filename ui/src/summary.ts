// One-line summaries of journal events, mirroring the CLI's per-kind
// summaries (internal/cli/journal.go) over the detail forms the journal
// writes (internal/journal/ndjson.go). A kind this build does not know, or
// a detail without the members its kind's summary reads, renders
// generically as its kind and detail, so a newer server's events still
// show and no detail breaks the page.

import { formatDuration } from "./format";
import { isCount, isRecord, type JournalEvent } from "./journal";
import { label, mechanismLabels, viaLabels } from "./labels";

type Detail = Readonly<Record<string, unknown>>;

const text = (v: unknown): string | undefined => (typeof v === "string" ? v : undefined);
const named = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);
const member = (v: unknown): Detail => (isRecord(v) ? v : {});
const positive = (v: unknown): number | undefined => (isCount(v) && v > 0 ? v : undefined);

// shortARN is an ARN's resource without its type: q1 for
// arn:aws:sqs:us-east-1:000000000000:q1, worker for
// arn:aws:lambda:us-east-1:000000000000:function:worker. Text that is no
// ARN is returned as is.
export function shortARN(arn: string): string {
  const parts = arn.split(":");
  if (parts.length < 6 || parts[0] !== "arn") {
    return arn;
  }
  const resource = parts.slice(5).join(":");
  const colon = resource.indexOf(":");
  if (colon >= 0 && !resource.slice(0, colon).includes("/")) {
    return resource.slice(colon + 1);
  }
  return resource;
}

// shortShape is an error shape id without its namespace: QueueDoesNotExist
// for com.amazonaws.sqs#QueueDoesNotExist.
const shortShape = (id: string): string => id.slice(id.indexOf("#") + 1);

function apiCall(d: Detail): string | null {
  const service = named(d.service);
  if (service === undefined) {
    return null;
  }
  const outcome = member(d.outcome);
  const parts = [`${service}.${named(d.operation) ?? "<unknown operation>"}`];
  const kind = named(outcome.kind);
  if (kind !== undefined) {
    parts.push(kind);
  }
  const shape = named(member(outcome.identity).shape);
  if (shape !== undefined) {
    parts.push(shortShape(shape));
  }
  const status = positive(outcome.status);
  if (status !== undefined) {
    parts.push(String(status));
  }
  const rule = named(outcome.rule);
  if (rule !== undefined) {
    parts.push(`by rule ${rule}`);
  }
  const latency = positive(d.latency);
  if (latency !== undefined) {
    parts.push(formatDuration(latency));
  }
  const via = named(member(d.origin).via);
  if (via !== undefined) {
    parts.push(`via ${label(viaLabels, via)}`);
  }
  return parts.join(" ");
}

function delivery(d: Detail): string | null {
  const mechanism = named(d.mechanism);
  const outcome = member(d.outcome);
  const kind = named(outcome.kind);
  if (mechanism === undefined || kind === undefined) {
    return null;
  }
  const to = named(d.to) ?? named(d.endpoint) ?? "";
  const attempt = isCount(d.attempt) ? ` attempt ${String(d.attempt)}` : "";
  let out = `${label(mechanismLabels, mechanism)} ${shortARN(text(d.from) ?? "")} -> ${shortARN(to)}${attempt} ${kind}`;
  const rule = named(outcome.rule);
  const next = named(outcome.next);
  const dlq = named(outcome.dlq);
  const reason = named(outcome.reason);
  if (rule !== undefined) {
    out += ` by ${rule}`;
  } else if (next !== undefined) {
    out += `, next attempt at ${next}`;
  } else if (dlq !== undefined) {
    out += ` to ${shortARN(dlq)}`;
  } else if (reason !== undefined) {
    out += `: ${reason}`;
  }
  return out;
}

function invocation(d: Detail): string | null {
  const fn = named(d.function);
  const outcome = member(d.outcome);
  const kind = named(outcome.kind);
  if (fn === undefined || kind === undefined) {
    return null;
  }
  const version = named(d.version);
  const parts = [version === undefined || version === "$LATEST" ? shortARN(fn) : `${shortARN(fn)}:${version}`, kind];
  const type = named(outcome.type);
  if (type !== undefined) {
    parts.push(type);
  } else if (kind === "exit-error" && isCount(outcome.code)) {
    parts.push(`code ${String(outcome.code)}`);
  }
  if (d.cold === true) {
    parts.push("cold");
  }
  const duration = positive(d.duration);
  if (duration !== undefined) {
    parts.push(formatDuration(duration));
  }
  return parts.join(" ");
}

function timerFired(d: Detail): string | null {
  const kind = named(d.timerKind);
  return kind === undefined ? null : `${kind} ${text(d.timerId) ?? ""}`.trimEnd();
}

function stateChange(d: Detail): string | null {
  const to = named(d.to);
  return to === undefined ? null : `${shortARN(text(d.resource) ?? "")} ${text(d.from) ?? ""} -> ${to}`;
}

// clockMode is a clock mode as the journal writes it: frozen at an
// instant, running, or scaled by a factor.
function clockMode(v: unknown): string | null {
  const m = member(v);
  const kind = named(m.kind);
  if (kind === undefined) {
    return null;
  }
  const at = named(m.at);
  if (at !== undefined) {
    return `${kind} at ${at}`;
  }
  return typeof m.factor === "number" && m.factor !== 0 ? `${kind} x${String(m.factor)}` : kind;
}

function clockChanged(d: Detail): string | null {
  const to = clockMode(d.to);
  if (to === null) {
    return null;
  }
  const op = named(d.op);
  return `${op === undefined ? "" : `${op}: `}${clockMode(d.from) ?? "?"} -> ${to}`;
}

function faultInjected(e: JournalEvent): string | null {
  const rule = named(e.detail.rule);
  if (rule === undefined) {
    return null;
  }
  let out = `rule ${rule} hit ${String(isCount(e.detail.hit) ? e.detail.hit : "?")}`;
  const of = positive(e.detail.of);
  if (of !== undefined) {
    out += ` of ${String(of)}`;
  }
  const parent = positive(e.cause.parent);
  if (parent !== undefined) {
    out += `, on event ${String(parent)}`;
  }
  return out;
}

function advisory(d: Detail): string | null {
  const name = named(d.advisory);
  return name === undefined ? null : `${name}: ${text(d.text) ?? ""}`;
}

function unclaimed(d: Detail): string | null {
  const method = named(d.method);
  if (method === undefined) {
    return null;
  }
  const reasons = Array.isArray(d.reasons) ? d.reasons.filter((r): r is string => typeof r === "string") : [];
  const out = `${method} ${text(d.host) ?? ""}${text(d.path) ?? ""}`;
  return reasons.length > 0 ? `${out}: ${reasons.join("; ")}` : out;
}

function gap(d: Detail): string | null {
  return isCount(d.missed) ? `missed ${String(d.missed)} event(s)` : null;
}

const strings = (v: unknown): readonly string[] => (Array.isArray(v) ? v.filter((s): s is string => typeof s === "string") : []);

// adminAction is one state-changing admin call (ADR 0007 pre-tag
// amendment): its op and the members that op carries.
function adminAction(d: Detail): string | null {
  switch (d.op) {
    case "reset": {
      const services = strings(d.services);
      const accounts = strings(d.accounts);
      const regions = strings(d.regions);
      const parts = [`reset ${services.length > 0 ? services.join(", ") : "no services"}`];
      parts.push(accounts.length > 0 ? `accounts ${accounts.join(", ")}` : "every account");
      parts.push(regions.length > 0 ? `regions ${regions.join(", ")}` : "every region");
      if (d.clock === true) {
        parts.push("clock restored");
      }
      const faults = positive(d.faults);
      if (faults !== undefined) {
        parts.push(`${String(faults)} fault rule(s) removed`);
      }
      return parts.join("; ");
    }
    case "snapshot-save": {
      const path = named(d.path);
      return path === undefined ? "snapshot saved to the response" : `snapshot saved to ${path}`;
    }
    case "snapshot-load": {
      const path = named(d.path);
      return path === undefined ? "snapshot loaded from the request" : `snapshot loaded from ${path}`;
    }
    case "fault-rule-added": {
      const rule = named(d.rule);
      return rule === undefined ? null : `fault rule ${rule} added`;
    }
    case "fault-rule-removed": {
      const rules = strings(d.rules);
      return rules.length > 0 ? `fault rule(s) ${rules.join(", ")} removed` : "no fault rules to remove";
    }
    default:
      return null;
  }
}

function known(e: JournalEvent): string | null {
  const d = e.detail;
  switch (e.kind) {
    case "api-call":
      return apiCall(d);
    case "delivery":
      return delivery(d);
    case "invocation":
      return invocation(d);
    case "timer-fired":
      return timerFired(d);
    case "state-change":
      return stateChange(d);
    case "fault-injected":
      return faultInjected(e);
    case "clock-changed":
      return clockChanged(d);
    case "advisory":
      return advisory(d);
    case "unclaimed":
      return unclaimed(d);
    case "gap":
      return gap(d);
    case "admin-action":
      return adminAction(d);
    default:
      return null;
  }
}

// generic renders any event as its kind and detail.
export function generic(e: JournalEvent): string {
  return `${e.kind} ${JSON.stringify(e.detail)}`;
}

// summary renders e in one line and never throws.
export function summary(e: JournalEvent): string {
  const out = known(e);
  return out === null || out === "" ? generic(e) : out;
}
