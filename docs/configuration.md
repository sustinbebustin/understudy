# Configuration

`understudy serve` reads its settings from four places, in this order of precedence: flags, then `UNDERSTUDY_*` environment variables, then a JSON configuration file, then the defaults. The file is `./understudy.json` when it exists, or the one `--config` (`UNDERSTUDY_CONFIG`) names. Every scalar setting has all three forms; a few sections, such as `lambda.hotReload` and `faults`, exist only in the file.

Loading is strict. An unknown flag, variable or file key is refused with the closest valid name, and a value of the wrong shape says the shape it needs. A setting this build cannot apply yet is refused by name rather than ignored.

## Accounts and regions

A request's account comes from the access key id it is signed with:

- A key understudy minted, through IAM `CreateAccessKey`, STS `AssumeRole` or a Lambda execution role, is the principal it was minted for.
- A 12-digit access key id is the root of that account, so `AWS_ACCESS_KEY_ID=111111111111` works in account 111111111111.
- Any other key is the root of the default account: `000000000000`, unless `accounts.default` in the file sets another. A real-looking `AKIA` or `ASIA` key is never decoded to the real account it belongs to; understudy logs one warning per such key.

The region comes from the SigV4 credential scope. It must be an AWS region name, or a name listed in the top-level `regions` array of the file, such as `{"regions": ["local-1"]}`.

## Browser pages

The AWS API checks no signature, so understudy answers a call made from a web page only when the page is on a loopback origin (`localhost`, a name under it, or a loopback address) and on a port other than understudy's own. A page understudy served itself, such as an S3 object or a function URL response, is refused. So is a call a dev server proxies to understudy without rewriting the `Host` header, since the request then names the page's own port; let the proxy set `Host` to understudy's address (in Vite, `changeOrigin: true`). Serve the app from its own port, or make the call from an SDK outside the browser. Calls from the CLI and SDKs carry no `Origin` and are not affected.

## Exit codes

`serve` exits 2 when it refuses a flag, variable or file setting, and 1 when it fails after loading them, such as a port already in use. Every CLI command shares one table, which `understudy --help` prints:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | the operation failed |
| 2 | usage error: fix the command line or the configuration |
| 3 | understudy is not reachable at the endpoint, or did not answer in time |
| 4 | a condition was not met, such as `health --wait` or `settle` timing out |
| 5 | the server refused the admin token (`UNDERSTUDY_ADMIN_TOKEN`) |

## Every setting

This is what `understudy serve --help` prints under Flags. A test keeps it in step with the binary.

<!-- BEGIN serve --help -->
```text
  --config FILE
      JSON configuration file (UNDERSTUDY_CONFIG; default ./understudy.json when it exists)
  --listen ADDR
      address to listen on; port 0 picks a free port (default 127.0.0.1:4566, or 0.0.0.0:4566 inside a container with its own network, outside Kubernetes)
      variable UNDERSTUDY_LISTEN, file key "listen"
  UNDERSTUDY_ADMIN_TOKEN=TOKEN (no flag)
      bearer token every admin call must carry; required to listen beyond loopback on a host, with host networking or in a Kubernetes pod, where it must be at least 16 characters. It guards the admin API under /_understudy/ only
      variable UNDERSTUDY_ADMIN_TOKEN, file key "adminToken"
  --listen-auth MODE
      what guards a listener beyond loopback: token, or container-published inside a container with its own network, outside Kubernetes (default container-published there, else token)
      variable UNDERSTUDY_LISTEN_AUTH, file key "listenAuth"
  --allow-unauthenticated-aws-api
      serve the AWS API to every peer that reaches a listener guarded by the admin token; this build checks no request signature, so such a peer can read every secret and object and run any image as a Lambda function. Required with the token beyond loopback
      variable UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API, file key "allowUnauthenticatedAwsApi"
  --tls
      also serve HTTPS with a self-signed certificate for localhost, *.localhost and 127.0.0.1
      variable UNDERSTUDY_TLS, file key "tls"
  --tls-listen ADDR
      address of the TLS listener; implies --tls (default the listen address at port 4567, or port 0 when listening on port 0)
      variable UNDERSTUDY_TLS_LISTEN, file key "tlsListen"
  --persist DIR
      directory state persists in across restarts (default none: state is in memory only). A running clock resumes at wall time, or at its saved instant when that is later; a frozen or scaled one resumes from its saved instant
      variable UNDERSTUDY_PERSIST, file key "persist"
  --persist-every DURATION
      wall time between checkpoints while state changed, with --persist (default 30s)
      variable UNDERSTUDY_PERSIST_EVERY, file key "persistEvery"
  --load-snapshot FILE
      snapshot archive to load at start, over any state --persist restored
      variable UNDERSTUDY_LOAD_SNAPSHOT, file key "loadSnapshot"
  --seed N
      seed for reproducible ids, an unsigned integer (default random)
      variable UNDERSTUDY_SEED, file key "seed"
  --clock MODE
      how the emulator clock starts: running, frozen, or frozen@<RFC 3339 time> (default running)
      variable UNDERSTUDY_CLOCK, file key "clock"
  --clock-scale FACTOR
      run the clock this many emulator seconds per wall second from start, above 0 and at most 10000; not with a frozen --clock (default 1)
      variable UNDERSTUDY_CLOCK_SCALE, file key "clockScale"
  --sigv4 MODE
      signature checking: parse (default). verify, which refuses a bad signature, is not applied yet, and serve refuses it
      variable UNDERSTUDY_SIGV4, file key "sigv4"
  --docker URL
      Docker Engine for Lambda and DynamoDB Local: unix://<socket> or disabled (default DOCKER_HOST, else unix:///var/run/docker.sock)
      variable UNDERSTUDY_DOCKER, file key "docker"
  --accept-dynamodb-local-license
      accept the Amazon DynamoDB Local License Agreement so DynamoDB Local is pulled and run; the variable accepts only the value 1
      variable UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE, file key "acceptDynamodbLocalLicense"
  --ready-file FILE
      file to write {"endpoint", "pid"} to once listening
      variable UNDERSTUDY_READY_FILE, file key "readyFile"
  --log-format FORMAT
      log format: text or json (default text)
      variable UNDERSTUDY_LOG_FORMAT, file key "logFormat"
  --log-level LEVEL
      log level: debug, info, warn or error (default info)
      variable UNDERSTUDY_LOG_LEVEL, file key "logLevel"
  --output text|json
      another name for --log-format

Precedence: flags, then UNDERSTUDY_* variables, then the file, then the defaults.

The file also holds sections no flag sets. This file shows each, with the defaults where a
setting has one; any key may be left out:

  {
    "listen": "127.0.0.1:4566",
    "accounts": {"default": "000000000000"},
    "regions": ["local-1"],
    "s3": {"baseDomains": ["s3.dev.localhost"], "crossRegion": "aws"},
    "lambda": {
      "network": "auto", "startConcurrency": 16, "maxEnvironments": 64, "idleTimeout": "10m",
      "stateTransitions": "final",
      "hotReload": [{"function": "worker", "dir": "/abs/path/to/build", "account": "000000000000", "region": "us-east-1"}]
    },
    "sqs": {"queueDeletedRecently": "ignore"},
    "iam": {"assumeRole": "strict"},
    "dynamodb": {"enabled": true, "memoryLimit": 1073741824},
    "journal": {"capacity": 100000, "maxBytes": 134217728, "file": "/abs/path/to/journal.ndjson"},
    "faults": [{"id": "orders-throttle", "phase": "request", "match": {"operations": ["sqs.SendMessage"]},
                "action": {"kind": "throttle"}, "trigger": {"kind": "next-n", "n": 3}}]
  }

  regions                    region names understudy accepts beyond AWS's own
  s3.baseDomains             names added to localhost and s3.localhost; each must resolve to
                             loopback by itself (a name under localhost)
  s3.crossRegion             aws or lenient
  lambda.network             auto, disabled, or bridge with "bridgeGateway": "172.17.0.1"
  lambda.stateTransitions    final or clock
  lambda.hotReload           functions whose code is read from a host directory; account and
                             region may be left out
  sqs.queueDeletedRecently   ignore or enforce
  iam.assumeRole             strict or lenient
  journal.file               NDJSON file every event is appended to (default none)
  faults                     rules as 'understudy faults list --output json' prints each "rule"

Not supported yet: this build refuses sigv4 verify, accounts.keys, s3.maxObjectBytes, dynamodb.registry rather than ignore them.

Accounts: a request signed with a 12-digit access key id is in that account; any other key not
minted by understudy is in the default account, 000000000000 unless accounts.default sets another.

Reference: docs/configuration.md in the understudy repository.
```
<!-- END serve --help -->
