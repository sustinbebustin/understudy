# Actions for the repository owner

What only the owner can do before the first public tag, as of 2026-10-14 (local `main` with DynamoDB Local on by default). Each item needs credentials, a machine the agents do not have, or an externally visible act that agents never perform. Engineering follow-ups, including the ones DEFERRED.md marks "needs a decision", are not here: an agent can take those from [DEFERRED.md](DEFERRED.md).

Suggested order: item 1 first; item 2 is optional and can wait. Decide 3 before 4, and do 4 before 5 and 6.

## 1. The macOS run (ARCHITECTURE.md 25.1)

- **What.** Run the unit tests, the Docker suite, the smoke tests and the examples on Docker Desktop on an Apple Silicon Mac. Then decide whether Docker Desktop becomes a tested topology.
- **Why the owner.** Agents have only Linux, and the decision on what the project supports is yours.
- **Next step.** Follow [docs/human/macos-run.md](../human/macos-run.md), which takes about an hour, and send back `~/understudy-macos-run.tgz` with your decision. Pre-tag wave 3 added one check that the page does not cover yet: DEFERRED.md's "Docker Desktop listening beyond loopback". Listen beyond loopback with `UNDERSTUDY_ADMIN_TOKEN` and `--allow-unauthenticated-aws-api`, and confirm that function code gets `401` from the admin API. Ask an agent to add that as a step to the page before you run it.
- **Unblocks.** Closing 25.1. It also lets an agent replace "untested" in README.md, SECURITY.md and ADR 0011 with what you observed, and update `docs/guide/lambda.md`. Finally, it answers the open "Mounts denied" question (DEFERRED.md, wave 4) and whether `*.localhost` resolves on macOS (record backlog 2.5).

## 2. The recordings the free-plan account could not make (optional)

- **What.** R01 to R18 and the KMS duplicate-alias item were recorded on 2026-10-14 in the `understudy-rec` project (us-east-2) and filed under `testdata/parity/` (25.3 is closed). Still unrecorded:
  - R06's us-east-1 and us-west-2 buckets and step 5 (`LocationConstraint=EU`, `x-amz-bucket-region`), and R15's key in another Region: free-plan projects open only their own Region.
  - The kit's optional steps: R04 step 8 (48 h TTL sweep), R03's CloudTrail check, R05 step 6 (cross-account), and R14 step 4's EventBridge Scheduler payload.
  - The kit's "Open items with no script yet" (section 3) and DEFERRED.md's "Function URL refusal status" captures (`AWS_IAM` function URL with a wrong secret, an unknown key and a malformed header).
  - R12 steps 16 to 18, the S3 control-character keys (DEFERRED.md, "xmlw control characters"). They need the recording account but no second Region: run them on `$P-edge` after step 13.
- **Why the owner.** They need a paid account with more than one Region, or a script an agent has not written yet.
- **Next step.** None is needed for the first tag. To record the Region-bound ones, repeat the R06 and R15 steps from the [recording kit](../human/recording-kit.md) in an account that allows us-east-1 and us-west-2.
- **Unblocks.** The remaining `s3.md` 4.1 region rows and the cross-region KMS text.

## 3. Decide on squashing the published history

- **What.** `origin/main` already holds 14 commits, up to `6593abc` (2026-10-10). Several of them redo the same work under similar titles: four "generator slice" commits and three "leaf packages review fixes". The repository is private today. Choose one: keep the history, squash the published commits into one, or curate them into a few commits.
- **Why the owner.** Any rewrite needs a force push to a shared remote. It is cheap while the repository is private and you are its only author. After it goes public, it breaks every clone and fork.
- **Next step.** Decide the shape. While doing so, check two things that a public history fixes for good: the author identity on all 34 commits (`sustinbebustin <999@sustinbebustin.com>`), and whether the agent-facing documents (`docs/brief/`, `docs/plan/`) should appear from the first commit. An agent can then build the rewritten branch locally for you to review. It never pushes.
- **Unblocks.** Item 4, and with it item 5.

## 4. Push

- **What.** Push local `main`, which is 20 commits ahead of `origin/main`, or the rewritten branch from item 3.
- **Why the owner.** Agents never push. On 2026-10-14 this machine's SSH key was also refused by GitHub (`git ls-remote origin`: `Permission denied (publickey)`), so push from a session where your credentials work.
- **Next step.** Without a rewrite, run `git push origin main`. With one, run `git push --force-with-lease origin main` from the reviewed branch. Then watch the first `ci` workflow run (`gh run watch`). It is the first CI run of DynamoDB Local, with you as the licensee (ADR 0014 amendment of 2026-10-14).
- **Unblocks.** The first CI run on GitHub. DEFERRED.md wave 1 ("CI not run on GitHub") records that the workflow has only been checked locally, so expect fixes from that first run.

## 5. Make the repository public

- **What.** Change `sustinbebustin/understudy` from private to public.
- **Why the owner.** It is an irreversible publication of the code and of everything in the history.
- **Next step.** After items 3 and 4:
  1. Run `gh repo edit sustinbebustin/understudy --visibility public --accept-visibility-change-consequences`.
  2. Enable private vulnerability reporting under Settings, Security. SECURITY.md sends reporters there.
  3. Add branch protection for `main` if you want it.
- **Unblocks.** Item 6, outside contributions, and the issue links that the NotImplemented message prints (ADR 0003 amendment).

## 6. The first tag and its publication

- **What.** Tag `v0.1.0` (or whatever version you choose) and publish its artifacts. [ROADMAP](../ROADMAP.md) item 4 lists them: linux and darwin binaries, the multi-arch image on GHCR, and a Homebrew tap.
- **Why the owner.** Publishing is externally visible, and agents never publish. Today `release.yml` is a dry run: on a `v*` tag it builds a snapshot and publishes nothing.
- **Next step.** Once item 1 has closed 25.1 (25.3 is closed), ask an agent to switch `release.yml` and `.goreleaser.yaml` from snapshot to publish. Create the Homebrew tap repository and the token it needs. Then push the tag.
- **Unblocks.** Users installing without building from source, and the README's image size and multi-arch claims, which nothing has measured yet (phase 1 report finding 4).
