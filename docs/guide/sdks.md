# SDKs and the AWS CLI

Every current AWS SDK and the AWS CLI v2 read `AWS_ENDPOINT_URL`, so `eval "$(understudy env)"` is the whole setup for code that builds its clients from the default configuration. The snippets below were run that way; none of them names understudy.

The one setting no variable carries is S3 path-style addressing in the Go and JS SDKs:

- At the `http://127.0.0.1:4566` endpoint `understudy env` prints, both SDKs already address S3 path style, since a bucket name cannot go in front of an IP address. The setting in the snippets below changes nothing there.
- At a host-name endpoint such as `http://localhost:4566`, the SDKs address S3 virtual-hosted (`http://my-bucket.s3.localhost:4566/`). That works only where `*.localhost` names resolve, which `understudy doctor` checks; everywhere else, set path style.

## Go (aws-sdk-go-v2)

```go
cfg, err := config.LoadDefaultConfig(ctx) // reads AWS_ENDPOINT_URL, credentials and region
if err != nil {
	log.Fatal(err)
}

s3c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
	Bucket: aws.String("go-demo"), Key: aws.String("hello.txt"), Body: strings.NewReader("hello"),
})

sqsc := sqs.NewFromConfig(cfg)
q, err := sqsc.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("go-demo")})
```

### Go tests with `understudytest`

The `understudytest` package starts an in-process understudy per test on a free loopback port, with its own state, and stops it when the test ends. Tests can run in parallel. This test sends a message with a 15-minute delay and receives it without waiting:

```go
func TestDelayedMessage(t *testing.T) {
	ctx := t.Context()
	emu := understudytest.Start(t, understudytest.Options{
		Clock:  understudytest.Frozen{At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		Docker: understudytest.DockerDisabled{},
	})
	for k, v := range emu.Env() {
		t.Setenv(k, v)
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client := sqs.NewFromConfig(cfg)

	q, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("jobs")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: q.QueueUrl, MessageBody: aws.String("later"), DelaySeconds: 900,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := emu.Admin().ChangeClock(ctx, admin.Advance{By: 15 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(out.Messages))
	}
}
```

`emu.Admin()` is an `admin.Client` with the whole admin API: clock, faults, journal, snapshots, resources, IAM usage. `emu.Settle(ctx)` waits until deliveries and invocations are done. Both packages are public and experimental until the first release. To run understudy as a container from a testcontainers-go, Python or Node suite instead, see [Testcontainers](testcontainers-modules.md).

## Python (boto3)

boto3 needs nothing beyond the environment, and uses path-style S3 with a custom endpoint by default:

```python
import io

import boto3

s3 = boto3.client("s3")
sqs = boto3.client("sqs")

s3.create_bucket(Bucket="py-demo")
s3.upload_fileobj(io.BytesIO(b"x" * (20 * 1024 * 1024)), "py-demo", "big.bin")  # a multipart upload

url = sqs.create_queue(QueueName="py-demo")["QueueUrl"]
sqs.send_message(QueueUrl=url, MessageBody="hello")
print(sqs.receive_message(QueueUrl=url)["Messages"][0]["Body"])
```

Tests in any language can drive the clock and the other admin features over HTTP:

```sh
curl -s -X POST http://127.0.0.1:4566/_understudy/v1/clock -H 'Content-Type: application/json' -d '{"op":"advance","by":"90s"}'
curl -s 'http://127.0.0.1:4566/_understudy/v1/settle?timeout=5s'
curl -s -X POST http://127.0.0.1:4566/_understudy/v1/reset -H 'Content-Type: application/json' -d '{}'
```

[examples/python-worker](../../examples/python-worker/) has a pytest suite built this way.

## JavaScript (AWS SDK v3)

```js
import { S3Client, CreateBucketCommand, PutObjectCommand } from "@aws-sdk/client-s3";
import { SQSClient, CreateQueueCommand, SendMessageCommand, ReceiveMessageCommand } from "@aws-sdk/client-sqs";

const s3 = new S3Client({ forcePathStyle: true });
await s3.send(new CreateBucketCommand({ Bucket: "js-demo" }));
await s3.send(new PutObjectCommand({ Bucket: "js-demo", Key: "hello.txt", Body: "hello" }));

const sqs = new SQSClient({});
const { QueueUrl } = await sqs.send(new CreateQueueCommand({ QueueName: "js-demo" }));
await sqs.send(new SendMessageCommand({ QueueUrl, MessageBody: "hello" }));
const { Messages } = await sqs.send(new ReceiveMessageCommand({ QueueUrl }));
console.log(Messages[0].Body);
```

This runs unchanged under Node and Bun.

## AWS CLI

```sh
eval "$(understudy env --sdk cli)"
aws s3 mb s3://demo-bucket
aws s3 cp hello.txt s3://demo-bucket/hello.txt
aws sns create-topic --name orders
aws sqs create-queue --queue-name orders-worker
```

The CLI's shorthand syntax cannot hold JSON values, so pass attributes such as a filter policy as JSON:

```sh
aws sns subscribe --topic-arn arn:aws:sns:us-east-1:000000000000:orders --protocol sqs \
  --notification-endpoint arn:aws:sqs:us-east-1:000000000000:orders-worker \
  --attributes '{"RawMessageDelivery":"true","FilterPolicy":"{\"kind\":[\"order\"]}"}'
```

## TLS

Some SDK behavior only happens over HTTPS: the Go SDK, boto3 and the AWS CLI send streaming S3 uploads with `aws-chunked` checksum trailers there, and with a signed payload hash and checksum headers over plain HTTP. To test the HTTPS path, start a second listener with a self-signed certificate and point the clients at it:

```sh
understudy serve --tls
eval "$(understudy env --tls)"
```

The TLS listener is on port 4567. `env --tls` writes the certificate to the user cache directory (or `--ca-file`) and sets `AWS_CA_BUNDLE`, and `NODE_EXTRA_CA_CERTS` for Node. Without `--persist` the certificate is new at every start, so run `env --tls` again after a restart. With `--persist` it is kept in the data directory (`tls.crt`, and `tls.key` readable by its owner only) and the CA bundle keeps working across restarts. A start replaces a kept certificate that is unreadable, expires within a day, or differs from the one this build issues, and logs why at warn level:

```
level=WARN msg="replaced the stored TLS certificate with a new one; a CA bundle written before this start is stale, so run understudy env --tls again" reason="tls.crt and tls.key are not a certificate and its key: tls: failed to find any PEM data in certificate input" dir=...
```

## S3 SSE-C and Object Lock

AWS refuses SSE-C (customer-provided keys) over plain HTTP, and, since April 2026, on every new bucket until its encryption configuration unblocks it. understudy does both, so SSE-C needs the TLS listener and an unblocked bucket:

```sh
understudy serve --tls
eval "$(understudy env --tls --sdk cli)"
aws s3api create-bucket --bucket ssec-demo
aws s3api put-bucket-encryption --bucket ssec-demo --server-side-encryption-configuration \
  '{"Rules":[{"BlockedEncryptionTypes":{"EncryptionType":["NONE"]}}]}'
aws s3api put-object --bucket ssec-demo --key secret.txt --body secret.txt \
  --sse-customer-algorithm AES256 --sse-customer-key 0123456789abcdef0123456789abcdef
aws s3api get-object --bucket ssec-demo --key secret.txt \
  --sse-customer-algorithm AES256 --sse-customer-key 0123456789abcdef0123456789abcdef out.txt
```

`get-object` writes the object to `out.txt` and echoes the algorithm and the key's MD5 (trimmed):

```text
    "SSECustomerAlgorithm": "AES256",
    "SSECustomerKeyMD5": "hRasmdxgYDKV3nvbahU1MA=="
```

The refusals are AWS's:

| Request | Error |
|---|---|
| `put-object` with SSE-C into a bucket that still blocks it | `AccessDenied` "Access Denied" |
| `put-object` or `get-object` with SSE-C headers over plain HTTP, blocked or not | `InvalidArgument` "Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection." |
| `get-object` without the key | `InvalidRequest` "The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object." |
| `get-object` with another key | `AccessDenied` |

understudy never stores the key. It keeps a salted HMAC of the key to check reads, and stores the object's bytes as sent, unencrypted.

Object Lock retention runs on the [emulator clock](clock.md), so a test moves the clock past the retain-until date instead of waiting for it:

```sh
understudy serve --clock frozen@2026-01-01T00:00:00Z
eval "$(understudy env --sdk cli)"
aws s3api create-bucket --bucket lock-demo --object-lock-enabled-for-bucket
VERSION=$(aws s3api put-object --bucket lock-demo --key report.csv --body report.csv \
  --object-lock-mode COMPLIANCE --object-lock-retain-until-date 2026-01-02T00:00:00Z \
  --query VersionId --output text)
aws s3api delete-object --bucket lock-demo --key report.csv --version-id "$VERSION"  # AccessDenied
understudy clock advance 24h
aws s3api delete-object --bucket lock-demo --key report.csv --version-id "$VERSION"  # deleted
```

The first delete and the clock advance print:

```text
An error occurred (AccessDenied) when calling the DeleteObject operation: Access Denied because object protected by object lock.
2026-01-02T00:00:00Z frozen
```

- **Refused deletes:** until the clock reaches the date, the versioned delete fails with 403 `AccessDenied`, as above. A delete without `--version-id` succeeds, because it only adds a delete marker.
- **Bypass:** `--bypass-governance-retention` lifts GOVERNANCE retention but never COMPLIANCE retention. understudy does not evaluate `s3:BypassGovernanceRetention` ([differences](differences.md#s3)).
- **Legal holds:** `put-object-legal-hold --legal-hold Status=ON` blocks deletes, bypass or not, until the hold is set to `OFF`.
- **Default retention:** a bucket default from `put-object-lock-configuration` dates each new version from its creation. A `put-object` into such a bucket needs `Content-MD5` or a checksum, which the AWS CLI and current SDKs send by default.
- **Reset:** `understudy reset` drops locked versions along with everything else, so COMPLIANCE retention never outlives a test run.

## Accounts in tests

A 12-digit access key id selects that account, so separate test suites can share one server without seeing each other's resources:

```sh
eval "$(understudy env --account 111111111111)"
```
