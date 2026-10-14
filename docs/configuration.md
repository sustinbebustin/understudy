# Configuration

`understudy serve` reads its settings from four places, in this order of precedence: flags, then `UNDERSTUDY_*` environment variables, then a JSON configuration file, then the defaults. The file is `./understudy.json` when it exists, or the one `--config` (`UNDERSTUDY_CONFIG`) names. Every scalar setting has all three forms; a few sections, such as `lambda.hotReload` and `faults`, exist only in the file.

Loading is strict. An unknown flag, variable or file key is refused with the closest valid name, and a value of the wrong shape says the shape it needs. A setting this build cannot apply yet is refused by name rather than ignored.

## Accounts and regions

A request's account comes from the access key id it is signed with:

- A key understudy minted, through IAM `CreateAccessKey`, STS `AssumeRole` or a Lambda execution role, is the principal it was minted for.
- A key listed in `accounts.keys` is the root of the account its entry names.
- A 12-digit access key id is the root of that account, so `AWS_ACCESS_KEY_ID=111111111111` works in account 111111111111.
- Any other key, `test` among them, is the root of the default account: `000000000000`, unless `accounts.default` in the file sets another. A real-looking `AKIA` or `ASIA` key is never decoded to the real account it belongs to; understudy logs one warning per such key.

`accounts.keys` lists keys with their own secret, for a CI setup that signs with fixed credentials:

```json
{"accounts": {"keys": [{"accessKeyId": "CIKEY0000000000001", "secretAccessKey": "ci-secret", "account": "222222222222"}]}}
```

Each `accessKeyId` is 16 to 128 letters and digits (DynamoDB Local accepts no others) and appears once; `secretAccessKey` may not be empty. Static keys come from the configuration at every start: snapshots never hold them, and a reset or a snapshot load keeps them.

The region comes from the SigV4 credential scope. It must be an AWS region name, or a name listed in the top-level `regions` array of the file, such as `{"regions": ["local-1"]}`.

## Signature verification

`--sigv4` (`UNDERSTUDY_SIGV4`, file key `sigv4`) is `parse` by default: understudy reads every request's credentials for its account and region and checks no signature, so any secret works.

`--sigv4 verify` checks every signed request as AWS does, in the claimed service's own error words:

- The key must have a secret. A key understudy minted has its own; an `accounts.keys` entry has the one it lists; the dummy key `test` and every 12-digit key have the secret `test`, so `AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test` and the account-selecting keys keep working. Any other key is refused as unknown: `UnrecognizedClientException` (JSON services), `InvalidClientTokenId` (Query services), `InvalidAccessKeyId` (S3).
- Temporary credentials (an `ASIA` key from STS `AssumeRole` or a Lambda execution role) must send their session token and be unexpired: a missing or wrong token is `UnrecognizedClientException`, `InvalidClientTokenId` or `InvalidToken`; an expired key is `ExpiredTokenException`, `ExpiredToken` or, on S3, `ExpiredToken`. Expiry is judged on the wall clock, as STS stamps `Expiration`.
- The signature must verify: header-signed SigV4, presigned SigV4 and S3's SigV2 header and presigned forms. A mismatch is `InvalidSignatureException` (JSON), `SignatureDoesNotMatch` (Query), or S3's `SignatureDoesNotMatch` with its signing-debug elements. A request time more than 15 minutes from the wall clock is refused.
- The body is bound to the signature. S3 checks the streamed bodies of `PutObject` and `UploadPart` against `x-amz-content-sha256` as they arrive; understudy hashes every other body before the call runs. A body that differs from the hash it was signed with is refused (`XAmzContentSHA256Mismatch` on S3) and changes nothing.
- Presigned S3 URLs expire on the emulator clock. A SigV4 URL lives `X-Amz-Expires` of emulator time from the moment it was signed, so a URL presigned after `understudy clock advance` lives its full lifetime, and advancing the clock past its expiry makes it answer 403 `AccessDenied` "Request has expired", whether the clock is running, frozen or scaled. `X-Amz-Date` has whole-second precision, so under `verify` the answer to a call that changes the clock waits for the next whole second; only a URL signed while that call is in flight can be placed on the wrong side of the change. A SigV2 URL carries only its `Expires` instant, not when it was signed, so it is compared with emulator time directly and expires as intended only while the clock has not been moved away from wall time since it was signed; presign with SigV4 when a test moves the clock (boto3 presigns with SigV2 in `us-east-1` unless its client has `Config(signature_version="s3v4")`). Under `parse` a presigned URL never expires.
- Signed `aws-chunked` uploads (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD` and its `-TRAILER` form) verify every chunk and trailer signature.

A refused request is journaled as a failed call. Neither mode logs a secret or a session token. Credentials that cannot be read at all are refused in both modes, on every route. The one exception is an `Authorization` header in no AWS scheme, such as `Bearer`, sent to a function URL: it reaches the function unsigned, as AWS passes it to a function whose URL has `AuthType` `NONE`.

## Browser pages

The AWS API checks no signature by default, and every client may sign with `test`/`test` even under `--sigv4 verify`, so understudy answers a call made from a web page only when the page is on a loopback origin (`localhost`, a name under it, or a loopback address) and on a port other than understudy's own. A page understudy served itself, such as an S3 object or a function URL response, is refused. So is a call a dev server proxies to understudy without rewriting the `Host` header, since the request then names the page's own port; let the proxy set `Host` to understudy's address (in Vite, `changeOrigin: true`). Serve the app from its own port, or make the call from an SDK outside the browser. Calls from the CLI and SDKs carry no `Origin` and are not affected.

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
      serve the AWS API to every peer that reaches a listener guarded by the admin token; the token guards only /_understudy/, and every client may sign with the public dummy pair test/test, which verifies even under --sigv4 verify, so such a peer can read every secret and object and run any image as a Lambda function. Required with the token beyond loopback
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
  --snapshot-max-bytes BYTES
      largest snapshot archive, counted decompressed, that save, load, upload and --persist accept, from 1048576 (1 MiB) to 4294967296 (4 GiB) (default 1073741824)
      variable UNDERSTUDY_SNAPSHOT_MAX_BYTES, file key "snapshotMaxBytes"
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
      signature checking: parse (default) reads credentials only; verify refuses a bad signature, an unknown key, a missing or expired session token, and expires presigned URLs on the emulator clock
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

Not supported yet: this build refuses s3.maxObjectBytes, dynamodb.registry rather than ignore them.

Accounts: a request signed with a 12-digit access key id is in that account; an accounts.keys
entry is in its account; any other key not minted by understudy, test among them, is in the
default account, 000000000000 unless accounts.default sets another. Under --sigv4 verify, test
and every 12-digit key sign with the secret test, an accounts.keys entry with its own secret,
and any other key understudy did not mint is refused.

Reference: docs/configuration.md in the understudy repository.
```
<!-- END serve --help -->
