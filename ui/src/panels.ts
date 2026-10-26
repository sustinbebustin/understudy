// The state views: resources by account and region, fault rules, the
// clock, the IAM policy per principal and the coverage summary. Each reads
// its admin route when shown and again when the journal reports a change
// it shows (views.ts).

import type { Admin, Answer } from "./client";
import { fill, h } from "./dom";
import { groupByScope } from "./resources";
import { type Read, readClock, readClockResult, readCoverage, readFaults, readPolicy, readPrincipals, readResources, statusKinds } from "./wire";

export type Panel = {
  readonly el: HTMLElement;
  // refresh reads the panel's routes again and redraws it.
  refresh(): Promise<void>;
};

// read applies reader to a successful answer; a failed call or an answer
// the reader refuses is the reason to show.
function read<T>(a: Answer, reader: (body: unknown) => Read<T>): Read<T> {
  if (!a.ok) {
    return { ok: false, reason: a.failure.message };
  }
  const got = reader(a.body);
  return got.ok ? got : { ok: false, reason: `the server's answer is not the form this page reads (${got.reason}); the page and the server may be from different builds` };
}

function section(title: string, ...rest: readonly (Node | null)[]): HTMLElement {
  return h("section", { class: "panel" }, h("div", { class: "panel-head" }, h("h2", {}, title), ...rest));
}

const button = (text: string, onclick: () => void, cls?: string): HTMLButtonElement =>
  h("button", { attrs: { type: "button" }, onclick, ...(cls === undefined ? {} : { class: cls }) }, text);

const table = (head: readonly string[], rows: readonly (readonly (Node | string)[])[]): HTMLTableElement =>
  h("table", {}, h("thead", {}, h("tr", {}, ...head.map((c) => h("th", {}, c)))), h("tbody", {}, ...rows.map((r) => h("tr", {}, ...r.map((c) => h("td", {}, c))))));

export function resourcesPanel(admin: Admin): Panel {
  const body = h("div");
  const el = section("Resources", button("Refresh", () => void refresh()));
  el.append(body);
  async function refresh(): Promise<void> {
    const got = read(await admin.call("GET", "/resources"), readResources);
    if (!got.ok) {
      fill(body, h("p", { class: "error" }, got.reason));
      return;
    }
    const groups = groupByScope(got.value);
    if (groups.length === 0) {
      fill(body, h("p", { class: "empty" }, "No resources yet. They appear here as calls create them."));
      return;
    }
    fill(
      body,
      ...groups.map((a) =>
        h(
          "div",
          { class: "group" },
          h("h3", {}, `Account ${a.account}`),
          ...a.regions.map((r) =>
            h(
              "div",
              { class: "group" },
              h("h4", {}, r.region === "" ? "global" : r.region),
              table(
                ["Service", "ARN", "Summary"],
                r.resources.map((res) => [res.service, h("code", {}, res.arn), res.summary ?? ""]),
              ),
            ),
          ),
        ),
      ),
    );
  }
  return { el, refresh };
}

// exampleRule is a request-phase rule in the admin API's JSON form
// (admin/faults.go), for the add form to start from.
const exampleRule = JSON.stringify(
  { phase: "request", match: { operations: ["sqs.SendMessage"] }, action: { kind: "error", shape: "QueueDoesNotExist" }, trigger: { kind: "next-n", n: 1 } },
  null,
  2,
);

export function faultsPanel(admin: Admin): Panel {
  const list = h("div");
  const note = h("p", { class: "meta" });
  const id = h("input", { attrs: { name: "id", required: "", pattern: "[A-Za-z0-9._\\-]{1,64}", placeholder: "orders-throttle", autocomplete: "off" } });
  const rule = h("textarea", { attrs: { name: "rule", rows: "8", spellcheck: "false" } });
  rule.value = exampleRule;
  const form = h(
    "form",
    { class: "add", onsubmit: () => void add() },
    h("h3", {}, "Add a rule"),
    h("label", {}, "Id ", id),
    h("label", {}, "Rule (JSON, as PUT /faults/{id} takes it)", rule),
    h("button", { attrs: { type: "submit" } }, "Add rule"),
  );
  const el = section("Fault rules", button("Refresh", () => void refresh()));
  el.append(note, list, form);

  async function add(): Promise<void> {
    let body: unknown;
    try {
      body = JSON.parse(rule.value);
    } catch (err) {
      fill(note, h("span", { class: "error" }, `The rule is not JSON (${err instanceof Error ? err.message : String(err)}); nothing was sent.`));
      return;
    }
    const got = read(await admin.call("PUT", `/faults/${encodeURIComponent(id.value)}`, body), (b) => readFaults({ faults: [b] }));
    if (!got.ok) {
      fill(note, h("span", { class: "error" }, got.reason));
      return;
    }
    const added = got.value[0];
    fill(note, `Rule ${id.value} added.`, added?.warning === undefined ? "" : h("span", { class: "warn" }, ` ${added.warning}`));
    await refresh();
  }

  async function remove(ruleId: string): Promise<void> {
    const a = await admin.call("DELETE", `/faults/${encodeURIComponent(ruleId)}`);
    fill(note, a.ok ? `Rule ${ruleId} removed.` : h("span", { class: "error" }, a.failure.message));
    await refresh();
  }

  async function refresh(): Promise<void> {
    const got = read(await admin.call("GET", "/faults"), readFaults);
    if (!got.ok) {
      fill(list, h("p", { class: "error" }, got.reason));
      return;
    }
    fill(
      list,
      got.value.length === 0
        ? h("p", { class: "empty" }, "No active fault rules.")
        : table(
            ["Id", "Phase", "Action", "Trigger", "Hits", "Warning", ""],
            got.value.map((f) => [h("code", { title: JSON.stringify(f.rule, null, 2) }, f.id), f.phase, f.action, f.trigger, String(f.hits), f.warning ?? "", button("Remove", () => void remove(f.id), "danger")]),
          ),
    );
  }
  return { el, refresh };
}

export function clockPanel(admin: Admin): Panel {
  const state = h("div");
  const note = h("p", { class: "meta" });
  const by = h("input", { attrs: { name: "by", required: "", placeholder: "90s or 1h30m", autocomplete: "off" } });
  by.value = "1m";
  const el = section("Clock", button("Refresh", () => void refresh()));
  el.append(
    state,
    h("div", { class: "actions" }, button("Freeze", () => void change({ op: "freeze" })), button("Resume", () => void change({ op: "resume" }))),
    h("form", { class: "add", onsubmit: () => void change({ op: "advance", by: by.value }) }, h("label", {}, "Advance by ", by), h("button", { attrs: { type: "submit" } }, "Advance")),
    note,
  );

  async function change(op: Readonly<Record<string, string>>): Promise<void> {
    const got = read(await admin.call("POST", "/clock", op), readClockResult);
    if (!got.ok) {
      fill(note, h("span", { class: "error" }, got.reason));
      return;
    }
    const { fired } = got.value;
    fill(
      note,
      op.op === "advance" ? `Advanced by ${by.value}; ${String(fired.length)} timer(s) fired.` : `Clock ${op.op === "freeze" ? "frozen" : "resumed"}.`,
      fired.length > 0 ? h("ul", {}, ...fired.map((t) => h("li", {}, h("code", {}, `${t.kind} ${t.id}`)))) : null,
    );
    await refresh();
  }

  async function refresh(): Promise<void> {
    const got = read(await admin.call("GET", "/clock"), readClock);
    if (!got.ok) {
      fill(state, h("p", { class: "error" }, got.reason));
      return;
    }
    const c = got.value;
    fill(
      state,
      h(
        "dl",
        {},
        h("dt", {}, "Emulator time"),
        h("dd", {}, h("code", {}, c.now)),
        h("dt", {}, "Mode"),
        h("dd", {}, c.factor === undefined ? c.mode : `${c.mode} x${String(c.factor)}`),
        h("dt", {}, "Wall time when read"),
        h("dd", {}, h("code", {}, c.wall)),
      ),
    );
  }
  return { el, refresh };
}

export function iamPanel(admin: Admin): Panel {
  const principals = h("datalist", { attrs: { id: "principals" } });
  const principal = h("input", { attrs: { name: "principal", list: "principals", required: "", placeholder: "arn:aws:iam::000000000000:role/worker", autocomplete: "off" } });
  const policy = h("div");
  const note = h("p", { class: "meta" });
  let shown: string | null = null;
  const el = section("IAM policy", button("Refresh", () => void refresh()));
  el.append(
    h("p", { class: "meta" }, "The least-privilege identity policy for a principal, from the calls it made."),
    h("form", { class: "add", onsubmit: () => void show(principal.value) }, h("label", {}, "Principal ", principal), principals, h("button", { attrs: { type: "submit" } }, "Show policy")),
    note,
    policy,
  );

  async function show(who: string): Promise<void> {
    shown = who;
    const got = read(await admin.call("GET", `/iam/policy?principal=${encodeURIComponent(who)}`), readPolicy);
    if (!got.ok) {
      fill(policy, h("p", { class: "error" }, got.reason));
      return;
    }
    const p = got.value;
    fill(
      policy,
      h("h3", {}, p.principal),
      h("pre", { class: "detail" }, JSON.stringify(p.document, null, 2)),
      p.resourcePolicies.length > 0 ? h("h4", {}, "Statements resource policies need") : null,
      p.resourcePolicies.length > 0 ? table(["Target", "Principal", "Action", "Source"], p.resourcePolicies.map((r) => [h("code", {}, r.target), r.principal, r.action, h("code", {}, r.source)])) : null,
    );
  }

  async function refresh(): Promise<void> {
    const got = read(await admin.call("GET", "/iam/usage"), readPrincipals);
    if (!got.ok) {
      fill(note, h("span", { class: "error" }, got.reason));
    } else {
      fill(principals, ...got.value.map((p) => h("option", { attrs: { value: p } })));
      fill(note, got.value.length === 0 ? "No principal has made a call yet." : `${String(got.value.length)} principal(s) have made calls; pick one or type an ARN.`);
    }
    if (shown !== null) {
      await show(shown);
    }
  }
  return { el, refresh };
}

export function coveragePanel(admin: Admin): Panel {
  const body = h("div");
  const el = section("Coverage", button("Refresh", () => void refresh()));
  el.append(h("p", { class: "meta" }, "What this build implements, by service and operation status (GET /coverage; docs/coverage.md has the notes)."), body);
  async function refresh(): Promise<void> {
    const got = read(await admin.call("GET", "/coverage"), readCoverage);
    if (!got.ok) {
      fill(body, h("p", { class: "error" }, got.reason));
      return;
    }
    const { services, protocols } = got.value;
    const total = (k: (typeof statusKinds)[number]) => String(services.reduce((n, s) => n + s.counts[k], 0));
    fill(
      body,
      h("h3", {}, "Operations"),
      table(
        ["Service", ...statusKinds],
        [...services.map((s) => [s.service, ...statusKinds.map((k) => String(s.counts[k]))]), ["all", ...statusKinds.map(total)]],
      ),
      h("h3", {}, "Protocol tests"),
      table(
        ["Protocol", "Kind", "Passed"],
        protocols.map((p) => [p.protocol, p.kind, `${String(p.passed)} of ${String(p.cases)}`]),
      ),
    );
  }
  return { el, refresh };
}
