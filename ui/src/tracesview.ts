// The live traces view: the newest traces as they arrive, and one trace's
// causal tree (ADR 0007). Events go into a Forest, which places children
// that arrive before their parents and dedupes what both the tail query and
// the stream deliver; the view redraws at most once a frame.

import { fill, h } from "./dom";
import { isRecord, type JournalEvent } from "./journal";
import { kindLabels, label } from "./labels";
import { journalKinds } from "./labels.gen";
import { summary } from "./summary";
import { Forest, type Gap, type Trace, type TraceNode } from "./traces";

// listLimit bounds the rows the trace list draws; the forest holds more.
const listLimit = 200;
const gapsShown = 5;

const knownKinds: ReadonlySet<string> = new Set(journalKinds);

// clockTime is the time of day of an RFC 3339 instant, to the millisecond;
// text in another form is returned as is.
function clockTime(at: string): string {
  const m = /T(\d\d:\d\d:\d\d(?:\.\d{1,3})?)/.exec(at);
  return m?.[1] ?? at;
}

const isPending = (e: JournalEvent): boolean => e.kind === "api-call" && isRecord(e.detail.outcome) && e.detail.outcome.kind === "pending";

function kindTag(e: JournalEvent): HTMLElement {
  return h("span", { class: `kind kind-${knownKinds.has(e.kind) ? e.kind : "other"}` }, label(kindLabels, e.kind));
}

function gapText(g: Gap): string {
  const where = g.afterId === null ? "before the first event this page saw" : `after event #${String(g.afterId)}`;
  return `The stream missed ${String(g.missed)} event(s) ${where}: this page read too slowly, or lost the stream while the journal evicted them. GET /journal/events still holds what the journal retains.`;
}

export class TracesView {
  readonly el: HTMLElement;
  readonly #forest = new Forest();
  readonly #list = h("div", { class: "trace-list" });
  readonly #tree = h("div", { class: "trace-tree" });
  readonly #meta = h("p", { class: "meta" });
  readonly #pause = h("button", { attrs: { type: "button" } }, "Pause");
  #selected: number | null = null;
  // #open: the events whose full detail is shown.
  readonly #open = new Set<number>();
  #badLines = 0;
  #lastBad = "";
  #paused = false;
  #scheduled = false;

  constructor() {
    this.#pause.addEventListener("click", () => {
      this.#paused = !this.#paused;
      this.#pause.textContent = this.#paused ? "Resume" : "Pause";
      this.#schedule();
    });
    this.el = h(
      "section",
      { class: "panel traces" },
      h("div", { class: "panel-head" }, h("h2", {}, "Live traces"), this.#pause),
      this.#meta,
      h("div", { class: "trace-split" }, this.#list, this.#tree),
    );
    this.#render();
  }

  // add places an event from the journal.
  add(e: JournalEvent): void {
    if (this.#forest.add(e) !== "duplicate") {
      this.#schedule();
    }
  }

  // badLine counts a line the page could not read.
  badLine(line: string, reason: string): void {
    this.#badLines++;
    this.#lastBad = `${reason}: ${line.length > 120 ? `${line.slice(0, 117)}...` : line}`;
    this.#schedule();
  }

  #schedule(): void {
    if (this.#scheduled) {
      return;
    }
    this.#scheduled = true;
    requestAnimationFrame(() => {
      this.#scheduled = false;
      this.#render();
    });
  }

  #render(): void {
    const traces = this.#forest.traces();
    fill(
      this.#meta,
      `${String(this.#forest.eventCount())} event(s) in ${String(traces.length)} trace(s)`,
      this.#paused ? " (paused: new events are kept, not drawn)" : "",
      this.#badLines > 0 ? h("span", { class: "warn", title: this.#lastBad }, ` ${String(this.#badLines)} line(s) unreadable; last: ${this.#lastBad}`) : null,
    );
    if (this.#paused) {
      return;
    }
    this.#renderList(traces);
    this.#renderTree();
  }

  #renderList(traces: readonly Trace[]): void {
    const gaps = this.#forest.gaps().slice(-gapsShown);
    const rows = traces.slice(0, listLimit).map((t) => {
      const head = t.root?.event ?? t.orphans[0]?.event;
      return h(
        "button",
        {
          class: `trace-row${t.id === this.#selected ? " selected" : ""}`,
          attrs: { type: "button" },
          onclick: () => {
            this.#selected = t.id;
            this.#render();
          },
        },
        h("span", { class: "id" }, `#${String(t.id)}`),
        head === undefined ? null : kindTag(head),
        h("span", { class: "summary" }, head === undefined ? "" : summary(head)),
        h("span", { class: "count" }, `${String(t.size)} event(s)`),
        t.root === null ? h("span", { class: "badge orphan" }, "root not held") : null,
        t.orphans.length > 0 ? h("span", { class: "badge orphan" }, `${String(t.orphans.length)} orphan(s)`) : null,
      );
    });
    fill(
      this.#list,
      ...gaps.map((g) => h("p", { class: "gap" }, gapText(g))),
      traces.length === 0 ? h("p", { class: "empty" }, "No events yet. Calls to the endpoint appear here as they happen.") : null,
      ...rows,
      traces.length > listLimit ? h("p", { class: "meta" }, `Showing the newest ${String(listLimit)} of ${String(traces.length)} traces.`) : null,
    );
  }

  #renderTree(): void {
    if (this.#selected === null) {
      fill(this.#tree, h("p", { class: "empty" }, "Pick a trace to see its tree."));
      return;
    }
    const t = this.#forest.trace(this.#selected);
    if (t === undefined) {
      fill(this.#tree, h("p", { class: "empty" }, `Trace #${String(this.#selected)} is no longer held: the page keeps the newest 1000 traces.`));
      return;
    }
    fill(
      this.#tree,
      h("h3", {}, `Trace #${String(t.id)}, ${String(t.size)} event(s)`),
      t.root === null ? h("p", { class: "meta" }, `Its root, event #${String(t.id)}, is not held: the journal evicted it or it has not arrived.`) : h("ul", { class: "tree" }, this.#node(t.root)),
      t.orphans.length > 0 ? h("h4", {}, "Orphans: their parent is not held") : null,
      t.orphans.length > 0 ? h("ul", { class: "tree" }, ...t.orphans.map((n) => this.#node(n))) : null,
    );
  }

  #node(n: TraceNode): HTMLElement {
    const e = n.event;
    const parent = e.cause.parent;
    const links = e.links ?? [];
    const orphanText =
      parent === undefined || parent === 0
        ? "no parent"
        : e.id === e.cause.trace || e.cause.trace === undefined
          ? `parent #${String(parent)} was evicted; the journal rooted this event in its own trace`
          : `parent #${String(parent)} not held`;
    const row = h(
      "div",
      {
        class: "node-row",
        title: "Show or hide the full event",
        onclick: () => {
          if (!this.#open.delete(e.id)) {
            this.#open.add(e.id);
          }
          this.#render();
        },
      },
      h("span", { class: "id" }, `#${String(e.id)}`),
      kindTag(e),
      h("span", { class: "summary" }, summary(e)),
      h("span", { class: "time", title: `emulator time ${e.at}; wall time ${e.wall}` }, clockTime(e.at)),
      isPending(e) ? h("span", { class: "badge pending" }, "in flight") : null,
      n.orphan ? h("span", { class: "badge orphan" }, orphanText) : null,
      links.length > 0 ? h("span", { class: "badge link", title: "Links are causes beside the parent; they are not tree edges (ADR 0007)" }, `links ${links.map((l) => `#${String(l)}`).join(", ")}`) : null,
    );
    return h(
      "li",
      {},
      row,
      this.#open.has(e.id) ? h("pre", { class: "detail" }, JSON.stringify(e, null, 2)) : null,
      n.children.length > 0 ? h("ul", {}, ...n.children.map((c) => this.#node(c))) : null,
    );
  }
}
