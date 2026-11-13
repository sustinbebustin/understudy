# Getting started

## Install

```sh
brew install --cask sustinbebustin/tap/understudy
```

The [README](../../README.md#install) also covers the release binaries, `go install` and the image `ghcr.io/sustinbebustin/understudy`.

Lambda functions and DynamoDB need a Docker Engine: `DOCKER_HOST`, a unix socket or a `tcp://` daemon, else the current Docker context's (`docker context use`), else `/var/run/docker.sock`. Everything else runs without one.

## Start it

```sh
understudy serve
```

The banner names the endpoint (`http://127.0.0.1:4566`), the admin API, the services and anything that needs attention, such as Docker not answering. State lives in memory until the process ends; see [snapshots and persistence](snapshots.md) to keep it. Stop it with Ctrl-C: it removes every container it started.

## Point a client at it

```sh
eval "$(understudy env)"
understudy health
aws sts get-caller-identity
```

`understudy env` prints the variables every SDK, the AWS CLI and Terraform read: `AWS_ENDPOINT_URL`, dummy credentials and a region. Comments on stderr name the settings a client needs that no variable carries. `--sdk go|python|js|cli|terraform` narrows the output to one client, and `--format sh|fish|dotenv|github` changes its form:

```sh
understudy env --sdk python --format dotenv > .env
```

No code changes are needed in an application that builds its clients with the SDK's default configuration. [SDKs and the AWS CLI](sdks.md) has examples per language.

## Accounts and regions

The access key picks the account. A 12-digit access key id is that account; any other key, such as `test`, is the default account `000000000000`:

```sh
eval "$(understudy env --account 111111111111)"
aws sts get-caller-identity
```

Accounts are fully isolated, so parallel test suites can each use their own. Credentials minted by IAM `CreateAccessKey` or STS `AssumeRole` act as the user or role they belong to.

The region comes from the request's signature, as on AWS. Any AWS region name works; add others with `"regions": ["local-1"]` in [the configuration file](configuration.md).

## See what is there

```sh
understudy resources
understudy resources --service sqs --region us-east-1
understudy journal --since 10m
understudy coverage --service sqs
```

`resources` lists every resource by service, account and region. [The journal](journal.md) shows every call. `coverage` says, per operation, whether understudy implements it fully, stores it only, implements it with named gaps, or answers 501.

An operation understudy does not implement fails fast, in AWS's error format, and says so:

```text
An error occurred (NotImplemented) when calling the GetRandomPassword operation: understudy does not implement secretsmanager.GetRandomPassword. Run 'understudy coverage --service secretsmanager' for what is supported; to ask for it, see https://github.com/sustinbebustin/understudy/issues?q=secretsmanager.GetRandomPassword
```

A call to a service understudy does not emulate at all, such as ECS, fails the same way and names the services it does.

## Start over

```sh
understudy reset
understudy reset --service sqs --service sns
understudy reset --account 111111111111 --region eu-west-1
understudy reset --clock --faults
```

A reset keeps the clock and fault rules unless `--clock` or `--faults` says otherwise.

## Check the machine

```sh
understudy doctor
```

`doctor` prints a fix for each problem it finds. It checks:

- the server and the configuration file;
- Docker, its socket and the container networking Lambda will use;
- whether `*.localhost` names resolve, which virtual-hosted S3, function URLs and API Gateway hosts need;
- binfmt support for the other CPU architecture;
- hot-reload directories;
- whether DynamoDB Local can run.

## Exit codes

Every command uses the same codes, which `understudy --help` prints:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | the operation failed |
| 2 | usage error |
| 3 | understudy is not reachable, or did not answer in time |
| 4 | a condition was not met, such as `health --wait` or `settle` timing out |
| 5 | the server refused the admin token |
