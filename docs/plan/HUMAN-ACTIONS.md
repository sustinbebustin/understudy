# Actions for the repository owner

What only the owner can do before the first public tag, as of 2026-10-14 (local `main` at the pre-tag hardening commit). Each item needs a legal reading, credentials, a machine the agents do not have, or an externally visible act that agents never perform. Engineering follow-ups, including the ones DEFERRED.md marks "needs a decision", are not here: an agent can take those from [DEFERRED.md](DEFERRED.md).

Suggested order: start counsel (1) first, because it takes longest. Items 2 to 4 can run in any order meanwhile. Decide 5 before 6, and do 6 before 7 and 8.

## 1. Counsel on the DynamoDB Local licence (ARCHITECTURE.md 25.2, 25.6)

- **What.** Get a legal reading of the Amazon DynamoDB Local License Agreement for two questions: may understudy keep pulling and proxying DynamoDB Local for users who accept it (25.2), and may the project accept it for its own CI (25.6).
- **Why the owner.** It is a legal judgement on the project's exposure, and only the owner can engage counsel and accept the risk.
- **Next step.** Send counsel [docs/human/dynamodb-local-licence.md](../human/dynamodb-local-licence.md) with the full `LICENSE.txt` (section 1 says how to fetch and check it). Section 3 holds the eight questions; section 4 says what each answer changes.
- **Raise in the same engagement.** These are not due before the tag:
  - Whether AWS managed policy documents may be fetched and shipped in the binary (25.5). This is due before IAM enforcement ([ROADMAP](../ROADMAP.md) item 5).
  - The terms of the Lambda base images pulled from `public.ecr.aws/lambda/*`. Nobody has examined them (`docs/research/phase1/dynamodb-local-terms.md` 6). If counsel needs a summary first, ask an agent to research them the way that file covers DynamoDB Local.
- **Unblocks.** Closing 25.2 and 25.6 in an ADR 0014 amendment. A "yes" lets CI run DynamoDB in every conformance suite ([ROADMAP](../ROADMAP.md) item 4). A "no" means an agent builds the designed lock-table fallback (section 5 of the brief) before the tag, or the tag ships with DynamoDB disabled.

## 2. Licensed DynamoDB conformance runs

- **What.** Run every conformance suite and the Docker tests with DynamoDB Local enabled. Since the release review, every run has opted DynamoDB out, so its tests have not run with any client. That includes the JS suite's two DynamoDB tests, which were fixed in pre-tag wave 3 but never run.
- **Why the owner.** Running them means accepting the DynamoDB Local licence. Agents never accept it ([brief](../brief/phase1-brief.md), working rules). You accepted it on this machine for the release review. Run these only if item 1 does not change that for you personally.
- **Next step.** From the repository root on a machine with Docker:

  ```sh
  for s in go-sdk python-boto3 js-sdk-v3 aws-cli terraform e2e-pipeline; do
    UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 conformance/$s/run.sh || echo "FAILED: $s"
  done
  UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 SIGV4=verify conformance/e2e-pipeline/run.sh
  UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 just test-docker
  ```

  No DynamoDB test should appear among the summaries' `SKIP` lines. Hand the logs to an agent.
- **Unblocks.** Closing DEFERRED.md's "Licensed DynamoDB conformance runs" (pre-tag wave 3 and pre-tag hardening) and "DynamoDB conformance runs" (pre-tag wave 1). It also lets the "Fix the JS suite's two DynamoDB tests" clause come out of ROADMAP item 4.

## 3. The macOS run (ARCHITECTURE.md 25.1)

- **What.** Run the unit tests, the Docker suite, the smoke tests and the examples on Docker Desktop on an Apple Silicon Mac. Then decide whether Docker Desktop becomes a tested topology.
- **Why the owner.** Agents have only Linux, and the decision on what the project supports is yours.
- **Next step.** Follow [docs/human/macos-run.md](../human/macos-run.md), which takes about an hour, and send back `~/understudy-macos-run.tgz` with your decision. Pre-tag wave 3 added one check that the page does not cover yet: DEFERRED.md's "Docker Desktop listening beyond loopback". Listen beyond loopback with `UNDERSTUDY_ADMIN_TOKEN` and `--allow-unauthenticated-aws-api`, and confirm that function code gets `401` from the admin API. Ask an agent to add that as a step to the page before you run it.
- **Unblocks.** Closing 25.1. It also lets an agent replace "untested" in README.md, SECURITY.md and ADR 0011 with what you observed, and update `docs/guide/lambda.md`. Finally, it answers the open "Mounts denied" question (DEFERRED.md, wave 4) and whether `*.localhost` resolves on macOS (record backlog 2.5).

## 4. Recordings against real AWS (ARCHITECTURE.md 25.3)

- **What.** Capture R01 to R07, the core workload and Terraform plan stability, against a throwaway AWS account. Tiers 2 and 3 can follow later.
- **Why the owner.** It needs real AWS credentials, and agents never call real AWS. Until it is done, the coverage page has no `aws-recorded` evidence (115 `inferred` overlay entries and 52 inferred fidelity switches at the release review), and the README's fidelity wording stays hedged.
- **Next step.** Follow [docs/human/recording-kit.md](../human/recording-kit.md): set up the `understudy-rec` free-plan project and a USD 20 budget (section 1), then run Tier 1. Tier 1 is about four hours and can span sessions with the same `$P`. Section 6 says how the captures land. Before the Tier 3 session for R13, ask an agent to add DEFERRED.md's "Function URL refusal status" captures to that row. Those are a wrong-secret, an unknown-key and a malformed-header request to an `AWS_IAM` function URL, and the kit does not list them yet.
- **Unblocks.** Closing 25.3, which the first tag waits on (ARCHITECTURE.md 19). An agent then settles each item ("settle R0x from `testdata/parity/*/R0x`") and firms up the README's Status and Limitations wording.

## 5. Decide on squashing the published history

- **What.** `origin/main` already holds 14 commits, up to `6593abc` (2026-10-10). Several of them redo the same work under similar titles: four "generator slice" commits and three "leaf packages review fixes". The repository is private today. Choose one: keep the history, squash the published commits into one, or curate them into a few commits.
- **Why the owner.** Any rewrite needs a force push to a shared remote. It is cheap while the repository is private and you are its only author. After it goes public, it breaks every clone and fork.
- **Next step.** Decide the shape. While doing so, check two things that a public history fixes for good: the author identity on all 31 commits (`sustinbebustin <999@sustinbebustin.com>`), and whether the agent-facing documents (`docs/brief/`, `docs/plan/`) should appear from the first commit. An agent can then build the rewritten branch locally for you to review. It never pushes.
- **Unblocks.** Item 6, and with it item 7.

## 6. Push

- **What.** Push local `main`, which is 17 commits ahead of `origin/main`, or the rewritten branch from item 5.
- **Why the owner.** Agents never push. On 2026-10-14 this machine's SSH key was also refused by GitHub (`git ls-remote origin`: `Permission denied (publickey)`), so push from a session where your credentials work.
- **Next step.** Without a rewrite, run `git push origin main`. With one, run `git push --force-with-lease origin main` from the reviewed branch. Then watch the first `ci` workflow run (`gh run watch`).
- **Unblocks.** The first CI run on GitHub. DEFERRED.md wave 1 ("CI not run on GitHub") records that the workflow has only been checked locally, so expect fixes from that first run.

## 7. Make the repository public

- **What.** Change `sustinbebustin/understudy` from private to public.
- **Why the owner.** It is an irreversible publication of the code and of everything in the history.
- **Next step.** After items 5 and 6, and preferably after counsel's answer to item 1:
  1. Run `gh repo edit sustinbebustin/understudy --visibility public --accept-visibility-change-consequences`.
  2. Enable private vulnerability reporting under Settings, Security. SECURITY.md sends reporters there.
  3. Add branch protection for `main` if you want it.
- **Unblocks.** Item 8, outside contributions, and the issue links that the NotImplemented message prints (ADR 0003 amendment).

## 8. The first tag and its publication

- **What.** Tag `v0.1.0` (or whatever version you choose) and publish its artifacts. [ROADMAP](../ROADMAP.md) item 4 lists them: linux and darwin binaries, the multi-arch image on GHCR, and a Homebrew tap.
- **Why the owner.** Publishing is externally visible, and agents never publish. Today `release.yml` is a dry run: on a `v*` tag it builds a snapshot and publishes nothing.
- **Next step.** Once items 1 to 4 have closed 25.1, 25.2, 25.3 and 25.6, ask an agent to switch `release.yml` and `.goreleaser.yaml` from snapshot to publish. Create the Homebrew tap repository and the token it needs. Then push the tag.
- **Unblocks.** Users installing without building from source, and the README's image size and multi-arch claims, which nothing has measured yet (phase 1 report finding 4).
