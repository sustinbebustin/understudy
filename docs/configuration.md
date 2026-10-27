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
- Presigned S3 URLs expire on the emulator clock. A SigV4 URL lives `X-Amz-Expires` of emulator time from the moment it was signed, so a URL presigned after `understudy clock advance` lives its full lifetime, and advancing the clock past its expiry makes it answer 403 `AccessDenied` "Request has expired", whether the clock is running, frozen or scaled. `X-Amz-Date` has whole-second precision, so under `verify` a clock change takes effect for presigned URLs from the next whole second, and an admin answer given before then (the call that changed the clock, or any other) waits for it; only a URL signed while that call is in flight can be placed on the wrong side of the change. A SigV2 URL carries only its `Expires` instant, not when it was signed, so it is compared with emulator time directly and expires as intended only while the clock has not been moved away from wall time since it was signed; presign with SigV4 when a test moves the clock (boto3 presigns with SigV2 in `us-east-1` unless its client has `Config(signature_version="s3v4")`). Under `parse` a presigned URL never expires.
- Signed `aws-chunked` uploads (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD` and its `-TRAILER` form) verify every chunk and trailer signature.

A function URL refuses in the JSON services' words with status 403, as its front end answers its other refusals; that status for a signature it refuses is inferred from user reports, not recorded. A refused request is journaled as a failed call. Neither mode logs a secret or a session token. Credentials that cannot be read at all are refused in both modes, on every route. The one exception is an `Authorization` header in no AWS scheme, such as `Bearer`, sent to a function URL: it reaches the function unsigned, as AWS passes it to a function whose URL has `AuthType` `NONE`.

## Browser pages

The AWS API checks no signature by default, and every client may sign with `test`/`test` even under `--sigv4 verify`, so understudy answers a call made from a web page only when the page is on a loopback origin (`localhost`, a name under it, or a loopback address) and on a port other than understudy's own. A page understudy served itself, such as an S3 object or a function URL response, is refused. So is a call a dev server proxies to understudy without rewriting the `Host` header, since the request then names the page's own port; let the proxy set `Host` to understudy's address (in Vite, `changeOrigin: true`). Serve the app from its own port, or make the call from an SDK outside the browser. Calls from the CLI and SDKs carry no `Origin` and are not affected.

## Docker

Lambda and DynamoDB Local run on the Docker Engine `--docker` names, else `DOCKER_HOST`, else `unix:///var/run/docker.sock`. Besides a unix socket, a `tcp://<host>:<port>` daemon is read as the Docker CLI reads it: no port means 2375 and no host means `localhost`. With `DOCKER_TLS_VERIFY` set to anything non-empty, understudy speaks TLS, verifies the daemon against `ca.pem` and presents `cert.pem` and `key.pem`, all from `DOCKER_CERT_PATH`, else `DOCKER_CONFIG`, else `~/.docker`. A missing or unreadable file, a refused connection, or TLS on one side only fails the first container call, and `understudy doctor`, with what to change. A `DOCKER_HOST` understudy cannot use, such as `ssh://` or a `tcp://` address with a path, turns containers off with a warning; the same value in `--docker` is refused.

A daemon on another host, such as a docker:dind CI service, is detected when the first container is needed: functions reach understudy at its own address on the daemon's network, so understudy must run on that network ([CI guide](guide/ci.md#docker-in-docker)). `understudy doctor` reports the daemon, its transport and the topology.

`--docker-labels` (`UNDERSTUDY_DOCKER_LABELS`, file key `dockerLabels`) adds labels to every container understudy creates, function environments and DynamoDB Local, beside understudy's own `understudy.dev/` labels. `serve` creates no Docker network. The value is a JSON object of string values; the file takes the object itself or a string holding its text, and the source that sets it replaces the others whole. It is how a Testcontainers module that runs understudy in a container hands over the session's reaper labels, such as `{"org.testcontainers.session-id":"<id>"}`, so Ryuk also removes the containers understudy started when the test process dies. Keys under `understudy.dev/` are refused (`label key "understudy.dev/managed" is reserved for understudy's own labels; choose a key outside understudy.dev/`), so the sweeps and `understudy cleanup`, which select only those labels, remove what they did before.

## Outbound HTTP delivery

understudy makes no outbound HTTP request unless a setting allows it. `--sns-http-delivery-allow` (`UNDERSTUDY_SNS_HTTP_DELIVERY_ALLOW`, file key `sns.httpDelivery.allow`) lists the hosts SNS delivers `http` and `https` subscriptions to. It is empty by default: such a subscription stays pending and receives nothing, and each delivery is journaled as `refused`, with the entry that would allow it. [SNS HTTP delivery](guide/sns-http.md) walks through a receiver, retries and signature validation.

Each entry is `host:port`. The host is `localhost`, an IPv4 literal, a bracketed IPv6 literal such as `[::1]`, or a DNS name; the port is 1 to 65535, or `*` for any port, meant for a test receiver on an ephemeral port. There are no host wildcards and no address ranges. The flag and the variable take the entries comma-separated, untrimmed; the file takes a JSON array, nested in its `sns` section:

```json
{"sns": {"httpDelivery": {"allow": ["127.0.0.1:*", "receiver:8080"]}}}
```

The source that sets the list replaces the others whole. A malformed entry, or one naming a host understudy never reaches, stops `serve` with exit code 2, naming the entry by its index from 0.

A subscription's URL is allowed when its host, lowercased and without a trailing dot, equals an entry's host, and its port (80 for `http` and 443 for `https` when the URL names none) equals the entry's port or the entry's port is `*`; the scheme does not matter. The address is checked again once the name resolves, and the address checked is the one dialed:

- A `localhost` entry reaches only loopback.
- An IP literal entry reaches that address.
- A DNS-name entry reaches any address but loopback, which only `localhost` or an IP literal entry lists. Private addresses (RFC 1918 and IPv6 ULA) are allowed, so a compose service such as `receiver:8080` or a Kubernetes service such as `receiver.default.svc.cluster.local:8080` can be listed by name.

Whatever the list says, understudy never delivers to a name under an AWS domain (in the URL or anywhere in its CNAME chain), to the instance metadata and ECS credentials addresses, to any link-local, unspecified or multicast address, to its own listeners (main, TLS, container and each Runtime API) or to a `tcp://` Docker Engine. The Engine's host is resolved once, when `serve` starts; if it does not resolve then, `serve` logs a warning and cannot refuse the Engine's addresses, so do not list a name that resolves to the Engine. A delivery uses no proxy, follows no redirect, speaks HTTP/1.1 and gives up after 15 seconds.

`--sns-http-delivery-ca-bundle` (`UNDERSTUDY_SNS_HTTP_DELIVERY_CA_BUNDLE`, file key `sns.httpDelivery.caBundle`) names a PEM file of CA certificates an `https` endpoint may chain to, beside the system roots; certificate verification is never skipped. The file is read at start, and one that cannot be read, holds no certificate or holds one that does not parse stops `serve`.

While `--tls` serves, the `SubscribeURL`, `UnsubscribeURL` and `SigningCertURL` an HTTP delivery carries are on `https://localhost:<TLS port>`; otherwise they are on the address the calling client used, unless that address is an AWS name, in which case they are on `localhost` at the port the client used. They never name an AWS host.

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
  --iam-enforce MODE
      IAM enforcement: off (default) records which actions each principal uses and enforces nothing; advise and enforce are not built yet, so this build refuses them
      variable UNDERSTUDY_IAM_ENFORCE, file key "enforce" in the "iam" section
  --sns-http-delivery-allow HOST:PORT,...
      hosts SNS delivers http and https subscriptions to, as comma-separated host:port entries: the host localhost, an IP literal ([v6] for IPv6) or a DNS name, the port 1-65535 or * (default none: nothing is delivered). AWS hosts, link-local and metadata addresses and understudy's own listeners are refused whatever the list says
      variable UNDERSTUDY_SNS_HTTP_DELIVERY_ALLOW, file key "httpDelivery.allow" in the "sns" section
  --sns-http-delivery-ca-bundle FILE
      PEM file of CA certificates an https subscription endpoint may chain to, beside the system roots (default none)
      variable UNDERSTUDY_SNS_HTTP_DELIVERY_CA_BUNDLE, file key "httpDelivery.caBundle" in the "sns" section
  --docker URL
      Docker Engine for Lambda and DynamoDB Local: unix://<socket>, tcp://<host>:<port> (TLS when DOCKER_TLS_VERIFY is set, with ca.pem, cert.pem and key.pem from DOCKER_CERT_PATH), or disabled (default DOCKER_HOST, else unix:///var/run/docker.sock)
      variable UNDERSTUDY_DOCKER, file key "docker"
  --docker-labels JSON
      labels added to every container understudy creates, function environments and DynamoDB Local, as a JSON object of string values, such as {"org.testcontainers.session-id":"<id>"} so a Testcontainers reaper also removes them; keys under understudy.dev/ are understudy's own and refused (default none)
      variable UNDERSTUDY_DOCKER_LABELS, file key "dockerLabels"
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
    "sns": {"httpDelivery": {"allow": ["127.0.0.1:8080"]}},
    "iam": {"assumeRole": "strict", "enforce": "off"},
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
  dynamodb.enabled           true, or false to turn DynamoDB off so DynamoDB Local is never
                             pulled; on, it is pulled on first use and runs under Amazon's
                             licence terms
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
