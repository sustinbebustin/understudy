# Testcontainers modules: primary-source research

Scope: the design input for [ROADMAP](../ROADMAP.md) item 4's "Testcontainers modules for Go, Python and JS over `--ready-file` and `/health`". First, the dependency decision the maintainer asked for (section 2): whether to build on the Testcontainers libraries, on something self-maintained, or on another widely used library, per language and with measurements. Then the module design. This note covers no AWS behavior. Its claims are about Testcontainers, Ryuk, the Docker Engine and understudy itself. Sources: the Testcontainers sources at the release tags pinned in 0.2; their module guidelines and the community module registry; Ryuk; Docker's documentation; the Go modules reference; registry metadata from proxy.golang.org, pkg.go.dev, pypi.org, pypistats.org, registry.npmjs.org and api.npmjs.org; the GitHub API; and understudy's own repo files (repo-relative paths). Sources were read on 2026-10-25 and 2026-10-26, and every registry and GitHub number was measured on 2026-10-26. "Last 12 months" means since 2025-10-26.

## 0. How to read this

### 0.1 Evidence tags

| Tag | Meaning |
|---|---|
| [src] | Upstream source at the pinned commit in 0.2, cited as `<short name> path:Lnn`. |
| [doc] | Vendor documentation (docs.docker.com, go.dev, the Testcontainers guides in their repositories). |
| [measured] | Measured for this note on 2026-10-26: registry APIs, the GitHub API, or a probe module built in a scratch directory outside the repository (never inside it). Probe builds used Go 1.27.3 on linux/amd64, `CGO_ENABLED=0 -trimpath -ldflags='-s -w'`, the ADR 0016 method. Python trees came from `uv pip compile` for CPython 3.13 on Linux. npm trees came from `pnpm add --lockfile-only --ignore-scripts`, so nothing was installed or executed. |
| [repo] | A file in this repository, repo-relative. |
| inferred | My reasoning from the cited facts. It is never presented as a source's claim. |

UNVERIFIED marks behavior that no source settles. Each UNVERIFIED item has a recording case in section 11. None of those cases calls AWS.

### 0.2 Pinned sources

| Short name | Upstream | Pin |
|---|---|---|
| tc-go | testcontainers/testcontainers-go | tag v0.44.0, commit `007bd6b1407fe3f2ab9487479e8bd841e16a1c97` (2026-08-07) |
| tc-py | testcontainers/testcontainers-python | tag testcontainers-v4.15.0, commit `04267b35bdcf79ee9d2efbfdd61b2512dab0bdac` (2026-07-24) |
| tc-node | testcontainers/testcontainers-node | tag v12.2.1, commit `17316dbd001bf7468023087e3620a134466f32c5` (2026-10-13) |
| ryuk | testcontainers/moby-ryuk | tag 0.14.0, commit `b3726afd6cc2c36628abcc08e9cabac43f587384` (2025-09-26); the image tc-go v0.44.0 pulls by default is `testcontainers/ryuk:0.14.0` (tc-go `internal/config/config.go:L16`), as does tc-node 12.2.1 (tc-node `packages/testcontainers/src/reaper/reaper.ts:L21`). tc-py 4.15.0 defaults to `testcontainers/ryuk:0.8.1` (tc-py `src/testcontainers/core/config.py:L105`): tag 0.8.1, commit `2035aab9f891dc8617b8458c2322f43caeeb88d2`, cited as "ryuk 0.8.1" (section 7) |
| registry | testcontainers/community-module-registry | commit `46e1729f7a2c46593f79c0f53b984436d46406b0` |
| docker-py | docker/docker-py | tag 7.2.0 |

## 1. Findings that change the design

1. **For Go, understudy already ships the self-maintained launcher, and it is the better default.** `understudytest.Start` runs understudy in process on `127.0.0.1:0`, with its own state per test and Docker for Lambda ([repo] `understudytest/understudytest.go:L95-L115`). A consumer probe links 1 module and has 8 entries in its module graph. A testcontainers-go probe links 44 modules (section 2.2). A Go Testcontainers module is worth having only for users who already run testcontainers-go and need understudy as a container, for example beside an application container on a shared Docker network.
2. **No library binds published ports to loopback by default, and understudy's admin API in the image is unauthenticated.** Inside the image, understudy listens on `0.0.0.0:4566` without a token, and the published port is the only exposure control ([repo] `README.md:L65`, `docs/ARCHITECTURE.md` 24). tc-go and tc-node publish exposed ports with `HostPort: "0"` and no `HostIp` (tc-go `lifecycle.go:L635`, tc-node `packages/testcontainers/src/generic-container/generic-container.ts:L426-L429`). tc-py's `with_bind_ports` takes no host IP (tc-py `src/testcontainers/core/container.py:L165-L201`). Docker then publishes on `0.0.0.0` and `[::]`, and "Publishing container ports is insecure by default" ([doc] [port publishing](https://docs.docker.com/engine/network/port-publishing/)). Every module must set `HostIp: 127.0.0.1` itself (section 8.4).
3. **When Ryuk removes the understudy container, understudy's own Lambda and DynamoDB Local containers are left behind.** Ryuk force-removes, so understudy gets no shutdown (ryuk `reaper.go:L30`). Ryuk selects by the Testcontainers session labels (section 7), and understudy labels its children only `understudy.dev/*` ([repo] `internal/docker/labels.go`). A later containerized understudy never sweeps them either: the stale-run check requires the same boot and PID namespace ([repo] `internal/server/runs.go`, `gone`). This needs one understudy change: a setting that adds labels to every container and network understudy creates (section 9.3). tc-node's LocalStack module already solves the same problem by passing the session label to LocalStack's Lambda containers (tc-node `packages/modules/localstack/src/localstack-container.ts:L37-L48`).
4. **Two of the three libraries stop containers with no grace period, so even a normal teardown skips understudy's drain.** tc-py's `stop()` is `remove(force=True)` (tc-py `container.py:L292-L294`). tc-node's stop defaults to a timeout of 0 (tc-node `started-generic-container.ts:L129-L130`). Only tc-go stops with a 10 s grace and then removes the container (tc-go `cleanup.go:L24-L28`, `docker.go:L328-L352`). The Python and Node modules must override stop with a grace period (section 8.6).
5. **Wait on HTTP `GET /_understudy/v1/health` = 200, not on the image HEALTHCHECK, and never on a bare `/health`.** The HEALTHCHECK's first run is `interval` (5 s) after start ([repo] `Dockerfile:L65-L66`; [doc] [Dockerfile reference, HEALTHCHECK](https://docs.docker.com/reference/dockerfile/): "The health check will first run interval seconds after the container is started"). understudy is ready in about 20 ms ([repo] `README.md`, "Small and offline"). `/health` is an S3 path (bucket `health`). `/_understudy/...` cannot be a bucket name, because AWS bucket names begin with a letter or digit ([s3.md 3.1](s3.md); [repo] `docs/ARCHITECTURE.md` 4.1).
6. **Docker socket access for the image's non-root user needs a numeric `--group-add`, and on Docker Desktop the value is UNVERIFIED.** The image runs as `65532:65532` from `scratch` ([repo] `Dockerfile:L54`), and the README's Linux recipe adds the socket's GID ([repo] `README.md:L70-L73`). A module can stat the socket only when the daemon is local. Docker Desktop's in-VM socket ownership is recording case TC-01.
7. **A Go module that imports understudy's `admin` package inherits Go 1.27 as its floor.** The go line "is a mandatory requirement" and "must be greater than or equal to the go line of all dependencies" ([doc] [go.mod reference, go directive](https://go.dev/ref/mod#go-mod-file-go)). understudy's go.mod says `go 1.27` ([repo] `go.mod`). tc-go v0.44.0 needs only `go 1.25.0` [measured: proxy.golang.org `@v/v0.44.0.mod`].
8. **tc-go is v0 and changed its public types in a minor release.** v0.41.0 required `github.com/docker/docker`. v0.42.0 (2026-04-09) replaced it with `github.com/moby/moby/client` and `moby/moby/api`, and v0.44.0 requires client v0.5.0 and api v1.55.0 [measured: proxy.golang.org `.mod` files]. A module's `WithHostConfigModifier` callback takes the moby `container.HostConfig` type (tc-go `options.go:L109`), so the module source must follow such moves. The moby client is the module ADR 0016 refused, at v0.6.2, for `internal/docker` ([repo] `docs/adr/0016-third-party-inputs-and-dependencies.md`, Refused).
9. **Package names `understudy` are taken on both PyPI and npm by unrelated projects** [measured: `pypi.org/pypi/understudy/json` and `registry.npmjs.org/understudy` answer 200 with other projects]. `testcontainers-understudy` is free on both [measured: 404]. Registry precedents for the names are `testcontainers-spicedb` on PyPI, listed as `maintainer: community` (registry `modules/spicedb/index.md:L17-L22`), and, among modules listed as `maintainer: official`, `@microcks/microcks-testcontainers` on npm and `github.com/microcks/microcks-testcontainers-go` (registry `modules/microcks/index.md:L26-L46`).

## 2. The dependency decision

The maintainer's question was: "Why testcontainers-go over something self maintained for what we need, or a more widely used dep (if there even is one)? I'm not saying no, I just want to make sure there is a valid reason behind it and that all options were considered." This section answers it per language with measurements.

### 2.1 What every option must do

Each option is measured against the same job. It starts understudy (image or binary). It publishes the port on loopback only (finding 2). It waits for `/_understudy/v1/health` = 200 (finding 5). It returns `AWS_ENDPOINT_URL` and the dummy credentials as `understudytest.Env` does ([repo] `understudytest/understudytest.go:L237-L245`). It optionally mounts the Docker socket with the right group (finding 6). It stops with a grace period, so understudy drains its children (finding 4). It cleans up when the test process is killed (finding 3). The last one is what separates the options most.

### 2.2 Go

| Option | Stars | Imported by (pkg.go.dev) | Versions since 2025-10-26 | Latest | Open issues / PRs | Non-bot commit authors, 12 months (top author's share) | Licence | Consumer probe: go.mod requires / module graph / linked modules / probe binary bytes |
|---|---|---|---|---|---|---|---|---|
| testcontainers-go | 5,017 | 1,755 | 5 | v0.44.0, 2026-08-07 | 126 / 69 | 39 (82 of 131) | MIT | 51 / 80 / 44 / 9,994,400 |
| ory/dockertest v3 | 4,527 (repo) | 748 | 0 | v3.12.0, 2025-03-12 | 5 / 5 (repo) | 6 (7 of 12) | Apache-2.0 | 27 / 72 / 25 / 8,315,040 |
| ory/dockertest v4 | same repo | 0 | 6 | v4.0.1, 2026-10-19 | same repo | same repo | Apache-2.0 | 23 / 53 / 20 / not built |
| orlangure/gnomock | 1,487 | 37 | 0 | v0.32.0, 2025-06-07 | 23 / 16 | 2 (3 commits in total) | MIT | 35 / 170 / 26 / 9,154,720 |
| fsouza/go-dockerclient | 2,249 | 11,450 | 6 | v1.13.3, 2026-08-24 | 12 / 10 | 3 (12 of 14) | BSD-2-Clause | 18 / 51 / 16 / 7,344,288 |
| moby/moby/client (the Docker SDK) | 72,208 (moby repo) | 1,686 (`docker/docker/client`: 13,321) | 18 | v0.6.2, 2026-10-08 | 3,957 issues and PRs (repo) | not counted | Apache-2.0 | 20 / 49 / 18 / 7,270,560 |
| standard library (`net/http` over the unix socket) | - | - | - | - | - | - | BSD-3-Clause (Go) | 0 / 1 / 0 / 5,886,112 |
| `understudytest` (in process, exists) | - | - | - | - | - | - | Apache-2.0 | 1 / 8 / 1 / not built (test binary) |

All figures are [measured]. Stars and issue counts come from the GitHub API. Issues and PRs are counted separately through the search API, except where marked "repo". Versions and dates come from proxy.golang.org `@v/list` and `.info`. "Imported by" is the count pkg.go.dev shows. pkg.go.dev/about does not say whether imports from `_test.go` files are counted. Testcontainers is mostly imported from tests, so treat this column as a lower bound for test libraries. Commit authors come from `GET /repos/{repo}/commits?since=2025-10-26` on the default branch, with bots excluded. Each probe imports the library and makes one start call (scratch module, `go mod tidy`, `go list -deps`). Probe binary size matters only to a program that links the library into its binary. For test-only consumers it is context.

What the figures say:

- **testcontainers-go is the most-adopted Go library for starting containers from tests.** Its pkg.go.dev count is 2.3 times dockertest v3's and 47 times gnomock's. go-dockerclient and the moby client have higher counts because they are general Docker API clients, not test harnesses. Building a launcher on them is the self-maintained route with an extra dependency.
- **Maintenance.** tc-go shipped 5 releases in 12 months, from 39 authors. It is backed by Docker Inc., which acquired AtomicJar, "the makers of Testcontainers", on 2023-12-11 and stated "no plans to change the licensing structure" ([doc] [Docker blog](https://www.docker.com/blog/docker-whale-comes-atomicjar-maker-of-testcontainers/)). The work is concentrated: one author wrote 82 of the 131 non-bot commits. gnomock shows no release and 3 commits in 12 months. dockertest v3 has had no release since 2025-03. Its v4 is three weeks old, and "Version 4 introduces automatic container reuse" ([doc] dockertest README on the default branch, "Migration from v3"). Reused containers would share understudy state across test runs.
- **Footprint.** A consumer that does not already use tc-go gets 44 linked modules: OpenTelemetry (`otel`, `otel/metric`, `otel/trace`, `otelhttp`, `auto/sdk`), `stretchr/testify` (linked into non-test code), `shirou/gopsutil/v4`, `golang.org/x/crypto`, `sirupsen/logrus` and the moby modules. By licence-file text, 28 of them are Apache-2.0, 8 BSD, 7 MIT and 1 ISC [measured]. None of this reaches understudy's binary, because the module would be a nested module (section 8.1). It reaches the module's consumers, who chose tc-go already.
- **Lighter options give up the cleanup.** dockertest v4 links 20 modules, the moby client 18 and go-dockerclient 16. Each still needs the launcher written on top, and none of them provides Ryuk-style reaping.

### 2.3 Python

| Option | Stars | Downloads, last month / last week (pypistats) | Releases since 2025-10-26 | Latest upload | Open issues / PRs | Non-bot commit authors, 12 months (top share) | Licence | Resolved tree for CPython 3.13 |
|---|---|---|---|---|---|---|---|---|
| testcontainers 4.15.0 | 2,318 | 31,386,155 / 8,711,012 | 9 (4 of them release candidates) | 2026-07-24 | 142 / 55 | 41 (32 of 94) | Apache-2.0 | 10 packages: testcontainers, docker, requests, urllib3, certifi, charset-normalizer, idna, python-dotenv, typing-extensions, wrapt |
| pytest-docker 3.2.6 | 489 | 2,207,210 / 630,324 | 3 | 2026-10-16 | 14 / 8 | 3 | MIT | 7 packages (pytest and its own tree, attrs); shells out to `docker compose` |
| docker (docker-py) 7.2.0 | 7,224 | 129,892,206 / 34,147,469 | 1 | 2026-07-09 | 447 / 129 | 7 | Apache-2.0 | 6 packages: docker, requests, urllib3, certifi, charset-normalizer, idna |
| python-on-whales 0.81.0 (a docker CLI wrapper) | 712 | 6,119,408 / 988,301 | not counted | 2026-03-09 | not counted | not counted | MIT | requires pydantic 2 and typing-extensions (not resolved) |
| standard library (`subprocess` running the binary or the docker CLI) | - | - | - | - | - | - | PSF | 0 |

All figures are [measured] (pypi.org JSON API, pypistats.org `recent`, GitHub API, `uv pip compile`). In the testcontainers tree, certifi is MPL-2.0 (file-level copyleft, which ADR 0016 accepts test-only) and typing-extensions is PSF-2.0. The rest are Apache-2.0, MIT or BSD [measured: PyPI licence fields]. docker-py's downloads include every testcontainers install, since testcontainers depends on it (inferred from the tree above).

- **testcontainers-python leads its category by an order of magnitude**: 14 times pytest-docker's monthly downloads. pytest-docker needs a compose file and the `docker compose` plugin, and has no reaper ([doc] pytest-docker README on the default branch). docker-py is the client testcontainers wraps, not a harness.
- **The self-maintained route needs a binary distribution that does not exist yet.** A stdlib launcher that runs `understudy serve --listen 127.0.0.1:0 --ready-file F` and reads the endpoint is about 80 lines with no dependency ([repo] `docs/ARCHITECTURE.md` 17, `--ready-file`). But Python users have no way to get the binary from PyPI. GitHub releases and Homebrew are what ROADMAP item 4 plans. A launcher over the docker CLI has no reaper.

### 2.4 JavaScript

| Option | Stars | Downloads, last month / week 2026-10-16 to 2026-10-22 | npm versions since 2025-10-26 | Latest | Open issues / PRs | Non-bot commit authors, 12 months (top share) | Licence | Resolved tree |
|---|---|---|---|---|---|---|---|---|
| testcontainers 12.2.1 | 2,631 | 28,835,516 / 5,145,502 | 16 | 2026-10-13 | 66 / 14 | 27 (106 of 154) | MIT | 154 package versions (135 names), including dockerode, docker-compose, undici, archiver, the ssh2 chain with optional native `cpu-features` and `nan`; `engines.node >= 22.22` |
| dockerode 5.0.1 | 4,953 | 32,921,939 / 5,852,903 | 5 | 2026-06-24 | 20 / 8 | 4 (24 of 27) | Apache-2.0 | 66 package versions (65 names), the same ssh2 chain |
| docker-compose 1.5.0 (PDMLab, a CLI wrapper) | 224 | 29,738,852 / 5,291,272 | 7 | 2026-09-17 | 20 / 1 | 3 | MIT | 2 packages; needs the `docker compose` CLI |
| `node:child_process` | - | - | - | - | - | - | MIT (Node) | 0 |

All figures are [measured] (registry.npmjs.org, api.npmjs.org, GitHub API, pnpm lockfile resolution). Of the 154 package versions in the testcontainers tree, 96 are MIT, 21 ISC, 17 Apache-2.0, 12 BSD-3-Clause, 4 BlueOak-1.0.0 and 1 Unlicense (tweetnacl). Three have no licence field in their registry metadata: ssh2, cpu-features and buildcheck [measured]. testcontainers 12.2.1 declares dockerode and docker-compose as direct dependencies [measured: registry metadata], so their download counts include every testcontainers install (inferred).

- **testcontainers-node is the only harness in the field.** dockerode and docker-compose are building blocks that testcontainers itself uses.
- **Its tree is heavy**: 154 packages, a Node floor of 22.22 (tc-node `packages/testcontainers/package.json:L59`), and an optional native addon chain. That cost falls only on users who did not already pick testcontainers.

### 2.5 What a Testcontainers module buys a consumer, and what self-maintaining would cost

What the libraries provide that a launcher would otherwise have to build:

- **Integration with the suites users already have.** Go has `testcontainers.CleanupContainer(tb, ctr)` (tc-go `testing.go:L99`). Python has context managers. Node has `await using` and the testcontainers-node conventions (tc-node `.agents/skills/add-module/SKILL.md:L33`).
- **Ryuk session reaping.** It survives `kill -9` of the test process: resources matching the session filter are removed 10 s after the last connection closes (ryuk `README.md:L75`).
- **Networks.** An application container under test can reach understudy by alias, which no in-process or binary launcher offers.
- **Compose interop.** All three libraries have a compose module or class (tc-go `modules/compose`, tc-py `src/testcontainers/compose`, tc-node `docker-compose-environment`).
- **Daemon discovery.** DOCKER_HOST, contexts, Docker Desktop and the properties file are handled (tc-go `internal/core/docker_host.go:L103-L121`).
- **Pull policies, registry auth, log capture into failure messages** (tc-py `container.py:L279-L286`).

A module adds no dependency for its intended consumer, because someone choosing a Testcontainers module already depends on Testcontainers. That is the valid reason for the library choice. The module's audience is defined by the library, and no other library has a larger audience in any of the three languages (sections 2.2 to 2.4).

What self-maintaining would cost:

- **Go container launcher.** `internal/docker` already has create, start, inspect, remove, pull, `PortBindings` on `127.0.0.1:0` and labels. It is 2,047 non-test lines ([repo] `internal/docker/*.go`). Its engine test already checks a 127.0.0.1 ephemeral binding (`internal/docker/engine_docker_test.go:L166`). A public package in the main module could reuse it with about 200 to 300 lines plus a `GroupAdd` field. It would have no reaper. A `go test -timeout` panic exits the process without running cleanups (inferred), which leaves the container. Its audience overlaps `understudytest` except for the shared-network case, and that case is exactly the Testcontainers user.
- **Python and JS binary launchers.** About 80 lines each, no dependency. They need the binary distributed to those ecosystems: platform wheels on PyPI and per-platform npm packages, the pattern ruff and esbuild use, which is release engineering understudy does not have yet. If the test runner is killed, the `serve` process is orphaned, because understudy has no parent-death handling ([repo] no `Pdeathsig`, parent-process or stdin-EOF handling in `cmd/` or `internal/server`). Its own children are still swept by the next `serve` on the same machine ([repo] `internal/server/runs.go`).
- **Maintenance avoided.** The Docker host and Desktop discovery logic, socket-path discovery (tc-go `internal/core/docker_host.go:L179-L215`) and a reaper would otherwise have to be written and maintained.

### 2.6 Recommendation per language

- **Go.** Keep `understudytest` as the recommended way for Go tests (finding 1). Ship a thin testcontainers-go module only if the maintainer approves the dependency for the nested module. It serves existing tc-go users who need container networking, and its maintenance cost is following tc-go's v0 minors (finding 8). Do not build a stdlib container launcher: its only distinct audience is the tc-go audience.
- **Python.** Ship `testcontainers-understudy` on testcontainers-python. The options with fewer dependencies either need a binary distribution understudy lacks (subprocess) or lack cleanup (pytest-docker, the docker CLI).
- **JavaScript.** Ship `testcontainers-understudy` on testcontainers-node, with `testcontainers` as a peer dependency (section 8.10), so the 154-package tree stays the user's own choice.

### 2.7 If the maintainer declines the dependencies

The cheapest honest alternative publishes no package. It has three parts:

1. Make the understudy-side change in section 9.3 (child labels). It is needed for clean Ryuk teardown whatever ships.
2. Add a runnable example per language under `examples/` (`testcontainers-go`, `testcontainers-python`, `testcontainers-node`). Each is a plain `GenericContainer` recipe of about 15 lines with the settings in section 8. The examples are checked by `examples/check.sh` and the existing `examples` CI job ([repo] `docs/ARCHITECTURE.md` 19), and each declares its dependency in its own manifest, as the conformance suites do (ADR 0016, "Conformance suites are separate modules").
3. Add a guide page linking the examples and the `--ready-file` recipe for runs without Docker ([repo] `docs/guide/ci.md`, "Parallel jobs on one machine").

Users copy the recipe into the Testcontainers setup they already have. understudy maintains no API and adds no dependency.

## 3. understudy's side of the contract

### 3.1 Readiness

`GET /_understudy/v1/health` answers 503 with `{"ready": false, "waiting": [...]}` until every service is constructed and the listener accepts, then 200 ([repo] `docs/ARCHITECTURE.md` 17; `internal/server/admin/health.go:L26-L31`). `admin.Health.Ready` is the field ([repo] `admin/types.go:L31-L36`, type at `L33`). `--ready-file PATH` writes `{"endpoint", "pid"}` once listening. With `--listen 127.0.0.1:0` it gives each parallel job its own port ([repo] `docs/ARCHITECTURE.md` 17; `docs/guide/ci.md`). Once ready, the health body also reports `docker` and `dynamodbLocal` (`{"status": "enabled"|"disabled"|"degraded", "reason"}`, [repo] `admin/types.go:L40-L43,L87-L97`). Each health call pings Docker and reports `degraded` with the error when the daemon does not answer ([repo] `internal/server/containers.go:L480-L490`). That is how a module can confirm a socket mount works (section 8.5).

### 3.2 The image

`FROM scratch` with CA certificates, `USER 65532:65532`, `VOLUME /data`, `EXPOSE 4566`, `ENTRYPOINT ["/understudy"]`, `CMD ["serve"]`, and `HEALTHCHECK --interval=5s --timeout=2s --start-period=3s CMD ["/understudy", "health"]` ([repo] `Dockerfile:L54-L66`). There is no shell, so only HTTP and health-check waits work, which matches tc-node's rule "Shell-less images need a health-check or HTTP wait" (tc-node `.agents/skills/add-module/SKILL.md:L22`). The release image is `ghcr.io/sustinbebustin/understudy`, tagged `{{ .Version }}` and, except for a prerelease, `latest`, for linux/amd64 and linux/arm64. It is not published yet ([repo] `.goreleaser.yaml:L48-L70`; `README.md:L14`). Local builds are `understudy:dev` ([repo] `justfile:L91-L92`). `VOLUME /data` gives every container an anonymous volume. tc-go Terminate, tc-py stop, tc-node stop and Ryuk all remove volumes (tc-go `docker.go:L351-L352`; tc-py `container.py:L294`; tc-node `started-generic-container.ts:L129`; ryuk `reaper.go:L30`).

### 3.3 Listener exposure in a container

In a container with its own network namespace, outside Kubernetes, `serve` defaults to `Exposed{0.0.0.0:4566, ContainerPublished}` with no token. The published port is the exposure control ([repo] `docs/ARCHITECTURE.md` 18, 24; `internal/config/draft.go:L372-L397`). With `UNDERSTUDY_ADMIN_TOKEN` the listener is `Exposed{Token}`. The token must be at least 16 characters (`internal/config/draft.go:L330,L355-L361`). It must come with `--allow-unauthenticated-aws-api` (`draft.go:L362-L368`). It then guards every admin endpoint, health included: a health call without it gets 401 ([repo] `internal/server/admin/errors_test.go:L107-L113`). The AWS API is never authenticated by the token ([repo] `docs/configuration.md`, `--allow-unauthenticated-aws-api`).

### 3.4 The Docker socket

Lambda and DynamoDB Local need the Docker Engine. Without a socket in the container, Docker and DynamoDB are "disabled: no Docker socket" and Lambda keeps only its control plane ([repo] `docs/ARCHITECTURE.md` 24; `internal/server/containers.go:L255-L268`). On Linux the documented form is `-v /var/run/docker.sock:/var/run/docker.sock --group-add "$(stat -c %g /var/run/docker.sock)"` ([repo] `README.md:L67-L76`). Mounting the socket grants root on the host to whoever controls understudy ([repo] `README.md:L76`; `docs/ARCHITECTURE.md` 20). For socket mounts, `HostConfig.Binds` creates a missing source path as a root-owned directory, while `Mounts` with `Type: bind` fails ([repo] `docs/research/containers.md` finding 5, 3.3).

### 3.5 Networking from inside a container

`internal/topology` finds understudy's own container through mountinfo IDs, then the hostname. It prefers a user-defined network over `bridge`, and returns `InContainer{Network, Self}` ([repo] `internal/topology/detect.go`, `detectSelf`, `pickNetwork`). Function containers and DynamoDB Local join that network and reach understudy by container IP ([repo] `docs/adr/0011-container-networking.md`, review amendment). Function code reaches the whole admin API in this topology, an exposure accepted as the local Docker trust domain ([repo] ADR 0011, hardening round 3; `README.md`, Limitations).

### 3.6 Cleanup that exists today

Every object understudy creates carries `understudy.dev/managed`, `instance` and `owner` labels, and function labels when it serves a function ([repo] `internal/docker/labels.go`). On SIGINT or SIGTERM, `serve` drains and force-removes its containers ([repo] `docs/ARCHITECTURE.md` 20; `docs/research/containers.md` 9.2, hard bound "a few seconds total"). At startup it sweeps its owner's leftovers. It also removes the leftovers of non-persistent runs whose process is gone, judged only within the same boot and PID namespace ([repo] `internal/server/runs.go`, `gone`). `understudy cleanup --all` removes every managed container except those labeled `understudy.dev/test=1`. It refuses while an understudy answers at the configured endpoint ([repo] `internal/cli/cleanup.go:L67-L71,L146-L167`). A containerized understudy that Ryuk kills therefore leaves its children until someone runs `understudy cleanup --all` from a host with the binary (finding 3).

### 3.7 Admin endpoints a module wraps

`GET /settle?timeout=`, `POST /clock` (`{"op":"advance","by":"90s"}` and the other ops), `POST /reset` (`{}` drops all state; clock and faults are kept unless asked), and `GET /journal/events` ([repo] `docs/ARCHITECTURE.md` 17). The typed Go client is `admin.Client`, standard-library only and experimental until 1.0 ([repo] `admin/doc.go`; `admin/client.go:L86-L388`; ADR 0017).

### 3.8 understudytest

Options: `Seed`, `Clock` (`Running` or `Frozen{At}`), `Docker` (`DockerSocket{Path}` or `DockerDisabled`), `DynamoDB` and `Regions`. It always listens on `127.0.0.1:0`. `Env()` returns `AWS_ENDPOINT_URL`, `AWS_ACCESS_KEY_ID=test`, `AWS_SECRET_ACCESS_KEY=test`, `AWS_REGION=us-east-1` and `AWS_DEFAULT_REGION=us-east-1` ([repo] `understudytest/understudytest.go:L18-L80,L237-L245`). The modules should mirror these names and values, so a test can switch between in-process and container without changes.

## 4. Testcontainers for Go (tc-go v0.44.0)

### 4.1 Module conventions

- **Hosting.** A community module lives in the author's own repository and is listed by a PR to the community module registry (tc-go `docs/modules/index.md:L16-L28`). Production modules must not bundle client SDKs; "keep those dependencies in tests/examples only" (`L101-L105`).
- **API shape.** A public `Container` type embeds `testcontainers.Container` (`L117-L118`). The entry point is `func Run(ctx context.Context, img string, opts ...testcontainers.ContainerCustomizer) (*Container, error)` (`L142`), so the image is always passed explicitly.
- **Pattern.** The LocalStack module builds `moduleOpts`, appends the user's options and calls `testcontainers.Run` (tc-go `modules/localstack/localstack.go:L73-L111`). It always mounts the socket by replacing `hostConfig.Binds` (`L90-L92`), and waits `wait.ForHTTP("/_localstack/health").WithPort("4566/tcp").WithStartupTimeout(120*time.Second)` (`L88`). The DynamoDB Local module waits `wait.ForListeningPort` (tc-go `modules/dynamodb/dynamodb.go:L22-L28`).
- **Test SDKs leak into the module graph.** The LocalStack module's go.mod requires aws-sdk-go v1 and v2 for its tests (tc-go `modules/localstack/go.mod:L7-L13`), which then appear in consumers' module graphs.

### 4.2 Wait strategies

- **`wait.ForHTTP(path)` defaults.** It defaults to a status matcher of `status == http.StatusOK` (tc-go `wait/http.go:L65-L66,L151`). It offers `WithStatusCodeMatcher`, `WithResponseMatcher(func(io.Reader) bool)` and `WithHeaders` (`L88,L93,L121`). It polls every 100 ms with a 60 s default timeout (`wait/wait.go:L59-L65`).
- **Failing fast.** It checks the container state before every attempt and fails at once if the container exited or was OOM-killed (`wait/http.go:L318-L325`, `wait/wait.go:L37-L57`).
- **Loopback address.** It dials `target.Host(ctx)` (`wait/http.go:L184`). `WithForcedIPv4LocalHost()` replaces `localhost` with `127.0.0.1` (`L144`).
- **`wait.ForHealthCheck()`** polls `State.Health.Status == healthy` (`wait/health.go:L55,L90`).

### 4.3 Port binding

`HostConfigModifier` runs before the bindings are merged (tc-go `lifecycle.go:L516,L546`). `mergePortBindings` keeps a binding the modifier set for an exposed port and adds `{HostPort: "0"}` with no host IP only when there is none (`L626-L650`). A binding for a port outside the exposed set is dropped; with no exposed ports requested, the set is the image's `EXPOSE` list (`L527-L538`), so the module exposes `4566/tcp` itself. So `hc.PortBindings["4566/tcp"] = []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "0"}}` in a modifier is preserved. `HostIP` is a `netip.Addr`, not a string, in moby `api` v1.55.0, the version tc-go v0.44.0 requires (moby `api@v1.55.0` `types/network/port.go:L164-L169`). Modifiers chain in call order (`options.go:L105-L121`).

### 4.4 Socket path

`testcontainers.MustExtractDockerSocket(ctx)` is the public entry point (tc-go `testcontainers.go:L28-L30`). It wraps `internal/core`, which a module outside the tc-go repository cannot import. It returns the daemon-side socket path: from the properties file or `TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE`, `/var/run/docker.sock` for a `tcp://` host or Docker Desktop, otherwise the unix path of the discovered host (tc-go `internal/core/docker_host.go:L103-L121,L179-L215`). It panics when no daemon is found (`L115`, `L206`).

### 4.5 Labels, Ryuk and termination

- **Default labels.** Every container gets `org.testcontainers=true`, `org.testcontainers.lang=go`, `org.testcontainers.version`, `org.testcontainers.sessionId` (camel case), and `org.testcontainers.reap=true` while Ryuk is enabled (tc-go `internal/core/labels.go:L14-L50`). User labels under `org.testcontainers` are refused (`L61-L67`).
- **What Ryuk reaps.** The handshake sends all default labels joined by `&` in one filter line, so a resource must carry all five to be reaped (`reaper.go:L556-L566`).
- **Public API.** `testcontainers.GenericLabels()` returns that set (`generic.go:L103-L105`) and `testcontainers.SessionID()` returns the session ID (`testcontainers.go:L53-L55`).
- **Termination.** Terminate stops with a 10 s default timeout, then removes with volumes (`cleanup.go:L24-L28`, `docker.go:L328-L352`).

### 4.6 Versioning

- **Tag prefix.** A module in a subdirectory is tagged with that subdirectory as a prefix: "The version `v0.4.0` of that module must have the tag named `gopls/v0.4.0`" ([doc] [go.dev/ref/mod, Mapping versions to commits](https://go.dev/ref/mod#vcs-version)). Nested modules are excluded from the parent module's zip ([doc] same page, module zip files).
- **tc-go's practice.** Each module is tagged `modules/<name>/vX.Y.Z` in lockstep with the core (tc-go `RELEASING.md:L134,L160-L167`). Each module requires the core and replaces it with `../..` for development (`modules/localstack/go.mod:L16,L86`). A replace directive applies only in the main module, so consumers ignore it ([doc] go.dev/ref/mod, replace directive).

## 5. Testcontainers for Python (tc-py 4.15.0)

### 5.1 Conventions and packaging

- **In-repo modules.** They live under `testcontainers.community.<name>`, after the 4.15 move: `testcontainers.localstack` now warns "deprecated, use testcontainers.community.localstack instead" (tc-py `src/testcontainers/localstack.py:L1-L11`). Their client SDKs are optional extras (tc-py `pyproject.toml`, `[project.optional-dependencies]`).
- **Contributing upstream.** A new container needs an issue first (tc-py `docs/contributing.md:L36-L46`). "Community modules are supported on a best-effort basis", and their changes never bump the major version (`L77-L82`).
- **Core package.** Its dependencies are `docker`, `urllib3`, `wrapt`, `typing-extensions` and `python-dotenv`, with `requires-python >=3.10` and licence Apache-2.0 (`pyproject.toml:L1-L9,L37-L43`).
- **LocalStack pattern.** The module waits on the log line `Ready.` (tc-py `src/testcontainers/community/localstack/__init__.py:L89-L92`) and mounts no socket.

### 5.2 Wait strategies

- **`HttpWaitStrategy(port, path)`.** Accepted status codes default to `{200}`, and `for_status_code` adds to the set (tc-py `src/testcontainers/core/wait_strategies.py:L181-L233`). A response predicate runs on the body only after an accepted status (`L357-L371`). A 503 surfaces as `HTTPError` and is retried, not accepted (`L428-L435`).
- **Polling.** Defaults are 120 tries at 1 s, so 120 s (tc-py `src/testcontainers/core/config.py:L103-L104,L161`; `waiting_utils.py:L77-L78`).
- **Exit detection.** The URL is built once, through `get_exposed_port`, which waits for status `running` (`container.py:L328-L331`). The polling loop does not re-check the container (`wait_strategies.py:L373-L390`). A container that exits after that first check costs the full timeout (inferred from the source).
- **`HealthcheckWaitStrategy`** requires a HEALTHCHECK and polls `State.Health` (`L439-L500`).

### 5.3 Ports, kwargs and the configure hook

- **Ports.** `self.ports` is typed `dict[str, Optional[int]]` (tc-py `container.py:L118`), and `with_bind_ports` takes only a host port (`L165-L201`). The underlying `DockerClient.create` annotates `ports` as accepting `tuple[str, int]` (tc-py `src/testcontainers/core/docker_client.py:L288`). docker-py turns a 2-tuple into `HostIp` and `HostPort` (docker-py `docker/utils/utils.py:L85-L110`). So `('127.0.0.1', 0)` binds loopback with an ephemeral port, but only through a value that does not match `self.ports`' annotation.
- **kwargs.** `with_kwargs` replaces `self._kwargs` wholesale (`container.py:L230-L231`), and `_kwargs` is splatted into create after `ports=` (`L263-L273`). The `_configure()` hook runs inside `start()` (`L250,L426`), and 23 definitions under `src/testcontainers/community` override it. The ollama module merges `self._kwargs = {**self._kwargs, ...}` (tc-py `src/testcontainers/community/ollama/__init__.py:L102-L105`), but from `__init__` (`L100`), not from `_configure()`, so a later `with_kwargs` call discards the merge (inferred from `container.py:L230-L231`). A merge inside `_configure()` survives it.
- **Image pulls.** `create` pulls the image only when it is missing locally (`docker_client.py:L300-L303`).

### 5.4 Labels, Ryuk and stop

- **Labels.** `org.testcontainers.session-id` (hyphenated) is set to a per-process UUID. The `org.testcontainers` namespace is reserved (tc-py `src/testcontainers/core/labels.py:L7-L35`).
- **What Ryuk reaps.** Ryuk is sent `label=org.testcontainers.session-id=<SESSION_ID>` (tc-py `container.py:L541`). It is mounted at the client-side socket path or `TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE` (`config.py:L32-L48,L163-L167`; `container.py:L498`).
- **Stop.** `stop()` is `self._container.remove(force=force, v=delete_volume)` with `force=True` (`container.py:L292-L294`): SIGKILL, with no drain.

## 6. Testcontainers for Node (tc-node 12.2.1)

### 6.1 Conventions and packaging

- **Monorepo.** Modules are npm workspaces under `packages/modules/*`, published as `@testcontainers/<name>` in lockstep with the core (`npm version "$VERSION" --ws`, tc-node `.github/workflows/npm-publish.yml:L55`). The LocalStack module depends on `testcontainers ^12.2.0` as a regular dependency (tc-node `packages/modules/localstack/package.json:L33`).
- **Module rules.** The constructor sets ports, the wait strategy and `withStartupTimeout(120_000)`. HTTP or health-check waits are preferred over log regexes. Zero config must work. Client libraries go in `devDependencies`. "Every test does a real client round trip". New modules are listed through the community registry (tc-node `.agents/skills/add-module/SKILL.md:L17-L43`).
- **LocalStack precedent.** The module passes `LAMBDA_DOCKER_FLAGS: -l org.testcontainers.session-id=<reaper.sessionId>`, so Ryuk reaps LocalStack's Lambda containers (tc-node `packages/modules/localstack/src/localstack-container.ts:L37-L48`). It does this in `beforeContainerCreated` (`L50-L53`).

### 6.2 Wait strategies

- **Options.** `Wait.forHttp(path, port, {abortOnContainerExit})` takes `abortOnContainerExit`, which defaults to false (tc-node `packages/testcontainers/src/wait-strategies/wait.ts:L32-L38`). There are also `forStatusCode`, `forResponsePredicate` and `withHeaders` (`http-wait-strategy.ts:L30-L52`).
- **Defaults.** With no predicate, any 2xx (`response.ok`) passes (`L123-L124`). The retry interval equals the 1 s read timeout (`L20,L88`), and the timeout defaults to 60 s (`wait-strategy.ts:L15`).
- **Host.** The URL uses `client.info.containerRuntime.host`, often the name `localhost` (`http-wait-strategy.ts:L91`).
- **`Wait.forHealthCheck()`** exists (`wait.ts:L24-L26`).

### 6.3 Ports and host config

- **No host IP.** `PortWithBinding` has no host-IP field (tc-node `packages/testcontainers/src/utils/port.ts:L1-L7`). `withExposedPorts` writes `PortBindings` with only `HostPort` (`generic-container.ts:L408-L438`).
- **Protected config.** `hostConfig` and `createOpts` are protected, so a subclass can set them (`L52-L53`).
- **Mapped port lookup.** It matches each looked-up address family of the runtime host against the binding's host IP, so a `127.0.0.1` binding is found even when `localhost` resolves to `::1` first (`utils/bound-ports.ts:L87-L99`).
- **Bind mounts.** `withBindMounts` replaces `hostConfig.Binds` with `source:target:mode` strings (`generic-container.ts:L448-L452`).

### 6.4 Labels, Ryuk and stop

- **What Ryuk reaps.** Ryuk is sent `label=org.testcontainers.session-id=<id>` per session (tc-node `packages/testcontainers/src/reaper/reaper.ts:L186-L188`). It is mounted at `client.info.containerRuntime.remoteSocketPath` (`L98-L110`).
- **Stop.** Stop defaults to `{ timeout: 0, removeVolumes: true }` with the timeout in milliseconds (`generic-container/started-generic-container.ts:L129-L132`; `container-runtime/clients/container/docker-container-client.ts:L142-L145`).

## 7. Ryuk (0.14.0)

- **What it does.** Ryuk removes "containers, networks, volumes and images by given filter after specified delay" (ryuk `README.md:L3`). Several `label=` terms on one line form one filter (`L36-L40`, `L55`). Cleanup runs 10 s (`RYUK_RECONNECTION_TIMEOUT`) after the last connection closes (`L75`).
- **Timing guard.** Resources created after the prune pass started (offset `RYUK_RETRY_OFFSET`, -1 s) trigger a retry instead of removal (`L78`).
- **Removal.** Containers are removed with `{RemoveVolumes: true, Force: true}` (ryuk `reaper.go:L30,L673-L691`). A resource that is already gone is skipped: a not-found error is logged at debug level and not counted as a failure (`L733-L737`). So Ryuk and understudy's own drain can both try a removal without failing (inferred).
- **tc-py's Ryuk.** tc-py 4.15.0 runs 0.8.1 (0.2). That version also removes containers with `{RemoveVolumes: true, Force: true}` (ryuk 0.8.1 `main.go:L277`), and cleans up 10 s after the last connection by default (`README.md:L42`, `main.go:L239`). It has no `RYUK_RETRY_OFFSET` guard (absent from its README and `main.go`). Finding 3 holds under either version.
- **Size.** The 0.14.0 amd64 image is 2,402,816 bytes [measured: Docker Hub tag API].

## 8. Module design

### 8.1 Layout and names

| | Go | Python | JavaScript |
|---|---|---|---|
| Directory | `testcontainers/go` (module root), package directory `testcontainers/go/understudy` | `testcontainers/python` | `testcontainers/node` |
| Published name | module `github.com/sustinbebustin/understudy/testcontainers/go`, import `.../testcontainers/go/understudy`, package `understudy` | distribution `testcontainers-understudy`, import `testcontainers_understudy` (precedent `testcontainers-spicedb`, finding 9) | `testcontainers-understudy` (free, finding 9), or a scope the maintainer owns (precedent `@microcks/microcks-testcontainers`) |
| Types | `Container` embedding `testcontainers.Container`; `Run` (tc-go conventions, 4.1) | `UnderstudyContainer(DockerContainer)` | `UnderstudyContainer extends GenericContainer`, `StartedUnderstudyContainer extends AbstractStartedContainer` (LocalStack pattern, tc-node `localstack-container.ts:L13,L60`) |
| Tooling | its own go.mod, lint and `go test` | uv, as `conformance/python-boto3` | Node and npm for tests, since tc-node declares `engines.node` (Bun compatibility is UNVERIFIED, TC-05) |

Nested modules in this repository follow the conformance-suite precedent: separate manifests and pins, outside the root module's depguard ([repo] ADR 0016; `.github/workflows/ci.yml` conformance matrix). The package directory under a `go` module root keeps the package name equal to its directory. A module root named `go` cannot hold a package named after its directory, because `go` is a keyword.

### 8.2 API

| Concern | Go | Python | JavaScript | What it sets |
|---|---|---|---|---|
| Start | `understudy.Run(ctx, img, opts...)` | `UnderstudyContainer(image=DEFAULT_IMAGE)`, `with ... as u` | `await new UnderstudyContainer(IMAGE).start()` | the settings below |
| Endpoint | `Endpoint(ctx) (string, error)` | `get_endpoint()` | `getEndpoint()` | `http://<host>:<mapped 4566>`, with `localhost` mapped to `127.0.0.1` so SDKs address S3 path style ([repo] `docs/guide/sdks.md:L5`) |
| Environment | `Env(ctx) (map[string]string, error)` | `get_env()` | `getEnv()` | the five variables of `understudytest.Env` (3.8) |
| Docker socket | `WithDockerSocket()`, `WithDockerSocketGroup(gid string)` | `with_docker_socket(gid=None)` | `withDockerSocket(gid?)` | socket mount and `GroupAdd` (8.5) |
| Clock | `WithClock(Frozen{At}) / WithClockScale(f)` | `with_clock(frozen_at=..., scale=...)` | `withClock({frozenAt, scale})` | `UNDERSTUDY_CLOCK`, `UNDERSTUDY_CLOCK_SCALE` |
| Seed | `WithSeed(uint64)` | `with_seed(int)` | `withSeed(bigint)` | `UNDERSTUDY_SEED` |
| SigV4 | `WithSigV4Verify()` | `with_sigv4_verify()` | `withSigV4Verify()` | `UNDERSTUDY_SIGV4=verify` |
| Snapshot | `WithSnapshot(path)` | `with_snapshot(path)` | `withSnapshot(path)` | copies the archive in and sets `UNDERSTUDY_LOAD_SNAPSHOT` |
| Admin token | `WithAdminToken()` | `with_admin_token()` | `withAdminToken()` | random 32-byte hex `UNDERSTUDY_ADMIN_TOKEN`, `UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API=true`; the wait and the helpers send `Authorization: Bearer` (3.3) |
| Publish address | `WithPublishedOn(netip.Addr)` | `with_published_on(addr)` | `withPublishedOn(addr)` | the `HostIp`; anything but loopback requires the admin token |
| Helpers | `Settle(ctx, timeout)`, `AdvanceClock(ctx, d)`, `Reset(ctx)`, `Admin(ctx) (*admin.Client, error)` | `settle(timeout)`, `advance_clock(by)`, `reset()` | `settle(timeoutMs)`, `advanceClock(by)`, `reset()` | the 3.7 endpoints |

Every other understudy setting with a `UNDERSTUDY_*` variable is reachable through each library's generic env option, so the modules add no option for it. Persistence (`UNDERSTUDY_PERSIST`) is left to that path. tc-node's rule "no speculative options" applies (tc-node `.agents/skills/add-module/SKILL.md:L27`). The Python and JS helpers are small standard-library HTTP calls (`urllib.request`, `fetch`), so they add no dependency. Go uses `admin.Client` (8.10).

### 8.3 Wait strategy

- **Go.** `wait.ForHTTP("/_understudy/v1/health").WithPort("4566/tcp").WithForcedIPv4LocalHost()`, with a response matcher that decodes `{"ready": true}`. Add `WithHeaders` when the token is set.
- **Python.** A subclass of `HttpWaitStrategy(4566, "/_understudy/v1/health")` with a `"ready": true` predicate, a 0.1 s poll interval, and a container-status check on every poll (5.2 gap).
- **Node.** `Wait.forHttp("/_understudy/v1/health", 4566, { abortOnContainerExit: true }).forStatusCode(200).forResponsePredicate(...)`.
- **Timeout.** 60 s in all three, set explicitly. It is the tc-go and tc-node wait default (4.2, 6.2). tc-py defaults to 120 s (5.2), and tc-node's module rule asks for `withStartupTimeout(120_000)` (6.1), so the Python and Node modules each depart from a library default or rule on purpose. understudy is ready in about 20 ms, so the timeout only bounds image pulls and failures.
- **Not the HEALTHCHECK.** Its first run is at 5 s (finding 5).

### 8.4 Port binding and exposure

- **Go.** The module exposes `4566/tcp`, and a `HostConfigModifier` sets `PortBindings["4566/tcp"] = [{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "0"}]` (4.3).
- **Python.** `_configure()` sets `self.ports["4566/tcp"] = ("127.0.0.1", 0)`, with a `# SAFETY:` comment naming docker-py's tuple form against the narrower annotation (5.3).
- **Node.** `beforeContainerCreated()` sets `this.hostConfig.PortBindings["4566/tcp"] = [{ HostIp: "127.0.0.1", HostPort: "0" }]` after any `withExposedPorts` call (6.3).
- **Remote daemons.** A loopback binding is on the daemon's host: "only the Docker host can access the published container port" ([doc] [port publishing](https://docs.docker.com/engine/network/port-publishing/)). With a daemon on another host, the user must choose `WithPublishedOn` beyond loopback, which requires `WithAdminToken`. The module refuses beyond-loopback publishing without the token, with the text in 9.5. Testcontainers Cloud's port forwarding of loopback-bound ports is UNVERIFIED (TC-06).
- **Same-network clients.** Containers on the same Docker network as understudy reach it by alias on 4566 whatever the binding, the same trust as the compose example ([repo] `examples/docker-compose/compose.yaml`).

### 8.5 Docker socket and group

- **Mount source.** Use the library's daemon-side socket path: tc-go `MustExtractDockerSocket` (4.4), tc-py `testcontainers_config.ryuk_docker_socket`, which is the client-side path unless `TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE` is set (5.4), so it is daemon-side only for a local daemon, tc-node `client.info.containerRuntime.remoteSocketPath` (6.4).
- **Mount type.** Mount it with `Mounts` `Type: bind`, never `Binds`, so a wrong path fails instead of creating a root-owned directory (3.4). Append; do not replace the user's mounts, which the LocalStack module and tc-node's `withBindMounts` do (4.1, 6.3).
- **Group.** On a local unix daemon, add the socket's numeric GID from `stat` (Go `syscall.Stat_t.Gid`, Python `os.stat().st_gid`, Node `fs.statSync().gid`). A group name cannot resolve in a `scratch` image with no `/etc/group` (inferred). Otherwise require `WithDockerSocketGroup(gid)`, with the text in 9.5. On Docker Desktop, whose `GET /info` reports `OperatingSystem` `Docker Desktop`, add group 0 instead: a container sees the VM's socket, `uid=0 gid=0 mode=660`, not the host's, which belongs to the user's group, so the host `stat` gives a group that cannot use it (TC-01, recorded 2026-11-06).
- **Check after start.** Read `/health` and fail with its `docker.reason` unless `docker.status` is `enabled`. A socket that cannot be used must never pass silently (3.1, 9.7).
- **Fallback.** Running the container as `0:0` avoids the GID question. The socket already grants root on the host (3.4), but this drops the image's non-root default, so it is a documented fallback, not the default.

### 8.6 Cleanup: child labels and stop grace

- **Child labels.** Set the understudy setting from 9.3 to the session's Ryuk filter labels. In Go that is `testcontainers.GenericLabels()`, all five labels (4.5). In Python it is `{LABEL_SESSION_ID: SESSION_ID}` (5.4). In Node it is `{LABEL_TESTCONTAINERS_SESSION_ID: (await getReaper(client)).sessionId}`, as the LocalStack module does (6.1).
- **Stop grace.** The Python module overrides `stop()` to call `self._container.stop(timeout=10)` before `remove(v=True)`. The Node module defaults `stop()` to `{ timeout: 10_000 }`. Go keeps Terminate's 10 s default (4.5). Whether 10 s covers a drain with warm functions and DynamoDB Local is UNVERIFIED (TC-03).
- **No reuse.** The modules never enable container reuse: reuse would carry state between tests. `Reset` is the supported way to clear it.

### 8.7 Tests against a local image

- **Image selection.** Every module's tests read `UNDERSTUDY_IMAGE`, defaulting to `understudy:dev` from `just image` ([repo] `justfile:L91-L92`). Every library uses a local image when present (tc-py `docker_client.py:L300-L303`; tc-go pulls only when `AlwaysPullImage` is set or the local inspect fails, `docker.go:L1220-L1233`; tc-node's default policy `shouldPull()` returns false, `packages/testcontainers/src/utils/pull-policy.ts:L5-L9`).
- **Round trips.** Each test does a real client round trip: SQS create, send and receive. A Lambda case runs with the socket mounted. A teardown case asserts no `understudy.dev/managed` container of that instance remains after stop.
- **Sealed.** Tests run through `scripts/sealed` ([repo] CLAUDE.md invariant 1) and pass the container endpoint to each client explicitly.
- **CI.** One job builds the image once and runs the three suites, like the conformance matrix ([repo] `.github/workflows/ci.yml`). Ryuk's image is pulled from Docker Hub there (7).

### 8.8 Versioning and release

- **Lockstep.** All three modules release with understudy: understudy `vX.Y.Z` tags `testcontainers/go/vX.Y.Z` (4.6), PyPI `testcontainers-understudy==X.Y.Z` and npm `testcontainers-understudy@X.Y.Z`. The default image is `ghcr.io/sustinbebustin/understudy:X.Y.Z`. This is tc-go's and tc-node's lockstep practice (4.6, 6.1).
- **Image constants.** Python defaults its image argument; Go and Node take it explicitly, by their conventions (4.1, 6.1), and export `DefaultImage` and `UNDERSTUDY_IMAGE` constants.
- **Order.** Nothing is published before the GHCR image exists ([repo] ROADMAP item 4). The Go module requires a tagged root version for `admin`; in-repo development uses `replace => ../..` as tc-go does (4.6).

### 8.9 Registry listing

After the first release, a PR to the community module registry adds `modules/understudy/index.md`, with `go`, `python` and `nodejs` entries, `maintainer: official` and category `cloud`. These are values the registry lists (registry `README.md:L50-L60`). The README does not define `official`. Reading it as the value for vendor-published modules is inferred from its use for microcks' own repositories (registry `modules/microcks/index.md:L26-L46`).

### 8.10 Dependencies each module adds

Each needs the maintainer's approval.

- **Go: `github.com/testcontainers/testcontainers-go v0.44.0`** (MIT). It brings 44 linked modules for a consumer not already on it (2.2). `github.com/moby/moby/api` becomes a direct requirement, but it is already in tc-go's graph, and the module imports it for `container.HostConfig` (4.1).
- **Go: `github.com/sustinbebustin/understudy`** for `admin`. It links one module, but it sets the module's floor to Go 1.27 (finding 7) and puts understudy's go.mod requirements (rapid, go-cmp, xxhash, xxh3, cpuid, x/sys) in consumers' module graphs as go.mod-only entries [measured: the `understudytest` probe's 8-entry graph]. The alternative is about 60 lines of private HTTP calls duplicating admin wire types. That keeps a Go 1.25 floor, at the cost of drift caught only by the module's tests.
- **Go: test-only `aws-sdk-go-v2/service/sqs`** at the versions `conformance/go-sdk` pins. It appears in consumers' module graphs, as LocalStack's SDK requirements do (4.1).
- **Python: `testcontainers>=4.15,<5`** (Apache-2.0), 10 packages for a new consumer (2.3). Dev group: pytest and boto3, as `conformance/python-boto3` pins. A build backend (uv's or hatchling) is build-time only.
- **JavaScript: `peerDependencies: {"testcontainers": "^12.2.1"}`.** A peer keeps one copy, and so one reaper session, when the user's own testcontainers satisfies the range (inferred from npm's peer semantics). tc-node's in-repo modules use a regular dependency because they release in lockstep with it (6.1). Dev: testcontainers, typescript and `@aws-sdk/client-sqs`. The `node:test` runner avoids a test-runner dependency.

## 9. Implications for understudy

### 9.1 Operations and coverage

None. This work changes no AWS operation, so no operation becomes Full, Partial or StoredOnly or leaves 501, and `docs/coverage.md` is unaffected.

### 9.2 Fidelity switches

None. The modules start the same binary with the same defaults, so they must introduce no deviation from AWS. Any `UNDERSTUDY_*` value they set is visible in the container's environment.

### 9.3 understudy change required: labels for child containers

Add a setting that adds labels to every container and network understudy creates: function environments, DynamoDB Local, the Docker Desktop probe and any network. It needs an env form, because modules configure the container through env. Proposed: `UNDERSTUDY_DOCKER_LABELS` as a JSON object, and file key `dockerLabels`. The name is to settle at implementation.

- **Validation.** Keys under `understudy.dev/` are refused ("is reserved for understudy's own labels; choose a key outside understudy.dev/"). Values are strings. Unknown shapes are refused by name, like every other setting ([repo] `docs/configuration.md`, strict loading).
- **Sweep unaffected.** Selectors use only `understudy.dev/*` ([repo] `internal/docker/labels.go`), so added labels never change what a sweep removes.
- **Grounds.** Ryuk's filter semantics (4.5, 5.4, 6.4, 7) and the tc-node LocalStack precedent (6.1).
- **Docs.** It is user-visible: `docs/configuration.md` must carry the new `serve --help` text verbatim (a test checks), and the guide gets a Testcontainers page with commands run against a build ([repo] CLAUDE.md, Changing things).

### 9.4 Module decisions

| Decision | Value | Grounds |
|---|---|---|
| Wait | HTTP 200 on `/_understudy/v1/health`, body `ready: true`, exit-aware | 3.1, 8.3, finding 5 |
| Binding | `127.0.0.1`, ephemeral; beyond loopback only with the admin token | 3.3, 8.4, finding 2 |
| Endpoint host | `127.0.0.1` when the library reports `localhost` | 8.2 |
| Socket | opt-in; `Mounts` bind; numeric GID; post-start health check | 3.4, 8.5 |
| Teardown | 10 s stop grace in all three; child labels set to the session filter | 8.6, findings 3 and 4 |
| Credentials | `test`/`test`, account `000000000000`, `us-east-1`, as `understudytest.Env` | 3.8 |
| Go entry point | `understudytest` stays the recommendation for Go | finding 1, 2.6 |

### 9.5 Limits and validation texts

Each module validates before creating the container, so a misconfiguration fails at once rather than after the startup timeout (tc-node `.agents/skills/add-module/SKILL.md:L24`).

- Publishing beyond loopback without the token: "understudy: publishing port 4566 on <addr> exposes the admin API, which has no token in the container; add WithAdminToken() or publish on 127.0.0.1."
- The socket requested on a daemon whose socket cannot be stat'ed locally: "understudy: cannot read the group of the Docker socket on <daemon>, which is not a local unix socket; pass WithDockerSocketGroup(<gid>), the numeric group owning /var/run/docker.sock on the daemon's host."
- A socket mounted but `docker.status` not `enabled` after start: "understudy: the Docker socket is mounted but understudy reports Docker <status>: <reason>; Lambda and DynamoDB will not run. Check the socket group (WithDockerSocketGroup) or run without WithDockerSocket()."

Values understudy validates itself are not re-implemented. These are the seed ("an unsigned integer"), the clock scale ("above 0 and at most 10000; not with a frozen --clock") and the token length (at least 16) ([repo] `docs/configuration.md`; `internal/config/draft.go:L330`). Instead, the modules surface the container log when it exits before ready: tc-go fails fast (4.2), Node uses `abortOnContainerExit` (8.3), and Python uses the per-poll status check (8.3).

### 9.6 Terraform

Nothing module-specific. `Env()` provides `AWS_ENDPOINT_URL` at an IP endpoint, which needs `skip_credentials_validation = true` and `skip_requesting_account_id = true` in the provider block. With an account id, the provider reads bucket tags through S3 Control at `<account>.<endpoint host>`, which cannot resolve for an IP, and the refresh fails ([s3.md](s3.md) finding 1). With credential validation on, the provider takes the account from `sts:GetCallerIdentity` and never consults `skip_requesting_account_id` ([tf-messaging.md 1.1](tf-messaging.md)), so that setting alone is not enough. Correction, 2026-10-26: this section first named `skip_requesting_account_id` alone, after `docs/guide/terraform.md`. Run against `understudy:dev` with provider 6.68.0, that block failed on every `aws_s3_bucket` with `dial tcp: lookup 000000000000.127.0.0.1: no such host`, through the Python module and against a host `understudy serve`; with both settings, apply, a no-change plan and destroy passed. The module docs show that block.

### 9.7 What must never be faked

- **Readiness.** It is reported only after `/health` answers 200 with `ready: true`, never after "port open" or a fixed sleep.
- **Docker.** A requested socket that does not work fails the start (8.5). Lambda must never appear to work without Docker.
- **Exposure.** It never silently widens: no `0.0.0.0` binding without the explicit option and the token.
- **Credentials.** `Env()` never returns real-looking credentials and never omits `AWS_ENDPOINT_URL`. The module's tests run sealed (8.7).
- **Cleanup.** The modules never claim it beyond what holds. With Ryuk disabled (`TESTCONTAINERS_RYUK_DISABLED=true`), a killed test process leaves containers that only `understudy cleanup --all` removes (3.6), and the docs say so.

### 9.8 Approvals needed from the maintainer

1. The dependencies in 8.10, per language.
2. The `admin` import (Go 1.27 floor) or the private client (8.10).
3. The 9.3 setting.
4. The npm name: unscoped `testcontainers-understudy`, or a scope the maintainer controls. Whether a suitable scope exists was not checked.

## 10. UNVERIFIED items

1. **Docker Desktop for macOS socket access.** Settled by TC-01 on 2026-11-06 (11.1): the socket is root-owned 0660 in the container, `--group-add 0` lets uid 65532 use it, and a `127.0.0.1` binding is reachable from macOS's loopback. 8.5's Desktop branch adds group 0.
2. **Lambda from a containerized understudy on Docker's default `bridge` network.** Testcontainers does not create a network by default. ADR 0011 says the InContainer topology is CI-tested, but which network the test uses was not established. The plausible outcomes: it works by container IP on `bridge`, or the module must create a user-defined network when the socket is mounted. TC-02.
3. **Drain time under a 10 s stop.** Whether `docker stop -t 10` lets understudy remove warm function containers and DynamoDB Local before SIGKILL. The design target is "a few seconds total" ([repo] `docs/research/containers.md` 9.2). Also what a force remove leaves, which confirms finding 3 empirically. Settles the 10 s grace in 8.6. TC-03.
4. **tc-node's HTTP wait against a 127.0.0.1-only binding when `localhost` resolves to `::1` first.** undici's dial-fallback behavior was not read. The plausible outcomes: it falls back to IPv4, or the wait times out and the Node module needs its own wait that dials 127.0.0.1. TC-04.
5. **testcontainers-node 12.2.1 under Bun,** the repository's JS runtime for conformance. The plausible outcomes: it works, or the module's tests must run under Node. TC-05.
6. **Testcontainers Cloud.** Whether its port forwarding reaches a port published on the cloud daemon's `127.0.0.1`. The plausible outcomes: it forwards loopback-bound ports, or the module needs the token and publishing beyond loopback there. TC-06.

## 11. Recording queue

None of these touch AWS, and none needs AWS credentials. Run them from a checkout of the repository, in a terminal no agent uses, with the recording-kit shell from [recording-kit.md](../recording-kit.md) sections 1 and 2 for `$P` (prefix) and `$REC` (output directory). `$R`, `$A`, `$Q`, `rec` and `recurl` are not needed, since no AWS CLI call reaches AWS. Every `aws` call goes through `scripts/sealed --endpoint`, which strips the shell's real AWS credentials. Build the image first with `just image`. Agents never run these.

### 11.1 TC-01: Docker Desktop for macOS, socket ownership and loopback publishing

Settles UNVERIFIED 1 (8.5 Desktop branch). Needs a Mac with Docker Desktop.

```sh
mkdir -p "$REC/TC-01"
docker version --format '{{.Server.Version}} {{.Server.Os}}' > "$REC/TC-01/engine.txt"
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock busybox:1.37 \
  stat -c 'uid=%u gid=%g mode=%a' /var/run/docker.sock | tee "$REC/TC-01/stat.txt"
G=$(docker run --rm -v /var/run/docker.sock:/var/run/docker.sock busybox:1.37 stat -c %g /var/run/docker.sock)
for v in with-group without-group; do
  extra=(); [ "$v" = with-group ] && extra=(--group-add "$G")
  docker run -d --name "$P-tc01-$v" -p 127.0.0.1::4566 -v /var/run/docker.sock:/var/run/docker.sock "${extra[@]}" understudy:dev
  PORT=$(docker port "$P-tc01-$v" 4566/tcp | head -1 | sed 's/.*://')
  for i in $(seq 1 50); do curl -fsS "http://127.0.0.1:$PORT/_understudy/v1/health" -o "$REC/TC-01/health-$v.json" && break; sleep 0.2; done
  docker port "$P-tc01-$v" > "$REC/TC-01/ports-$v.txt"
done
```

Record `docker.status` and `docker.reason` from both health files, and whether curl reached 127.0.0.1 at all. Cost: USD 0, about 10 minutes. Teardown: `docker rm -f "$P-tc01-with-group" "$P-tc01-without-group"`.

Recorded 2026-11-06 by the macOS Docker Desktop run (`docs/release-checklist.md` step 7), on Docker Desktop 4.98.0, Engine 29.9.2, macOS 26.7.1 on Apple Silicon, with Rosetta on:

- In a container: `uid=0 gid=0 mode=660`. On the host, `stat -L` of `/var/run/docker.sock`, a symlink to the user's Desktop socket: the user's uid, gid 20 (`staff`), mode 755.
- With `--group-add 0`: `docker.status` `enabled`. Without it: `degraded`, reason `docker daemon unavailable: permission denied on unix:///var/run/docker.sock`.
- curl reached the `127.0.0.1` publish in both runs.
- The three modules added the host's gid, 20, so each one's socket cases failed with its "the Docker socket is mounted but understudy reports Docker degraded" error, three per module. With group 0 on Desktop, every case passed.

### 11.2 TC-02: Lambda from a containerized understudy, default bridge versus a user network

Settles UNVERIFIED 2 (whether the module must create a network). Linux, Docker Engine.

```sh
mkdir -p "$REC/TC-02" && printf 'def handler(event, context):\n    return event\n' > "$REC/TC-02/h.py" && (cd "$REC/TC-02" && zip -q f.zip h.py)
docker network create "$P-net"
for mode in bridge user; do
  net=(); [ "$mode" = user ] && net=(--network "$P-net")
  docker run -d --name "$P-tc02-$mode" "${net[@]}" -p 127.0.0.1::4566 \
    -v /var/run/docker.sock:/var/run/docker.sock --group-add "$(stat -c %g /var/run/docker.sock)" understudy:dev
  E=http://127.0.0.1:$(docker port "$P-tc02-$mode" 4566/tcp | head -1 | sed 's/.*://')
  until curl -fsS "$E/_understudy/v1/health" >/dev/null; do sleep 0.2; done
  scripts/sealed --endpoint "$E" aws iam create-role --role-name "$P-fn" \
    --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
  scripts/sealed --endpoint "$E" aws lambda create-function --function-name "$P-echo" --runtime python3.13 \
    --handler h.handler --role "arn:aws:iam::000000000000:role/$P-fn" --zip-file "fileb://$REC/TC-02/f.zip"
  scripts/sealed --endpoint "$E" aws lambda wait function-active-v2 --function-name "$P-echo"
  scripts/sealed --endpoint "$E" aws lambda invoke --function-name "$P-echo" --cli-binary-format raw-in-base64-out \
    --payload '{"ok":1}' "$REC/TC-02/out-$mode.json" > "$REC/TC-02/invoke-$mode.json" 2>&1
  curl -s "$E/_understudy/v1/health" > "$REC/TC-02/health-$mode.json"
  docker logs "$P-tc02-$mode" > "$REC/TC-02/log-$mode.txt" 2>&1
  docker stop -t 10 "$P-tc02-$mode"
done
```

Record each invoke result and the `topology` field of each health file. Cost: USD 0 (the Python 3.13 base image pull), about 15 minutes. Teardown: `docker rm -f "$P-tc02-bridge" "$P-tc02-user"; docker network rm "$P-net"`. If a stop left containers behind, remove them by the IDs `docker ps -a --filter label=understudy.dev/managed=true` lists with the instance label from the logs.

### 11.3 TC-03: drain time under a 10 s stop, and what a force remove leaves

Settles UNVERIFIED 3 (the 8.6 grace) and confirms finding 3. Linux, Docker Engine. Run TC-02 first: this case reuses its `$REC/TC-02/f.zip`. DynamoDB Local runs under its own licence, which needs an AWS account in good standing ([repo] `README.md`, DynamoDB and its licence).

```sh
mkdir -p "$REC/TC-03"
for how in stop kill; do
  docker run -d --name "$P-tc03-$how" -p 127.0.0.1::4566 \
    -v /var/run/docker.sock:/var/run/docker.sock --group-add "$(stat -c %g /var/run/docker.sock)" understudy:dev
  E=http://127.0.0.1:$(docker port "$P-tc03-$how" 4566/tcp | head -1 | sed 's/.*://')
  until curl -fsS "$E/_understudy/v1/health" >/dev/null; do sleep 0.2; done
  scripts/sealed --endpoint "$E" aws dynamodb create-table --table-name "$P-t" \
    --attribute-definitions AttributeName=pk,AttributeType=S --key-schema AttributeName=pk,KeyType=HASH --billing-mode PAY_PER_REQUEST
  scripts/sealed --endpoint "$E" aws iam create-role --role-name "$P-fn" \
    --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
  scripts/sealed --endpoint "$E" aws lambda create-function --function-name "$P-echo" --runtime python3.13 \
    --handler h.handler --role "arn:aws:iam::000000000000:role/$P-fn" --zip-file "fileb://$REC/TC-02/f.zip"
  scripts/sealed --endpoint "$E" aws lambda wait function-active-v2 --function-name "$P-echo"
  for n in 1 2 3; do
    scripts/sealed --endpoint "$E" aws lambda invoke --function-name "$P-echo" --cli-binary-format raw-in-base64-out \
      --payload '{"ok":1}' "$REC/TC-03/out-$how-$n.json" > /dev/null
  done
  docker ps -a --filter label=understudy.dev/managed=true --format '{{.ID}} {{.Names}} {{.Status}}' > "$REC/TC-03/before-$how.txt"
  start=$(date +%s.%N)
  if [ "$how" = stop ]; then docker stop -t 10 "$P-tc03-$how"; else docker rm -f "$P-tc03-$how"; fi
  end=$(date +%s.%N); echo "$end - $start" | bc > "$REC/TC-03/seconds-$how.txt"
  sleep 3
  docker ps -a --filter label=understudy.dev/managed=true --format '{{.ID}} {{.Names}} {{.Status}}' > "$REC/TC-03/after-$how.txt"
done
```

Cost: USD 0, about 20 minutes. Teardown: `awk '{print $1}' "$REC/TC-03"/after-*.txt | sort -u | xargs -r docker rm -f; docker rm -f "$P-tc03-stop" 2>/dev/null`. Remove only the IDs this case recorded, never every managed container ([repo] `docs/research/containers.md` 9.4).

### 11.4 TC-04: tc-node's HTTP wait with a 127.0.0.1-only binding

Settles UNVERIFIED 4 (whether the Node module needs its own wait). Linux with Node 22.22 or later, on a host where `localhost` resolves to `::1` first (check with `getent ahosts localhost`).

```sh
mkdir -p "$REC/TC-04" && cd "$REC/TC-04" && npm init -y >/dev/null && npm install --ignore-scripts testcontainers@12.2.1
getent ahosts localhost > hosts.txt; node -e 'require("dns").lookup("localhost",{all:true},(e,a)=>console.log(JSON.stringify(a)))' > lookup.json
cat > tc04.js <<'EOF'
const { GenericContainer, Wait } = require("testcontainers");
class C extends GenericContainer {
  async beforeContainerCreated() { this.hostConfig.PortBindings["4566/tcp"] = [{ HostIp: "127.0.0.1", HostPort: "0" }]; }
}
(async () => {
  const t0 = Date.now();
  const c = await new C(process.env.UNDERSTUDY_IMAGE || "understudy:dev").withExposedPorts(4566)
    .withWaitStrategy(Wait.forHttp("/_understudy/v1/health", 4566).forStatusCode(200)).withStartupTimeout(20000).start();
  console.log(JSON.stringify({ ms: Date.now() - t0, host: c.getHost(), port: c.getMappedPort(4566) }));
  await c.stop({ timeout: 10000 });
})().catch((e) => { console.error(String(e)); process.exit(1); });
EOF
node tc04.js > result.json 2> error.txt; echo "exit=$?" >> result.json
```

Cost: USD 0, about 10 minutes. Teardown: Ryuk removes the container 10 s after the script exits; then `rm -rf "$REC/TC-04/node_modules"`.

### 11.5 TC-05: testcontainers-node under Bun

Settles UNVERIFIED 5 (the JS module's test runtime). Uses TC-04's directory and script.

```sh
cd "$REC/TC-04" && bun --version > bun-version.txt && bun run tc04.js > result-bun.json 2> error-bun.txt; echo "exit=$?" >> result-bun.json
```

Cost: USD 0, about 5 minutes. Teardown: as TC-04.

### 11.6 TC-06: Testcontainers Cloud and loopback-bound ports

Settles UNVERIFIED 6 (8.4's remote-daemon branch for Testcontainers Cloud). Needs a Testcontainers Cloud account with its agent running. Whether a free allowance covers this run is UNVERIFIED; check the account's plan before running. Uses TC-04's script with the image pushed to a registry the cloud daemon can pull: `UNDERSTUDY_IMAGE=<registry>/understudy:<tag>`. The cloud daemon cannot see a local `understudy:dev`.

```sh
cd "$REC/TC-04" && docker context ls > contexts.txt
UNDERSTUDY_IMAGE=<registry>/understudy:<tag> node tc04.js > result-cloud.json 2> error-cloud.txt; echo "exit=$?" >> result-cloud.json
```

Cost: USD 0 if the plan includes the minutes, otherwise the plan's per-minute price for about 5 minutes (UNVERIFIED), about 15 minutes in all. Teardown: the cloud session's Ryuk removes the container. Delete the pushed tag from the registry afterwards.
