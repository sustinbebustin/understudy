# SNS -> SQS -> Lambda with Terraform

An event-driven order pipeline, deployed to understudy with the standard Terraform AWS provider:

```text
aws sns publish
  -> SNS topic "orders"
       -> SQS queue "orders"            raw delivery; filter: event_type = order.created
            -> Lambda "order-worker"    Go on provided.al2023; event source mapping, batches of 10,
                                        ReportBatchItemFailures
                 -> S3: orders/<orderId>.json
       a message that fails twice -> SQS queue "orders-dlq"
```

The function runs in AWS's own `provided.al2023` base image under Docker, and its AWS SDK calls come back to understudy without any setting in the code. Its logs land in CloudWatch Logs.

| Path | What it is |
|---|---|
| `main.tf` | The whole pipeline. Only the provider block is set up for a local endpoint |
| `function/` | The worker, a Go module using `aws-lambda-go` and `aws-sdk-go-v2` |
| `.terraform.lock.hcl` | Pinned provider checksums |
| `demo.sh` | The commands below, in one script, with checks of every result |

## Run it

You need understudy (from the repository root, `go install ./cmd/understudy`), Docker, Go 1.27, Terraform 1.6 or later, the AWS CLI and `jq`. The first run downloads the Terraform providers and pulls the Lambda base image.

Start understudy in one terminal:

```sh
understudy serve
```

In another terminal, from this directory, point the shell at understudy and run the demo:

```sh
eval "$(understudy env)"
./demo.sh
```

It takes about four minutes, almost all of it in Terraform: the AWS provider polls every SQS queue it creates for 25 seconds, and every queue it deletes for 42 seconds, whatever the backend. `KEEP=1 ./demo.sh` leaves the pipeline deployed for you to explore; `terraform destroy -auto-approve` removes it.

### Step by step

Build the worker for Linux. Use `GOARCH=arm64` and `-var architecture=arm64` below on an ARM machine.

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -C function -tags lambda.norpc -trimpath -o ../build/bootstrap .
```

Deploy:

```sh
terraform init
terraform apply -auto-approve -var architecture=x86_64
```

Publish three messages:

```sh
topic=$(terraform output -raw topic_arn)
aws sns publish --topic-arn "$topic" --message '{"orderId": "1001", "amount": 42.5}' \
  --message-attributes '{"event_type": {"DataType": "String", "StringValue": "order.created"}}'
aws sns publish --topic-arn "$topic" --message '{"orderId": "1002", "amount": 10}' \
  --message-attributes '{"event_type": {"DataType": "String", "StringValue": "order.cancelled"}}'
aws sns publish --topic-arn "$topic" --message 'not an order' \
  --message-attributes '{"event_type": {"DataType": "String", "StringValue": "order.created"}}'
understudy settle
```

`understudy settle` waits until nothing is in flight: no SNS delivery, event source mapping poll or Lambda invocation. Then:

- Order 1001 is stored in S3. Order 1002 never reached the queue, because the filter policy dropped it.

  ```sh
  aws s3 ls s3://understudy-example-orders/orders/
  ```

- The worker reported `not an order` as a batch item failure, so the message is back on the queue, hidden for the 60-second visibility timeout. Instead of waiting it out twice, move the emulator clock. After the second failure, the next receive moves the message to the dead-letter queue (`maxReceiveCount` is 2):

  ```sh
  understudy clock advance 61s && understudy settle
  understudy clock advance 61s && understudy settle
  aws sqs receive-message --queue-url "$(terraform output -raw dlq_url)" --query 'Messages[].Body'
  ```

- The worker logs JSON, so a CloudWatch Logs filter pattern can match on a field:

  ```sh
  aws logs filter-log-events --log-group-name /aws/lambda/order-worker \
    --filter-pattern '{ $.msg = "order failed" }' --query 'events[].message'
  ```

- The journal shows everything one publish caused. Find the event id of the publish, then print its tree:

  ```sh
  understudy journal --operation sns.Publish
  understudy journal trace <id>
  ```

  The output, with the time and account columns cut:

  ```text
  139  api-call    sns.Publish ok
    141  api-call    sqs.SendMessage ok
      147  api-call    lambda.Invoke ok
        153  api-call    logs.CreateLogGroup failed ResourceAlreadyExistsException 400 request-id 6a345262-...
        156  api-call    logs.CreateLogStream ok
        158  api-call    logs.PutLogEventsInternal ok
      148  api-call    s3.PutObject ok
      149  invocation  order-worker ok cold 5.133359ms
      150  api-call    sqs.DeleteMessageBatch ok
    142  delivery    sns-to-sqs orders -> orders attempt 1 delivered
  ```

  The publish delivered the message to the queue (141, 142), the event source mapping invoked the worker (147, 149), the worker wrote the order (148), and the mapping deleted the message (150). The log lines are Lambda delivering the worker's output to CloudWatch Logs; the group already existed because Terraform created it.

Remove everything:

```sh
terraform destroy -auto-approve -var architecture=x86_64
```

## Deploying to AWS

Only the `provider "aws"` block in `main.tf` is local-specific. Remove `s3_use_path_style` and the three `skip_*` settings, unset `AWS_ENDPOINT_URL`, and pick a globally unique `bucket_name`. Unlike understudy, AWS enforces IAM, so the role's policy in `main.tf` lists what the worker needs.
