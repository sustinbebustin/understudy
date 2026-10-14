# SDKs and the AWS CLI

Every current AWS SDK and the AWS CLI v2 read `AWS_ENDPOINT_URL`, so `eval "$(understudy env)"` is the whole setup for code that builds its clients from the default configuration. The snippets below were run that way; none of them names understudy.

The one setting no variable carries is S3 path-style addressing in the Go and JS SDKs. At the `http://127.0.0.1:4566` endpoint `understudy env` prints, both SDKs already address S3 path style, since a bucket cannot go in front of an IP address; the setting in the snippets below changes nothing there. It matters at a host-name endpoint: at `http://localhost:4566` the SDKs address S3 virtual-hosted, which works only where `*.localhost` names resolve (`understudy doctor` checks), and need the path-style setting everywhere else. Virtual-hosted requests such as `http://my-bucket.s3.localhost:4566/` need that localhost endpoint.

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

`emu.Admin()` is an `admin.Client` with the whole admin API: clock, faults, journal, snapshots, resources, IAM usage. `emu.Settle(ctx)` waits until deliveries and invocations are done. Both packages are public and experimental until the first release.

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

The TLS listener is on port 4567. `env --tls` writes the certificate to the user cache directory (or `--ca-file`) and sets `AWS_CA_BUNDLE`, and `NODE_EXTRA_CA_CERTS` for Node. The certificate is new at every start, so run `env --tls` again after a restart.

## Accounts in tests

A 12-digit access key id selects that account, so separate test suites can share one server without seeing each other's resources:

```sh
eval "$(understudy env --account 111111111111)"
```
