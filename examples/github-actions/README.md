# understudy in GitHub Actions

[`understudy-tests.yml`](understudy-tests.yml) is a workflow that installs understudy, starts it in the background, points every later step at it, and runs a test suite. Copy it to `.github/workflows/` in your repository and replace the test step with your own.

The steps that matter:

```yaml
- name: Install understudy
  run: go install "github.com/sustinbebustin/understudy/cmd/understudy@${UNDERSTUDY_VERSION}"

- name: Start understudy
  run: |
    understudy serve --log-format json >"$RUNNER_TEMP/understudy.log" 2>&1 &
    understudy health --wait 30s
    understudy env --format github >>"$GITHUB_ENV"
```

- `serve` runs in the background for the rest of the job. Its output goes to a file, so the step does not wait on it.
- `health --wait 30s` returns once understudy answers, usually within milliseconds. It exits non-zero after 30 seconds otherwise, which fails the step.
- `env --format github` appends `AWS_ENDPOINT_URL`, dummy credentials and a region to `$GITHUB_ENV`. Every later step then uses understudy, whether it runs an SDK, the AWS CLI or Terraform. The hints it prints for each client go to the step log.

On failure, `understudy journal --errors` prints every call understudy answered with an error, and the server log is kept as an artifact.

`ubuntu-latest` has Docker, so Lambda functions run with no extra setup. DynamoDB also needs the DynamoDB Local licence accepted: add `--accept-dynamodb-local-license` to `serve`.

## Before copying

- **Version.** `UNDERSTUDY_VERSION: main` builds the newest commit. Pin a release tag or a commit instead, so a change in understudy cannot change your build unannounced.
- **Tests.** As written, the workflow runs the [python-worker](../python-worker) suite from the understudy repository. Change `working-directory` and `run` in the test step to run yours.

## How it was checked

GitHub Actions cannot run outside GitHub, so `examples/check.sh github-actions` checks the workflow in two parts. It lints the file with [actionlint](https://github.com/rhysd/actionlint). Then it runs the workflow's commands against a local understudy: it loads the output of `understudy env --format github` into the environment and runs `uv run pytest` in `examples/python-worker`. The install step is not run, because it fetches understudy from GitHub.
