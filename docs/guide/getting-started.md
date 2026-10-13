# Getting started

## Install

understudy is not published yet, so build it from a clone with Go 1.27:

```sh
go install ./cmd/understudy
```

This puts `understudy` in `$(go env GOPATH)/bin`. For the Docker image, run `just image`; the [README](../../README.md#docker) shows how to run it.

Lambda functions and DynamoDB need a Docker Engine (`DOCKER_HOST`, else `/var/run/docker.sock`). Everything else runs without one.

## Start it

```sh
understudy serve
```

The banner names the endpoint (`http://127.0.0.1:4566`), the admin API, the services and anything that needs attention, such as DynamoDB waiting for its licence. State lives in memory until the process ends; see [snapshots and persistence](snapshots.md) to keep it. Stop it with Ctrl-C: it removes every container it started.

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

A call to a service understudy does not emulate at all, such as ECS, fails the same way and names the twelve it does.

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

`doctor` checks the server, the configuration, Docker and its socket, the container networking Lambda will use, whether `*.localhost` names resolve (for virtual-hosted S3 and function URLs), binfmt support for the other CPU architecture, hot-reload directories and the DynamoDB Local licence, and prints a fix for each problem.

## Exit codes

Every command shares one table, which `understudy --help` prints: 0 success, 1 the operation failed, 2 usage error, 3 understudy not reachable, 4 a condition not met (such as `health --wait` or `settle` timing out), 5 admin token refused.
