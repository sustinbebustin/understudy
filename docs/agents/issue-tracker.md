# Issue tracker: Local Markdown

Issues and specs for this repo live as markdown files in `.scratch/`.

## Not committed

`.scratch/` is gitignored on purpose: tickets and specs are local working state, not repo history.

- Never `git add`, force-add, or commit anything under `.scratch/`
- Never un-ignore `.scratch/` or move tickets/specs elsewhere in the repo to get them tracked
- Loose `.scratch/*.md` files at the top level are working notes, not issues; leave them alone

## Conventions

- One feature per directory: `.scratch/<feature-slug>/`
- The spec is `.scratch/<feature-slug>/spec.md`
- Implementation issues are one file per ticket at `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, numbered from `01` in dependency order (blockers first), never a single combined tickets file
- Header lines near the top of each issue file:
  - `Type:`: `bug` or `enhancement`
  - `Status:`: exactly one value. A ticket sits on its triage role (see `triage-labels.md` for the role strings) until the work is finished, then moves to `resolved`. `resolved` and `wontfix` are terminal: no separate close step, no file to move. Without `resolved`, a finished ticket left on `ready-for-agent` reads as available and gets picked up again.
  - `Blocked by:`: the numbers of the tickets that gate this one, or `None`. A ticket is unblocked when every ticket it lists is `resolved`.
- Acceptance criteria are `- [ ]` checkboxes in the body. They are the ticket's definition of done; the `Status:` line is what tells another agent whether to pick the ticket up.
- Comments and conversation history append to the bottom of the file under a `## Comments` heading

## When a skill says "publish to the issue tracker"

Create a new file under `.scratch/<feature-slug>/` (creating the directory if needed).

## When a skill says "fetch the relevant ticket"

Read the file at the referenced path. The user will normally pass the path or the issue number directly.

## Working a ticket

Whenever work traces back to a `.scratch/` ticket (`/implement` on a spec, a single ticket picked up by hand, an AFK run), the ticket file is updated as part of the work, not after it:

1. **Tick as you verify.** Flip a criterion to `- [x]` when that criterion actually passes: the test is green, the migration applies, the check came back clean. Ticking the list in one pass at the end records an intention rather than a result.
2. **Resolve.** When every criterion is `- [x]`, set `Status: resolved`.

A criterion that did not pass stays `- [ ]` and the ticket stays open. Report the gap; a ticket marked `resolved` is a claim that the next ticket may build on this one.

Anything learned along the way that a later ticket needs (a deviation from the spec, work added beyond it, a known delta a downstream ticket must expect) goes under `## Comments`, not into the checkbox line. A checkbox is a yes or a no.

## Wayfinding operations

Used by `/wayfinder`. The **map** is a file with one **child** file per ticket.

- **Map**: `.scratch/<effort>/map.md` (the Notes / Decisions-so-far / Fog body).
- **Child ticket**: `.scratch/<effort>/issues/NN-<slug>.md`, numbered from `01`, with the question in the body. A `Type:` line records the ticket type (`research`/`prototype`/`grilling`/`task`); a `Status:` line records `claimed`/`resolved`.
- **Blocking**: a `Blocked by: NN, NN` line near the top. A ticket is unblocked when every file it lists is `resolved`.
- **Frontier**: scan `.scratch/<effort>/issues/` for files that are open, unblocked, and unclaimed; first by number wins.
- **Claim**: set `Status: claimed` and save before any work.
- **Resolve**: append the answer under an `## Answer` heading, set `Status: resolved`, then append a context pointer (gist + link) to the map's Decisions-so-far in `map.md`.
