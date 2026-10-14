# R05

- Date: 2026-10-14T06:33Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Understudy paths are under /home/austin/dev/local-aws-stack. Understudy values come from replaying r05.sh against a local understudy. RedriveAllowPolicy was set on the DLQ by set-queue-attributes (01-set-rap-on-dlq), not at create.

- SQS RedrivePolicy bytes (01-get-a): matches (compact, `deadLetterTargetArn` first, `maxReceiveCount` a number: `{"deadLetterTargetArn":"...","maxReceiveCount":3}` from input `{"maxReceiveCount": 3, "deadLetterTargetArn": "..."}`).
- SQS RedriveAllowPolicy bytes (01-get-dlq): matches (compact `{"redrivePermission":"byQueue","sourceQueueArns":["..."]}`).
- SQS Policy bytes (01-get-a): differs: AWS compacts whitespace and keeps key order (`{"Version":"2012-10-17","Statement":[{"Sid":"a","Effect":"Allow",...}]}`, Sid before Effect, so not sorted); understudy stores verbatim with spaces (internal/service/sqs/attributes.go:234). Contradicts tf-messaging.md 10 item 6 "Store Policy verbatim".
- tf-messaging hypothesis "compact sorted JSON for the redrive attributes": compact confirmed; the recorded key orders are alphabetical, so sorted and fixed order are indistinguishable.
- SQS defaults, standard (02-get-default): matches (MaximumMessageSize 1048576, SqsManagedSseEnabled true, ReceiveMessageWaitTimeSeconds 0, DelaySeconds 0, MessageRetentionPeriod 345600, VisibilityTimeout 30).
- SQS defaults, FIFO (02-get-default-fifo): matches (FifoQueue true, DeduplicationScope queue, FifoThroughputLimit perQueue, ContentBasedDeduplication false, SqsManagedSseEnabled true).
- SQS KmsMasterKeyId=alias/aws/sqs (02-get-kms): matches (SqsManagedSseEnabled false, KmsDataKeyReusePeriodSeconds 300).
- SQS set Policy= RedrivePolicy= (03-set-empty, 03-get-after-empty): matches (200 empty body; both keys gone).
- step 4 5xx query error: n/a (skipped by design).
- SNS topic Policy bytes, Id, Version 2008-10-17 (05-get-topic-attrs): matches byte for byte (account aside).
- SNS EffectiveDeliveryPolicy bytes; no DeliveryPolicy key: matches.
- SNS DisplayName "" (XML `<value/>`): matches.
- SNS SubscriptionsConfirmed after an SQS subscribe (05-get-topic-attrs-after-sub): differs: AWS still "0" seconds after the subscription confirmed (count lags); understudy "1" at once (internal/service/sns/topic_attributes.go:235). Eventual-consistency lag.
- SNS FilterPolicy bytes (05-get-sub-attrs): differs: AWS compacts `{ "a": ["x"] }` to `{"a":["x"]}`; understudy stores verbatim (internal/service/sns/subscription_attrs.go:109-116, 133).
- SNS subscription RedrivePolicy bytes: matches (AWS keeps it verbatim, space included: `{"deadLetterTargetArn": "..."}`).
- SNS RawMessageDelivery "true", FilterPolicyScope MessageAttributes, PendingConfirmation false, ConfirmationWasAuthenticated true: matches.
- SNS SubscriptionPrincipal: n/a (AWS shows the caller's role ARN, arn:aws:iam::123456789012:role/managed/AccountFullAccessRole; understudy shows its caller's root ARN; caller-dependent).
- SNS list-subscriptions-by-topic before/after the queue policy (05-list-subs-*): matches (same-account SQS auto-confirms; real SubscriptionArn listed both times).
- step 6 cross-account confirmation: n/a (skipped by design).

