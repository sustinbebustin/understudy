# macOS run: Docker Desktop on Apple Silicon

For the repository owner. This is ARCHITECTURE.md open decision 25.1: run the Docker suite and the smoke test on Docker Desktop on an Apple Silicon Mac, then decide whether Docker Desktop becomes a tested topology. Agents only have Linux. The run also answers two open questions: which Engine API call answers "Mounts denied" and with what status (`docs/plan/DEFERRED.md`, wave 4), and whether `*.localhost` names resolve on macOS (record backlog 2.5).

Nothing here touches AWS. Allow about an hour; the first run pulls the Lambda base images.

## Expected failures

Three outcomes are known before you start. Any failure not listed here is a finding.

- **DynamoDB.** Until `internal/docker` has port bindings, understudy reaches DynamoDB Local at its default-bridge address, and Docker Desktop does not route to that address (ADR 0014, wave 6 amendment). Tests in which understudy on the host reaches a real DynamoDB Local container (for example `TestLocalIsolatesAccountsAndRegions`) should fail. Check whether the fix has landed: `grep -rq PortBindings internal/docker && echo "port bindings present" || echo "DynamoDB expected to fail"`.
- **`TestDetectOnThisHost`** (`internal/topology`) asserts a Linux CI host and fails on Docker Desktop by design. Its `detected ...` log line is the result this run needs.
- **The DynamoDB Local licence.** The docker-tagged DynamoDB tests pull and run DynamoDB Local only with `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1`, which accepts its licence (see `dynamodb-local-licence.md`); without it they skip (step 5).

## 1. Set up the Mac

1. Install the current Docker Desktop. In Settings:
   - General: leave the file sharing implementation on VirtioFS. Note whether "Use Rosetta for x86_64/amd64 emulation on Apple Silicon" is on.
   - Advanced: turn on "Allow the default Docker socket to be used". The smoke and image tests mount `/var/run/docker.sock`, and understudy's default socket is that path.
   - Resources: give it at least 4 CPUs and 8 GB of memory. Leave File sharing at its defaults (`/Users`, `/Volumes`, `/private`, `/tmp`, `/var/folders`).
2. Install the tools. Go must be 1.27 or later (`go.mod`). Homebrew core no longer updates Terraform, so take it from HashiCorp's tap:

```sh
brew install go just uv jq awscli actionlint
brew install hashicorp/tap/terraform
```

3. Clone the repository somewhere under `/Users` (a shared path) and check out the commit under test. Run every command below from the repository root, in `bash` (macOS opens zsh):

```sh
bash
cd ~/src/understudy        # your clone
export OUT=~/understudy-macos-run && mkdir -p "$OUT"
```

## 2. Record the environment

```sh
{ sw_vers; uname -m; git rev-parse HEAD
  docker version; docker context ls; ls -l /var/run/docker.sock
  docker info --format '{{.OperatingSystem}} {{.Architecture}} {{.NCPU}} CPUs {{.MemTotal}} bytes'
  go version; just --version; terraform version; aws --version; uv --version; jq --version; actionlint --version
} >"$OUT/env.txt" 2>&1
```

## 3. Build, serve and run doctor

```sh
go build -o .build/understudy ./cmd/understudy
.build/understudy serve >"$OUT/serve.log" 2>&1 &
.build/understudy health --wait 30s
.build/understudy doctor >"$OUT/doctor.txt" 2>&1
.build/understudy doctor --output json >"$OUT/doctor.json" 2>&1
kill %1
```

`doctor` reports the Docker socket, the detected topology (`DockerDesktop` with a probed or a fallback host address), whether `*.localhost` resolves, the hot-reload paths against Desktop's shared paths, and the licence state.

## 4. Unit tests

These run without Docker and catch macOS-only differences such as file locking and paths:

```sh
go test -race -count=1 ./... 2>&1 | tee "$OUT/unit.log"
```

## 5. Docker suite

```sh
go test -race -count=1 -timeout 30m -tags docker ./... 2>&1 | tee "$OUT/docker-suite.log"
go test -count=1 -v -tags docker -run TestDetectOnThisHost ./internal/topology 2>&1 | tee "$OUT/topology.log"
```

The tests that pull DynamoDB Local (`TestLocalIsolatesAccountsAndRegions`, `TestLateExportResetImportOnTheRealImage`, `TestDynamoDBLocalMakesNoNetworkAttempts`, `TestShutdownDuringTheBackgroundStartLeavesNoContainer`) skip unless you accept its licence: put `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1` in front of the first command only if you do.

The Lambda tests (`internal/service/lambda`, `internal/service/lambda/pool`) are the core of this run. They start function containers through Docker Desktop, hand them the probed `host.docker.internal` address, and need the functions' SDK calls to reach understudy on the host's loopback.

## 6. Smoke test

```sh
just smoke 2>&1 | tee "$OUT/smoke.log"
go test -race -count=1 -timeout 30m -tags 'smoke docker' ./internal/smoke 2>&1 | tee "$OUT/smoke-docker.log"
```

The second command invokes a `provided.al2023` function from the host. It then builds the image and repeats the workload against understudy inside a container with the socket mounted (the in-container topology). Without `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1`, DynamoDB answers 501 and the test expects that.

Optional, only if you accept the licence: rerun the second command with `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1` in front, and save the log as `smoke-docker-ddb.log`. Expect the host run's DynamoDB step to fail (bridge address). The in-container run should pass, because there DynamoDB Local joins understudy's network and is addressed by container IP.

## 7. Examples

```sh
examples/check.sh 2>&1 | tee "$OUT/examples.log"
cp -R .build/examples/logs "$OUT/examples-logs"
```

This runs the Python worker, the SNS to SQS to Go Lambda Terraform example (its `demo.sh` builds the worker for `arm64` on this machine), the docker compose example, and the GitHub Actions workflow (linted, then its test step run against the local server).

## 8. Which call answers "Mounts denied"

`/opt` is not in Docker Desktop's shared paths. Ask the Engine API directly for a bind mount from there, and keep both answers:

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
docker image rm alpine:3.20     # skip if you had this image before the run
```

If the create call already fails, the start call answers "No such container"; that is the answer too.

## 9. Optional: the end-to-end pipeline

Run this only if the `grep` under "Expected failures" found port bindings. Every worker in the pipeline takes a DynamoDB lock, and `run.sh` accepts the DynamoDB Local licence for its run:

```sh
conformance/e2e-pipeline/run.sh 2>&1 | tee "$OUT/e2e.log"
```

## 10. Clean up

```sh
.build/understudy cleanup --all
ids=$(docker ps -aq --filter label=understudy.dev/test=1); [ -z "$ids" ] || docker rm -f $ids
docker image ls --filter label=understudy.dev/test=1
```

The tests remove their own containers and images. The last command should list nothing, and anything it does list is worth reporting. Remove what is left with `docker image rm`.

## 11. What to send back

Write `$OUT/results.md` with one line per step: pass, fail or skipped, and for each failure the test name and its first error line. Then answer these:

1. Which topology `doctor` detected, and whether the host came from the probe or the `192.168.65.254` fallback.
2. Whether `*.localhost` resolved (the `doctor` localhost check).
3. Whether Lambda functions ran from the host (step 5 Lambda packages, step 6 host run) and in the in-container topology (step 6 image run).
4. Which call answered "Mounts denied" (`mounts-create.txt` or `mounts-start.txt`), with its status and message.
5. Every failure that is not in "Expected failures".
6. Your decision for 25.1: is Docker Desktop a tested topology, with DynamoDB excluded until port bindings land?

Send the directory as one archive. It holds no credentials; it does hold local paths and your user name.

```sh
tar -C ~ -czf ~/understudy-macos-run.tgz understudy-macos-run
```

The agent that takes it updates ARCHITECTURE.md 25.1, ADR 0011, the README's Docker Desktop line and `docs/guide/lambda.md`, and turns each new failure into a DEFERRED.md entry or a fix.
