# macOS Docker Desktop run

For the maintainer, before a tag (ROADMAP item 4). CI only has Linux, so Docker Desktop is supported but not CI-tested (ADR 0011, amendment of 2026-11-06). The first run, on 2026-11-06, answered the four questions below; each later run checks that the answers still hold on the current Desktop:

1. The exact `OperatingSystem` string `docker info` reports on Desktop. Topology detection keys on it containing `Docker Desktop` (`internal/topology/detect.go`), as `docs/research/containers.md` section 2.2 records. If it changes, Desktop is treated as a Linux bridge and functions cannot reach understudy.
2. Which Engine API call answers "Mounts denied" for a path Desktop does not share, the container create or the start, and with what status. The Lambda launcher falls back to copying the code on either (`internal/service/lambda/launcher/doc.go`), but its tests only fake the daemon's answer.
3. Whether `*.localhost` names resolve on macOS (`understudy doctor`'s localhost check).
4. TC-01 (`docs/research/testcontainers.md` 11.1): the uid, gid and mode of the Docker socket as a container sees it on Desktop, whether `--group-add <gid>` lets the image's user 65532 use it, and whether a `127.0.0.1` publish is reachable from macOS.

Nothing here touches AWS: every AWS CLI call goes through `scripts/sealed`. Allow about 90 minutes; the first run pulls the Lambda base images and DynamoDB Local.

## Expected failures

Any failure not listed here is a finding.

- `TestDetectOnThisHost` (`internal/topology`, `-tags docker`) asserts a Linux CI host and fails on Docker Desktop by design. Its `detected ...` log line is the result this run needs.

Two tests skip on a Mac by design, with the reason in `-v` output: `TestContainerListenerServesOnlyItsNetwork` (`internal/server`) needs a second loopback address, and `TestDynamoDBLocalMakesNoNetworkAttempts` (`internal/service/dynamodb/ddblocal`, `-tags docker`) needs a host that routes to container addresses.

## 1. Set up the Mac

1. Install the current Docker Desktop. In Settings:
   - General: leave the file sharing implementation on VirtioFS. Note whether "Use Rosetta for x86_64/amd64 emulation on Apple Silicon" is on.
   - Advanced: turn on "Allow the default Docker socket to be used". understudy reads no Docker contexts: its default socket is `/var/run/docker.sock`, and the image tests mount that path.
   - Resources: at least 4 CPUs and 8 GB of memory. Leave File sharing at its defaults.
2. Install the tools. Go must be 1.27 or later (`go.mod`); the Testcontainers suites need Node 24, Bun 1.4.3 and golangci-lint 2.15; the conformance suites need bash 4, and macOS's `/bin/bash` is 3.2 (CONTRIBUTING "Setup"):

   ```sh
   brew install go just uv jq awscli actionlint golangci-lint node@24 bash
   brew install hashicorp/tap/terraform
   curl -fsSL https://bun.sh/install | bash -s bun-v1.4.3
   ```

3. Use a checkout under your home directory (a path Desktop shares by default), such as a fresh clone or an existing working copy, at the commit under test. Run every command below from the repository root, in Homebrew's `bash`. macOS's login shell is zsh, so start bash first. An agent whose tool calls each start a fresh shell runs each block as `bash -c '...'` and repeats the two exports at the top of every block:

   ```sh
   "$(brew --prefix)/bin/bash"
   export PATH="$(brew --prefix)/bin:$(brew --prefix node@24)/bin:$HOME/.bun/bin:$PATH"   # Homebrew's bash before /bin's 3.2; node@24 is keg-only; the Bun installer edits only your login shell's rc file
   export OUT=~/understudy-macos-run && mkdir -p "$OUT"
   cd <your checkout>
   ```

4. Pull the image's golang base, as CI's `test-docker` job does: steps 5 to 8 build the image behind a dead proxy, which cannot fetch a registry token.

   ```sh
   docker pull "$(sed -n 's/^FROM --platform=$BUILDPLATFORM \(golang:[^ ]*\) AS builder$/\1/p' Dockerfile)"
   ```

## 2. Record the environment

```sh
{ sw_vers; uname -m; git rev-parse HEAD
  docker version; docker context ls; ls -l /var/run/docker.sock
  docker info --format 'OperatingSystem={{.OperatingSystem}} Architecture={{.Architecture}} NCPU={{.NCPU}} MemTotal={{.MemTotal}}'
  go version; just --version; node --version; bun --version; golangci-lint version
  terraform version; aws --version; uv --version; jq --version
} >"$OUT/env.txt" 2>&1
```

The `OperatingSystem=` line answers question 1.

## 3. Build, serve and run doctor

```sh
go build -o .build/understudy ./cmd/understudy
.build/understudy serve >"$OUT/serve.log" 2>&1 &
.build/understudy health --wait 30s
.build/understudy doctor >"$OUT/doctor.txt" 2>&1
.build/understudy doctor --output json >"$OUT/doctor.json" 2>&1
scripts/sealed --endpoint http://127.0.0.1:4566 aws dynamodb list-tables >"$OUT/dynamodb.txt" 2>&1
grep 'DynamoDB Local is ready' "$OUT/serve.log" >>"$OUT/dynamodb.txt"
kill %1
```

`doctor` reports the Docker socket, the detected topology (`docker-desktop`, with the documented host address `192.168.65.254`), whether `*.localhost` resolves (question 3), the hot-reload paths against Desktop's shared paths, and DynamoDB.

On Desktop understudy publishes DynamoDB Local on `127.0.0.1` with an ephemeral host port, because Desktop does not route to container addresses (ADR 0014). `dynamodb.txt` should hold `"TableNames": []` and a `DynamoDB Local is ready` line whose endpoint is `http://127.0.0.1:<port>`.

## 4. Unit tests

These run without Docker and catch macOS-only differences such as file locking and paths:

```sh
go test -race -count=1 ./... 2>&1 | tee "$OUT/unit.log"
```

## 5. Docker suite

```sh
go test -race -count=1 -timeout 30m -tags docker ./... 2>&1 | tee "$OUT/docker-suite.log"
go test -count=1 -v -tags docker -run TestDetectOnThisHost ./internal/topology 2>&1 | tee "$OUT/topology.log"
go test -count=1 -v -tags docker -run 'TestLocalOnAPublishedPort|TestLocalIsolatesAccountsAndRegions' ./internal/service/dynamodb 2>&1 | tee "$OUT/dynamodb-tests.log"
```

Tests to look at in `docker-suite.log`, all expected to pass:

| Test | Package | What it shows on Desktop |
|---|---|---|
| `TestLocalOnAPublishedPort` | `internal/service/dynamodb` | DynamoDB Local reached on a published loopback port, the Desktop path |
| `TestLocalIsolatesAccountsAndRegions` | `internal/service/dynamodb` | DynamoDB end to end through Local |
| `TestLateExportResetImportOnTheRealImage` | `internal/service/dynamodb` | snapshots with Local |
| `TestDynamoDBLocalMakesNoNetworkAttempts` | `internal/service/dynamodb/ddblocal` | Local's telemetry stays off |
| `TestShutdownDuringTheBackgroundStartLeavesNoContainer` | `internal/server` | shutdown removes Local mid-start |
| every test | `internal/service/lambda`, `internal/service/lambda/pool` | function containers reach understudy on the host's loopback through `host.docker.internal` |

The Lambda packages are the core of this run.

## 6. Smoke test and the image

```sh
just smoke 2>&1 | tee "$OUT/smoke.log"
go test -race -count=1 -timeout 30m -tags 'smoke docker' ./internal/smoke 2>&1 | tee "$OUT/smoke-docker.log"
```

The second command invokes a `provided.al2023` function from the host, then builds the image and repeats the workload against understudy inside a container with the socket mounted (the in-container topology).

## 7. Socket ownership and loopback publishing (TC-01)

`just image` builds `understudy:dev`. Then:

```sh
just image
mkdir -p "$OUT/TC-01"
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock busybox:1.37 \
  stat -c 'uid=%u gid=%g mode=%a' /var/run/docker.sock | tee "$OUT/TC-01/stat.txt"
G=$(docker run --rm -v /var/run/docker.sock:/var/run/docker.sock busybox:1.37 stat -c %g /var/run/docker.sock)
for v in with-group without-group; do
  extra=(); [ "$v" = with-group ] && extra=(--group-add "$G")
  docker run -d --name "tc01-$v" -p 127.0.0.1::4566 -v /var/run/docker.sock:/var/run/docker.sock "${extra[@]}" understudy:dev
  PORT=$(docker port "tc01-$v" 4566/tcp | head -1 | sed 's/.*://')
  for i in $(seq 1 50); do curl -fsS "http://127.0.0.1:$PORT/_understudy/v1/health" -o "$OUT/TC-01/health-$v.json" && break; sleep 0.2; done
  docker port "tc01-$v" >"$OUT/TC-01/ports-$v.txt"
done
docker rm -f tc01-with-group tc01-without-group
stat -L -f 'host socket: uid=%u gid=%g mode=%Lp' /var/run/docker.sock >"$OUT/TC-01/host-stat.txt"
```

Record `docker.status` and `docker.reason` from both health files, and whether curl reached `127.0.0.1` at all. The host's own `stat` is kept too, for comparison: on 2026-11-06 the container saw `uid=0 gid=0 mode=660` and the host the user's group, which is why the modules and the README add group 0 on Desktop (testcontainers.md 8.5).

## 8. Testcontainers modules

```sh
testcontainers/go/run.sh 2>&1 | tee "$OUT/tc-go.log"
testcontainers/python/run.sh 2>&1 | tee "$OUT/tc-python.log"
testcontainers/node/run.sh 2>&1 | tee "$OUT/tc-node.log"
```

Each suite runs against `understudy:dev` and includes a Lambda case with the socket mounted, with group 0 on Desktop. A failure there with the module's "the Docker socket is mounted but understudy reports Docker ..." message means the socket's ownership changed (step 7). On Linux the Go suite has one known failure, the cleanup of `TestTerminateLeavesNoChildContainer`, which lists containers with an already-canceled context; it passed on Desktop on 2026-11-06. The Node suite skips one case when `localhost` resolves to IPv6 first, as it does on macOS.

## 9. Which call answers "Mounts denied"

`/opt` is not one of Desktop's shared paths. Ask the Engine API for a bind mount from there, and keep both answers. The two `sudo` lines need your password; an agent asks you to run them:

```sh
sudo mkdir -p /opt/understudy-mount-probe
docker pull alpine:3.20
curl -sS -i --unix-socket /var/run/docker.sock -H 'Content-Type: application/json' \
  -d '{"Image":"alpine:3.20","Cmd":["true"],"Labels":{"understudy.dev/test":"1"},"HostConfig":{"Binds":["/opt/understudy-mount-probe:/probe"]}}' \
  'http://localhost/containers/create?name=understudy-mount-probe' >"$OUT/mounts-create.txt"
curl -sS -i --unix-socket /var/run/docker.sock -X POST \
  http://localhost/containers/understudy-mount-probe/start >"$OUT/mounts-start.txt"
docker rm -f understudy-mount-probe
sudo rmdir /opt/understudy-mount-probe
```

If the create already fails, the start answers "No such container"; that is the answer too.

## 10. Examples and the end-to-end pipeline (optional)

```sh
examples/check.sh 2>&1 | tee "$OUT/examples.log"
conformance/e2e-pipeline/run.sh 2>&1 | tee "$OUT/e2e.log"
```

## 11. Clean up

```sh
.build/understudy cleanup --all
ids=$(docker ps -aq --filter label=understudy.dev/test=1); [ -z "$ids" ] || docker rm -f $ids
docker ps -a --filter label=understudy.dev/managed
docker image ls --filter label=understudy.dev/test=1
```

The last two commands should list nothing; anything they list is worth reporting. DynamoDB Local's image stays in Desktop's local cache only: never re-tag, push or save it (ADR 0014).

## 12. What to report back

Write `$OUT/results.md` with one line per step (pass, fail or skipped, and for each failure the test name and its first error line), then answer:

1. The `OperatingSystem` string (`env.txt`), and the topology `doctor` detected, with the host address it reports.
2. Whether `*.localhost` resolved (`doctor.txt`).
3. Whether Lambda functions ran from the host (step 5 Lambda packages, step 6 host run) and in the container (step 6 image run).
4. Which call answered "Mounts denied" (`mounts-create.txt` or `mounts-start.txt`), with its status and message.
5. TC-01: the socket's uid, gid and mode in a container and on the host, `docker.status` with and without the group, whether `127.0.0.1` was reachable, and the Testcontainers Lambda results.
6. Whether DynamoDB answered from the host (`dynamodb.txt`), and the endpoint of the `DynamoDB Local is ready` line.
7. Every failure not under "Expected failures".
8. Your decision: is Docker Desktop a tested topology?

Send the directory as one archive. It holds no credentials, but it holds local paths and your user name, so it stays out of the repository:

```sh
tar -C ~ -czf ~/understudy-macos-run.tgz understudy-macos-run
```

The agent that takes it updates ADR 0011, ADR 0014's Desktop line, `docs/research/containers.md` 2.2, `docs/research/testcontainers.md` 10 and 11.1, the README's Docker Desktop line, `docs/guide/lambda.md` and SECURITY.md where the answers change them, and turns each new failure into a fix or a ticket.
