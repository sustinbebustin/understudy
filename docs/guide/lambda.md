# Lambda

Functions run in containers through the Docker Engine API: zip functions in AWS's official Lambda base images, image functions in your own image. Each container runs the runtime interface client its image ships with, and understudy serves the Lambda Runtime API it talks to (it does not use the Runtime Interface Emulator; [ADR 0010](../adr/0010-lambda-hosted-runtime-api.md) says why). The control plane (create, update, configure, permissions, mappings) works without Docker; invoking needs it.

## Runtimes

Zip functions run in these base images, pinned by digest. Each is pulled the first time a function needs it; `understudy images pull` pulls them ahead of time, which suits CI:

| Runtime | Image |
|---|---|
| `provided.al2023` | `public.ecr.aws/lambda/provided:al2023` |
| `python3.13`, `python3.12` | `public.ecr.aws/lambda/python` |
| `nodejs22.x`, `nodejs20.x` | `public.ecr.aws/lambda/nodejs` |
| `java21` | `public.ecr.aws/lambda/java` |
| `dotnet8` | `public.ecr.aws/lambda/dotnet` |
| `ruby3.3` | `public.ecr.aws/lambda/ruby` |

```sh
understudy images pull --runtime python3.13 --runtime provided.al2023
```

`Architectures` is honored: an `arm64` function on an `x86_64` host needs a QEMU binfmt handler, which `understudy doctor` checks for.

## A zip function

```python
# handler.py
import os, boto3

sqs = boto3.client("sqs")

def handler(event, context):
    print("got", event)
    url = sqs.get_queue_url(QueueName="results")["QueueUrl"]
    sqs.send_message(QueueUrl=url, MessageBody="done")
    return {"ok": True, "endpoint": os.environ.get("AWS_ENDPOINT_URL")}
```

```sh
python3 -m zipfile -c function.zip handler.py
aws sqs create-queue --queue-name results
aws lambda create-function --function-name hello --runtime python3.13 --handler handler.handler \
  --role arn:aws:iam::000000000000:role/worker --zip-file fileb://function.zip
aws lambda invoke --function-name hello --cli-binary-format raw-in-base64-out --payload '{"name":"world"}' out.json
```

The function is `Active` when `create-function` returns. The first invoke starts a container (a cold start of a few hundred milliseconds once the image is present); later invokes reuse it while it is warm. Concurrent invokes get separate containers.

A Go function for `provided.al2023` is a binary named `bootstrap`:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags lambda.norpc -o bootstrap .
python3 -m zipfile -c function.zip bootstrap
aws lambda create-function --function-name go-hello --runtime provided.al2023 --architectures x86_64 \
  --handler bootstrap --role arn:aws:iam::000000000000:role/worker --zip-file fileb://function.zip
```

## A container-image function

ECR is not emulated: build or tag the image locally under the `ImageUri` you pass, and understudy finds it in the local Docker image store.

```dockerfile
FROM public.ecr.aws/lambda/python:3.13
COPY app.py ${LAMBDA_TASK_ROOT}/
CMD ["app.handler"]
```

```sh
docker build -t 000000000000.dkr.ecr.us-east-1.amazonaws.com/hello:latest .
aws lambda create-function --function-name img-hello --package-type Image \
  --code ImageUri=000000000000.dkr.ecr.us-east-1.amazonaws.com/hello:latest \
  --role arn:aws:iam::000000000000:role/worker
aws lambda wait function-active-v2 --function-name img-hello
```

An image function is `Pending` until its image is resolved, so wait for `Active` as you would on AWS.

## Calls from inside a function

understudy sets `AWS_ENDPOINT_URL` in the function's environment to an address the container can reach, and mints execution-role credentials for the function's role. An SDK built with default configuration therefore calls understudy, as that role, with no code change. Those calls appear in [the journal](journal.md) under the invocation that made them, and in the role's [generated policy](policy.md).

## Invoking

- `RequestResponse` (the default), `Event` and `DryRun` work. Event invocations queue on the emulator clock and retry function errors after 60 s and 120 s of emulator time, as AWS does.
- `--log-type Tail` returns the last 4 KB of the invocation's log, including `START`, `END` and `REPORT` lines.
- Function errors carry `X-Amz-Function-Error: Unhandled` and AWS's error payloads for exits, timeouts and out-of-memory kills.
- Timeouts, the init limit and `REPORT` durations are wall time: a frozen [clock](clock.md) never stops or shortens a running function.

## Logs

Each function writes to the log group `/aws/lambda/<function>`, one stream per execution environment, named as on AWS:

```sh
aws logs describe-log-streams --log-group-name /aws/lambda/hello --query 'logStreams[].logStreamName'
aws logs filter-log-events --log-group-name /aws/lambda/hello --filter-pattern '"got"' --query 'events[].message'
```

## Hot reload

Point a function at a directory on the host and every change to it takes effect on the next invoke:

```sh
understudy lambda hot-reload hello ./fn
```

The directory holds what the zip would: `handler.py`, or a built `bootstrap`. understudy watches it and cold-starts the next invoke after a change. The Lambda API keeps reporting the deployed code (`CodeSha256` is unchanged), so a Terraform `plan` stays empty while you iterate. To bind at startup, list the functions in the configuration file:

```json
{"lambda": {"hotReload": [{"function": "hello", "dir": "/abs/path/to/fn"}]}}
```

While a binding is active it wins over `UpdateFunctionCode`; the update is stored and the journal says the binding shadows it. When understudy runs in a container, the directory must be given as understudy's own container sees it.

## Event sources and triggers

- **SQS event source mappings**: batch size, batching window (on the emulator clock), filter criteria, `ReportBatchItemFailures` and redrive to the queue's dead-letter queue after `maxReceiveCount`, as AWS does them.
- **SNS subscriptions** with `protocol lambda` invoke asynchronously with the `Records[0].Sns` event.
- **EventBridge rules**, schedules included, invoke their Lambda targets; schedules fire on the emulator clock.
- **S3 event notifications** invoke functions asynchronously.

```sh
aws lambda create-event-source-mapping --function-name hello \
  --event-source-arn arn:aws:sqs:us-east-1:000000000000:jobs --batch-size 10
```

AWS checks a function's resource policy before SNS, S3 or EventBridge may invoke it. understudy does not enforce it, but journals an advisory when the delivery would be denied on AWS, and `understudy iam policy` lists the statements the deliveries need.

## Function URLs

```sh
aws lambda create-function-url-config --function-name hello --auth-type NONE
aws lambda add-permission --function-name hello --statement-id url --action lambda:InvokeFunctionUrl \
  --principal '*' --function-url-auth-type NONE
aws lambda add-permission --function-name hello --statement-id url-invoke --action lambda:InvokeFunction \
  --principal '*' --invoked-via-function-url
```

As on AWS, a public URL needs both statements; without them it answers 403. The URL has the form `http://<url-id>.lambda-url.us-east-1.localhost:4566/`. Where `*.localhost` does not resolve, use the path form `http://127.0.0.1:4566/_understudy/lambda-url/<url-id>/`. Payloads use format 2.0; responses are buffered, not streamed.

## Networking

understudy detects how containers reach it the first time a function runs, and `understudy doctor` reports what it found:

| Where understudy runs | How functions reach it |
|---|---|
| Linux host, Docker bridge network | A listener on the bridge gateway (such as `172.17.0.1`) serving the AWS API only; `localhost:<port>` URLs inside functions also reach the host |
| Docker Desktop (macOS) | `host.docker.internal`, forwarded to understudy on the host's loopback. Supported by design, not yet tested |
| In a container (the image) | understudy's own container network; functions join it |
| Rootless Docker, rootless Podman | Not supported; understudy says so when a function would start |

On a Linux host functions reach only the AWS API and the read-only health and clock endpoints. On Docker Desktop and in a container they call the main listener, so function code can reach the whole admin API; [SECURITY.md](../../SECURITY.md) says when an admin token closes that.

`lambda.network` in the configuration file overrides detection: `bridge` with a `bridgeGateway` address, or `disabled` for the control plane only. Each execution environment gets its own Runtime API listener on the same address. Containers are labeled, removed when understudy stops, and swept on the next start or by `understudy cleanup` if understudy crashed.

## Limitations

- Asynchronous invocation destinations are stored and not delivered.
- `EphemeralStorage` is validated but not enforced; reserved and provisioned concurrency are stored.
- The LocalStack convention of creating a function from the `hot-reload` bucket is not supported; use `understudy lambda hot-reload` or the configuration file.
- A fault rule scoped to a resource matches Lambda's `Invoke` only; scope rules on other Lambda operations by operation, account or region.
