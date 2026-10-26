// The page's entry point (ADR 0020): reads the session secret from the
// fragment once, removes it from the address bar, keeps it (and any admin
// token) in module memory only, follows the journal and renders the views.
// Nothing goes to a cookie, localStorage or sessionStorage.

import { needsToken } from "./api";
import { Admin, type AskToken, describe } from "./client";
import { fill, h } from "./dom";
import { type FollowEnd, follow } from "./follow";
import { clockPanel, coveragePanel, faultsPanel, iamPanel, type Panel, resourcesPanel } from "./panels";
import { secretFromHash, withoutFragment } from "./session";
import { TracesView } from "./tracesview";
import { refreshFor, type View } from "./views";

type PanelName = "traces" | View | "coverage";

const tabs: readonly (readonly [PanelName, string])[] = [
  ["traces", "Traces"],
  ["resources", "Resources"],
  ["faults", "Faults"],
  ["clock", "Clock"],
  ["iam", "IAM"],
  ["coverage", "Coverage"],
];

// tail is how many of the newest events the page loads before the stream.
const tail = 500;
const firstRetry = 1000;
const lastRetry = 30_000;

function noSession(app: HTMLElement): void {
  fill(
    app,
    h(
      "section",
      { class: "panel" },
      h("h2", {}, "No UI session"),
      h("p", {}, "This page carries no session secret. Open the UI from the URL understudy serve printed: the secret travels in its fragment (#s=...)."),
      h("p", { class: "meta" }, "The page keeps the secret in memory only, so a reload needs that URL again; restarting serve draws a new secret and prints a new URL."),
    ),
  );
}

// tokenPrompt asks for the admin token in host, once per call to it.
function tokenPrompt(host: HTMLElement): AskToken {
  return (why) =>
    new Promise<string>((resolve) => {
      const input = h("input", { attrs: { type: "password", name: "token", autocomplete: "off", required: "" } });
      fill(
        host,
        h(
          "form",
          {
            class: "token",
            onsubmit: () => {
              host.hidden = true;
              fill(host);
              resolve(input.value);
            },
          },
          h("p", {}, "This listener's admin API needs its token: ", why),
          h("label", {}, "Admin token ", input),
          h("button", { attrs: { type: "submit" } }, "Use token"),
          h("p", { class: "meta" }, "The page keeps the token in memory only."),
        ),
      );
      host.hidden = false;
      input.focus();
    });
}

function endText(end: FollowEnd): string {
  switch (end.kind) {
    case "aborted":
      return "stopped";
    case "ended":
      return "the stream ended; the server may have stopped";
    case "network":
      return `the stream failed: ${end.message}`;
    case "http":
      return `the ${end.request === "stream" ? "stream" : "tail query"} was refused: ${describe(end.status, end.error)}`;
  }
}

function start(app: HTMLElement, secret: string): void {
  const tokenHost = h("section", { class: "panel token-host" });
  tokenHost.hidden = true;
  const admin = new Admin(secret, tokenPrompt(tokenHost));
  const traces = new TracesView();
  const panels: Readonly<Record<Exclude<PanelName, "traces">, Panel>> = {
    resources: resourcesPanel(admin),
    faults: faultsPanel(admin),
    clock: clockPanel(admin),
    iam: iamPanel(admin),
    coverage: coveragePanel(admin),
  };
  const stale = new Set<Exclude<PanelName, "traces">>(["resources", "faults", "clock", "iam", "coverage"]);
  const inFlight = new Set<Exclude<PanelName, "traces">>();
  const again = new Set<Exclude<PanelName, "traces">>();
  let active: PanelName = "traces";

  const status = h("span", { class: "status" }, "connecting");
  const buttons = tabs.map(([name, text]) => h("button", { attrs: { type: "button" }, onclick: () => show(name) }, text));
  const main = h("div", { class: "main" });
  fill(app, h("header", { class: "top" }, h("h1", {}, "understudy"), h("nav", {}, ...buttons), status), tokenHost, main);

  // refresh reads a panel again; a change reported while it reads makes
  // one more read once it is done.
  async function refresh(name: Exclude<PanelName, "traces">): Promise<void> {
    if (inFlight.has(name)) {
      again.add(name);
      return;
    }
    inFlight.add(name);
    try {
      await panels[name].refresh();
    } finally {
      inFlight.delete(name);
    }
    if (again.delete(name)) {
      await refresh(name);
    }
  }

  function show(name: PanelName): void {
    active = name;
    tabs.forEach(([n], i) => buttons[i]?.classList.toggle("selected", n === name));
    if (name === "traces") {
      fill(main, traces.el);
      return;
    }
    fill(main, panels[name].el);
    if (stale.delete(name)) {
      void refresh(name);
    }
  }

  function invalidate(views: readonly View[]): void {
    for (const v of views) {
      if (v === active) {
        void refresh(v);
      } else {
        stale.add(v);
      }
    }
  }

  async function followJournal(): Promise<void> {
    let wait = firstRetry;
    // resumeAfter is the newest event a stream delivered, which a reconnect
    // resumes after, so the server replays what the page missed or reports
    // it as a gap; null until a stream has delivered one.
    let resumeAfter: number | null = null;
    for (;;) {
      fill(status, "connecting");
      const end = await follow({
        fetch: async (input, init) => {
          const resp = await fetch(input, init);
          if (resp.ok && (input.split("?", 1)[0] ?? "").endsWith("/journal/stream")) {
            fill(status, "live");
            wait = firstRetry;
          }
          return resp;
        },
        headers: admin.headers(),
        ...(resumeAfter === null ? { tail } : { after: resumeAfter }),
        signal: new AbortController().signal,
        onEvent: (e, by) => {
          if (by === "stream" && e.kind !== "gap") {
            resumeAfter = Math.max(resumeAfter ?? 0, e.id);
          }
          traces.add(e);
          invalidate(refreshFor(e));
        },
        onBadLine: (line, reason) => traces.badLine(line, reason),
      });
      if (end.kind === "http" && needsToken(end.status, end.error)) {
        await admin.token(describe(end.status, end.error));
        continue;
      }
      if (end.kind === "http" && end.status === 403) {
        // The session itself was refused; asking again changes nothing.
        fill(status, h("span", { class: "error" }, endText(end)));
        return;
      }
      fill(status, h("span", { class: "warn" }, `${endText(end)}; retrying in ${String(wait / 1000)}s`));
      await new Promise<void>((resolve) => setTimeout(resolve, wait));
      wait = Math.min(wait * 2, lastRetry);
    }
  }

  show("traces");
  void followJournal();
}

const secret = secretFromHash(location.hash);
history.replaceState(history.state, "", withoutFragment(location.href));
// Opening a printed URL in this tab, such as a restarted serve's, changes
// only the fragment, which loads nothing: load the page again to read it.
window.addEventListener("hashchange", () => {
  if (secretFromHash(location.hash) !== null) {
    location.reload();
  }
});
const app = document.getElementById("app");
if (app !== null) {
  if (secret === null) {
    noSession(app);
  } else {
    start(app, secret);
  }
}
