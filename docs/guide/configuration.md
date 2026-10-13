# Configuration

`understudy serve` takes each setting from a flag, an `UNDERSTUDY_*` variable or a key in a JSON file, in that order of precedence, then the default. The file is `./understudy.json` when it exists, or the one `--config` names. A few sections, such as Lambda hot reload and preloaded fault rules, exist only in the file.

`understudy serve --help` lists every setting with its three names, and [../configuration.md](../configuration.md) is the same list plus what the help leaves out. This page shows common setups.

## Loading is strict

An unknown flag, variable or key is refused with the closest valid name, and `serve` exits 2 before it listens:

```text
understudy serve: config: understudy.json: listn: is not a configuration key; the closest valid name is listen; fix or remove the key
```

A setting this build cannot apply yet, such as `s3.maxObjectBytes`, is refused by name rather than ignored.

## A configuration file

```json
{
  "seed": 7,
  "clock": "frozen@2026-01-01T00:00:00Z",
  "regions": ["local-1"],
  "lambda": {
    "hotReload": [{"function": "hello", "dir": "/abs/path/to/build"}]
  },
  "faults": [
    {"id": "flaky-send", "phase": "request",
     "match": {"operations": ["sqs.SendMessage"]},
     "action": {"kind": "error", "shape": "QueueDoesNotExist"},
     "trigger": {"kind": "next-n", "n": 1}}
  ]
}
```

This starts with a frozen clock at a fixed instant, reproducible identifiers, an extra region name, a function whose code is read from a directory, and one fault rule. `understudy doctor` checks a file without starting a server.

## Common setups

A free port, for parallel jobs. The ready file receives `{"endpoint", "pid"}` once the server listens:

```sh
understudy serve --listen 127.0.0.1:0 --ready-file ready.json
```

The CLI finds a server at `UNDERSTUDY_ENDPOINT`, else at the port `UNDERSTUDY_LISTEN` names, else at `http://127.0.0.1:4566`; every command also takes `--endpoint`.

Reproducible runs: with `--seed N`, generated identifiers (message ids, receipt handles, request ids, key ids) repeat from run to run for the same sequence of calls. A frozen clock takes wall time out of the picture:

```sh
understudy serve --seed 42 --clock frozen@2026-01-01T00:00:00Z
```

State that survives restarts, in a directory understudy locks for itself:

```sh
understudy serve --persist ./.understudy
```

Structured logs:

```sh
understudy serve --log-format json --log-level debug
```

Any flag has a variable, which suits containers and CI:

```sh
UNDERSTUDY_CLOCK=frozen understudy serve
```

## Listening beyond loopback

understudy binds `127.0.0.1` by default. The AWS API checks no signatures by default, and every client may sign with the public dummy pair `test`/`test` even under `--sigv4 verify`, so anyone who reaches the port can read every secret and object and run any image as a Lambda function. To listen on another address on a host, you must set an admin token, which guards the admin API under `/_understudy/`, and say explicitly that the AWS API is open:

```sh
UNDERSTUDY_ADMIN_TOKEN=0123456789abcdef0123 understudy serve --listen 172.17.0.1:4590 --allow-unauthenticated-aws-api
```

The token must be at least 16 characters. CLI commands send it when `UNDERSTUDY_ADMIN_TOKEN` is set in their environment, and exit 5 when the server refuses it. Inside a container with its own network, understudy listens on `0.0.0.0:4566` without a token and relies on the published port instead; publish it on `127.0.0.1`.

## Fidelity switches

A few behaviors are file settings, because AWS's own behavior would slow tests down or because some setups need leniency. The first value is the default:

| Key | Values | Effect |
|---|---|---|
| `s3.crossRegion` | `aws`, `lenient` | `aws` refuses a bucket request signed for another region, as AWS does; `lenient` serves it and journals a warning |
| `lambda.stateTransitions` | `final`, `clock` | `final` makes functions `Active` before the call returns; `clock` restores `Pending` and `InProgress` on the emulator clock, for SDK waiter tests |
| `sqs.queueDeletedRecently` | `ignore`, `enforce` | `ignore` frees a deleted queue's name at once; `enforce` refuses re-creating it for 60 seconds, as AWS does |
| `iam.assumeRole` | `strict`, `lenient` | `strict` refuses `AssumeRole` for a role that does not exist; `lenient` allows it |

`understudy coverage --switches` lists every fidelity switch, these last three among them, with its value and the evidence behind it. `s3.crossRegion` is a configuration setting only and is not listed there.
