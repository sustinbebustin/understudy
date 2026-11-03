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

## Layers

Publish a layer to understudy and name its version ARN in `Layers`. The function finds the layer's content under `/opt`, as on AWS:

```sh
mkdir -p layer/python
printf 'def hello():\n    return "hello from the layer"\n' > layer/python/greet.py
(cd layer && python3 -m zipfile -c ../layer.zip python)
aws lambda publish-layer-version --layer-name greet --zip-file fileb://layer.zip \
  --compatible-runtimes python3.13 --query LayerVersionArn --output text
```

```text
arn:aws:lambda:us-east-1:000000000000:layer:greet:1
```

```python
# handler.py
import greet

def handler(event, context):
    return {"greeting": greet.hello()}
```

```sh
python3 -m zipfile -c function.zip handler.py
aws lambda create-function --function-name layered --runtime python3.13 --handler handler.handler \
  --role arn:aws:iam::000000000000:role/worker --zip-file fileb://function.zip \
  --layers arn:aws:lambda:us-east-1:000000000000:layer:greet:1 --query Layers
aws lambda invoke --function-name layered out.json && cat out.json
```

```text
[
    {
        "Arn": "arn:aws:lambda:us-east-1:000000000000:layer:greet:1",
        "CodeSize": 264
    }
]
{
    "StatusCode": 200,
    "ExecutedVersion": "$LATEST"
}
{"greeting": "hello from the layer"}
```

- understudy merges a function's layers into `/opt` in the order listed, and mounts the merged tree read-only, or copies it into the container where a bind mount is not possible. A later layer's file replaces an earlier one's at the same path. AWS does not say which layer wins, so this is the fidelity switch `lambda.layers.merge-conflict`. A file in one layer where another has a directory fails the invoke with a `ServiceException` naming the path.
- understudy sets no `PYTHONPATH`, `NODE_PATH` or `PATH`. The runtimes in AWS's base images search `/opt/python`, `/opt/python/lib/python3.x/site-packages`, `/opt/nodejs/node_modules`, `/opt/bin` and `/opt/lib` themselves, and `provided.al2023` runs a layer's `/opt/bootstrap` when the package has none.
- Only layers published to understudy exist, because understudy never fetches a layer from AWS. An AWS-published layer such as Powertools is refused:

  ```sh
  aws lambda update-function-configuration --function-name layered \
    --layers arn:aws:lambda:us-east-1:017000801446:layer:AWSLambdaPowertoolsPythonV3-python313-x86_64:7
  ```

  ```text
  An error occurred (InvalidParameterValueException) when calling the UpdateFunctionConfiguration operation: Layer version arn:aws:lambda:us-east-1:017000801446:layer:AWSLambdaPowertoolsPythonV3-python313-x86_64:7 does not exist. understudy has only the layers published to it and never fetches layers from AWS; publish the archive to understudy with credentials for account 017000801446 and use the version that returns.
  ```

  Publish the layer's content to understudy yourself, in your own account or in its owner's (`AWS_ACCESS_KEY_ID=017000801446` signs as that account), and use the version ARN that returns.
- A layer of another account attaches whether or not its owner granted your account `lambda:GetLayerVersion` with `add-layer-version-permission`. AWS refuses an ungranted one; understudy journals an advisory (`understudy journal --kind advisory`) and attaches it (fidelity switch `lambda.layers.cross-account-policy`).
- `--compatible-runtimes` and `--compatible-architectures` filter `list-layers` and `list-layer-versions`. They are not checked when a layer is attached, because AWS's answer to a mismatch is unrecorded (fidelity switch `lambda.layers.compatibility`).
- A deleted layer version stays on the functions that use it and keeps working, but no function can add it.
- The function's code and its layers together may be 250 MB (262,144,000 bytes) unzipped.
- `Content.Location` in `get-layer-version` is a link understudy serves the archive at, not a presigned S3 URL.

## Calls from inside a function

understudy sets `AWS_ENDPOINT_URL` in the function's environment to an address the container can reach, and mints execution-role credentials for the function's role. An SDK built with default configuration therefore calls understudy, as that role, with no code change. Those calls appear in [the journal](journal.md) under the invocation that made them, and in the role's [generated policy](policy.md).

## Invoking

- `RequestResponse` (the default), `Event` and `DryRun` work. Event invocations queue on the emulator clock and retry function errors after 60 s and 120 s of emulator time, as AWS does.
- `--log-type Tail` returns the last 4 KB of the invocation's log, including `START`, `END` and `REPORT` lines.
- Function errors carry `X-Amz-Function-Error: Unhandled` and AWS's error payloads for exits, timeouts and out-of-memory kills.
- Timeouts, the init limit and `REPORT` durations are wall time: a frozen [clock](clock.md) never stops or shortens a running function.

## Destinations and dead-letter queues

When an asynchronous invocation ends, Lambda sends its invocation record to the on-success or on-failure destination, and a failed event also goes to the function's dead-letter queue. understudy does the same:

```python
# orders.py
def handler(event, context):
    raise Exception("order " + event["order"] + " failed")
```

```sh
python3 -m zipfile -c orders.zip orders.py
aws sqs create-queue --queue-name failed-orders
aws sqs create-queue --queue-name orders-dlq
aws lambda create-function --function-name orders --runtime python3.13 --handler orders.handler \
  --role arn:aws:iam::000000000000:role/worker --zip-file fileb://orders.zip \
  --dead-letter-config TargetArn=arn:aws:sqs:us-east-1:000000000000:orders-dlq
aws lambda put-function-event-invoke-config --function-name orders --maximum-retry-attempts 0 \
  --destination-config '{"OnFailure":{"Destination":"arn:aws:sqs:us-east-1:000000000000:failed-orders"}}'
aws lambda invoke --function-name orders --invocation-type Event --cli-binary-format raw-in-base64-out \
  --payload '{"order":"42"}' out.json
understudy settle
aws sqs receive-message --queue-url http://localhost:4566/000000000000/failed-orders \
  --query 'Messages[0].Body' --output text | python3 -m json.tool
```

The on-failure queue's message is the record:

```text
{
    "version": "1.0",
    "timestamp": "2026-10-27T03:49:13.665Z",
    "requestContext": {
        "requestId": "39b8e24a-f937-476b-911a-5e57733aaf6c",
        "functionArn": "arn:aws:lambda:us-east-1:000000000000:function:orders:$LATEST",
        "condition": "RetriesExhausted",
        "approximateInvokeCount": 1
    },
    "requestPayload": {
        "order": "42"
    },
    "responseContext": {
        "statusCode": 200,
        "executedVersion": "$LATEST",
        "functionError": "Unhandled"
    },
    "responsePayload": {
        "errorMessage": "order 42 failed",
        "errorType": "Exception",
        "requestId": "39b8e24a-f937-476b-911a-5e57733aaf6c",
        "stackTrace": [
            "  File \"/var/task/orders.py\", line 2, in handler\n    raise Exception(\"order \" + event[\"order\"] + \" failed\")\n"
        ]
    }
}
```

The dead-letter queue gets the event as it was sent, with the attributes AWS adds:

```sh
aws sqs receive-message --queue-url http://localhost:4566/000000000000/orders-dlq \
  --message-attribute-names All --query 'Messages[0].{Body: Body, Attributes: MessageAttributes}'
```

```text
{
    "Body": "{\"order\":\"42\"}",
    "Attributes": {
        "ErrorCode": {
            "StringValue": "200",
            "DataType": "Number"
        },
        "ErrorMessage": {
            "StringValue": "order 42 failed",
            "DataType": "String"
        },
        "RequestID": {
            "StringValue": "39b8e24a-f937-476b-911a-5e57733aaf6c",
            "DataType": "String"
        }
    }
}
```

- A destination is an SQS queue, an SNS topic, a function (invoked asynchronously), an EventBridge bus (default or custom; the event's `source` is `lambda` and its `detail-type` `Lambda Function Invocation Result - Success` or `- Failure`), or, on failure only, an S3 bucket (key `aws/lambda/async/<function>/YYYY/MM/DD/YYYY-MM-DDTHH.MM.SS-<uuid>`). The dead-letter target is an SQS queue or an SNS topic.
- Retries run on the emulator clock first. With the default two retries, the record waits for an attempt 60 s after the first failure and another 120 s after the second; on a frozen or slow clock, move it:

  ```sh
  understudy clock advance 1m --settle
  understudy clock advance 2m --settle
  ```

  The record then has `"approximateInvokeCount": 3`.
- The configuration of the qualifier invoked applies: `$LATEST`'s for an unqualified invoke, an alias's or version's own otherwise. An alias or version without one gets the defaults and no destination, as on AWS (fidelity switch `lambda.async.qualifier-config`). The dead-letter queue is set per function and serves every version.
- A destination function, queue or topic that does not exist is refused when you configure it, as on AWS; a missing bucket is refused in S3's words; a missing event bus is accepted, as AWS accepts it (fidelity switch `lambda.destination.config-existence-check`):

  ```text
  An error occurred (InvalidParameterValueException) when calling the PutFunctionEventInvokeConfig operation: The destination ARN arn:aws:sqs:us-east-1:000000000000:no-such-queue is invalid.
  ```

- `UpdateFunctionEventInvokeConfig` with one side of `DestinationConfig` keeps the other side, as on AWS.
- A FIFO queue is refused as a destination when you configure it, as on AWS. A FIFO topic is accepted but never delivered to. That failure, and any other failed delivery, is journaled as `dropped` with the metric AWS counts it in, and is not retried:

  ```sh
  understudy journal --kind delivery --errors
  ```

- Records and dead letters are sent as the function's execution role. understudy does not check the role's permissions, but `understudy iam policy --principal <role ARN>` lists the `sqs:SendMessage`, `sns:Publish`, `lambda:InvokeFunction`, `events:PutEvents`, `s3:ListBucket` and `s3:PutObject` statements the deliveries need (see [IAM policy](policy.md)).
- Synchronous invokes and SQS event source mappings produce no record, as on AWS.

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
- **DynamoDB stream and Kinesis stream event source mappings**: `TRIM_HORIZON` or `LATEST`, batch size, batching window, `MaximumRetryAttempts`, `MaximumRecordAgeInSeconds`, `ReportBatchItemFailures` and an SQS or SNS on-failure destination; see [DynamoDB Streams and Kinesis](streams.md). Kinesis stream consumers and `AT_TIMESTAMP` answer 501.
- **SNS subscriptions** with `protocol lambda` invoke asynchronously with the `Records[0].Sns` event.
- **EventBridge rules**, schedules included, invoke their Lambda targets; schedules fire on the emulator clock.
- **S3 event notifications** invoke functions asynchronously.

```sh
aws lambda create-event-source-mapping --function-name hello \
  --event-source-arn arn:aws:sqs:us-east-1:000000000000:jobs --batch-size 10
```

AWS checks a function's resource policy before SNS, S3 or EventBridge may invoke it. understudy checks it too, but by default only journals an advisory when AWS would deny the delivery. Under `--iam-enforce enforce` it refuses the delivery as AWS does ([Enforce](policy.md#enforce)). `understudy iam policy` lists the statements the deliveries need.

## Function URLs

```sh
aws lambda create-function-url-config --function-name hello --auth-type NONE
aws lambda add-permission --function-name hello --statement-id url --action lambda:InvokeFunctionUrl \
  --principal '*' --function-url-auth-type NONE
aws lambda add-permission --function-name hello --statement-id url-invoke --action lambda:InvokeFunction \
  --principal '*' --invoked-via-function-url
```

As on AWS, a public URL needs both statements; without them it answers 403. The URL has the form `http://<url-id>.lambda-url.us-east-1.localhost:4566/`. Where `*.localhost` does not resolve, use the path form `http://127.0.0.1:4566/_understudy/lambda-url/<url-id>/`. The path form shares understudy's own origin, so its answers carry `Content-Security-Policy: sandbox`, replacing any policy the function sets: a page served there runs without scripts or forms. HTTP clients and `fetch` calls are unaffected; open a browser app on the host form, its own origin, as on AWS. Payloads use format 2.0; responses are buffered, not streamed.

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

- An asynchronous invocation's record or dead letter that cannot be delivered is journaled and not retried. The execution role's permission to reach a destination, and the resource policy of a destination in another account, are evaluated only under `--iam-enforce advise`, which journals each denial, or `enforce`, which fails the delivery.
- `EphemeralStorage` is validated but not enforced. Reserved concurrency is enforced; the provisioned concurrency operations answer 501.
- AWS-published layers, such as Powertools, exist only once you publish their content to understudy.
- The LocalStack convention of creating a function from the `hot-reload` bucket is not supported; use `understudy lambda hot-reload` or the configuration file.
- A fault rule scoped to a resource matches Lambda's `Invoke` only; scope rules on other Lambda operations by operation, account or region.
