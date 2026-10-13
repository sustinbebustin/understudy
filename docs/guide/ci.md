# CI

understudy suits CI: it starts in milliseconds, needs no account, token or network beyond Docker image pulls, and leaves nothing behind when the job ends. The pattern is the same on every CI system: start it in the background, wait for it, export the environment, warm the images, run the tests, and keep the evidence when they fail.

## The steps

```sh
understudy serve --seed 1 --log-format json > "$RUNNER_TEMP/understudy.log" 2>&1 &
understudy health --wait 30s
understudy env --format github >> "$GITHUB_ENV"
understudy images pull --runtime python3.13
```

- `health --wait` exits 0 once every service is ready, 4 if the time runs out, and 3 if nothing ever answered.
- `env --format github` writes `AWS_ENDPOINT_URL`, dummy credentials and a region for every later step. Its explanatory comments go to stderr, so the file stays valid. Use `--format dotenv` or `sh` elsewhere.
- `images pull` takes the pinned Lambda base images' pull out of the first test's timing. Skip it if no test runs Lambda.
- `--seed` makes generated identifiers repeat from run to run.

After the tests, collect what explains a failure:

```sh
understudy settle --timeout 2m
understudy journal --errors --output json > "$RUNNER_TEMP/journal-errors.ndjson"
understudy snapshot save "$RUNNER_TEMP/state.tar.gz"
```

`settle` waits until no delivery, invocation or poll is pending, and exits 4 naming what is still running if the timeout passes. The journal lines and the snapshot are worth uploading as artifacts: `understudy snapshot inspect` and `snapshot load` reproduce the failing state on a laptop.

## GitHub Actions

```yaml
- name: Start understudy
  run: |
    understudy serve --seed 1 --log-format json > "$RUNNER_TEMP/understudy.log" 2>&1 &
    understudy health --wait 30s
    understudy env --format github >> "$GITHUB_ENV"
    understudy images pull --runtime python3.13

- name: Test
  run: uv run pytest

- name: Collect understudy evidence
  if: failure()
  run: |
    understudy journal --errors --output json > "$RUNNER_TEMP/journal-errors.ndjson"
    understudy snapshot save "$RUNNER_TEMP/state.tar.gz"
```

Hosted Ubuntu runners have Docker, so Lambda functions run as they do locally. understudy is not published yet, so a workflow builds it from the repository first; [examples/github-actions](../../examples/github-actions/) has a complete workflow.

## Parallel jobs on one machine

Either give each job its own server on a free port:

```sh
understudy serve --listen 127.0.0.1:0 --ready-file ready.json
```

and read the endpoint from `ready.json` once it appears (`{"endpoint": "http://127.0.0.1:40937", "pid": 1749456}`), passing it to the CLI as `UNDERSTUDY_ENDPOINT`. Or share one server and give each job its own account with a 12-digit access key (`understudy env --account 100000000001`): accounts never see each other's resources.

Go test suites can skip the server process entirely with [`understudytest`](sdks.md#go-tests-with-understudytest), which starts one in-process emulator per test.

## Docker Compose

Run the image as a service next to the application, with the port published on loopback and the Docker socket mounted for Lambda; the [README](../../README.md#docker) shows the `docker run` form, and [examples/docker-compose](../../examples/docker-compose/) a compose file. The image's healthcheck runs `understudy health`, so `depends_on: condition: service_healthy` waits for it.

## Self-hosted runners

understudy removes its containers when it stops. If a run crashes, the next `understudy serve` on the machine sweeps the containers of runs whose process is gone, and `understudy cleanup --persist DIR` removes those of a crashed run that used `--persist DIR`. `cleanup --all` removes every understudy container on the Docker daemon, including those of servers still running, so keep it for between jobs.
