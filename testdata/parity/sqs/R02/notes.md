# R02

- Date: 2026-10-14T06:32Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Understudy paths are under /home/austin/dev/local-aws-stack. Move-task data is from std-06-*/fifo-06-* (r02b.sh); the r02.sh step 6 did not run.

- move happens at the receive attempt that exceeds maxReceiveCount (std-03-m*, fifo-03-m1-recv3): matches (message gone from source on the 3rd receive; FIFO's 3rd receive returns the next message of the group instead; internal/service/sqs/receive.go:212).
- DLQ MessageId same as source (std-04-*, fifo-04-*): matches.
- DLQ ApproximateReceiveCount carried, not reset (first DLQ receive shows 3): matches.
- DLQ SentTimestamp, standard: matches (original kept).
- DLQ SentTimestamp, FIFO (fifo-04-dlq-poll-0s): differs: AWS keeps the original SentTimestamp; understudy sets Sent = now on a FIFO DLQ (receive.go:311).
- DLQ ApproximateFirstReceiveTimestamp kept: matches.
- DLQ DeadLetterQueueSourceArn present: matches.
- DLQ MD5OfBody, message attributes, MD5OfMessageAttributes retained: matches.
- DLQ FIFO MessageGroupId kept, MessageDeduplicationId = MessageId, new SequenceNumber: matches (receive.go:310-312).
- DLQ poll with VisibilityTimeout 0, MaxNumberOfMessages 10 (std-04-dlq-poll-0s): differs: AWS returned the same message 3 times in one response (ReceiveCount 3,4,5); understudy returns a message once per receive. Sampling quirk.
- queue counts over time (std-05-*, fifo-05-*): matches (DLQ 3 visible; source 0; std-05-attrs-src-1s shows a transient 1, approximate count).
- GetQueueAttributes RedrivePolicy bytes (std-05-attrs-src-0s): matches (`{"deadLetterTargetArn":"...","maxReceiveCount":2}`, count a number though sent as "2").
- TaskHandle (std-06-taskhandle-decoded.txt): matches (base64 of `{"taskId":"<uuid>","sourceArn":"<arn>"}`).
- ListMessageMoveTasks fields (std-06-list-*, fifo-06-list-*, 07-list-running): matches (TaskHandle only while RUNNING; MaxNumberOfMessagesPerSecond only when set; no DestinationArn/FailureReason when unset; StartedTimestamp epoch ms).
- redriven message (std-06-recv-src, fifo-06-recv-src): new MessageId, ReceiveCount 1, new SentTimestamp, DeadLetterQueueSourceArn gone, attributes kept, FIFO group kept, new SequenceNumber: matches (movetasks.go:341-357).
- redriven FIFO MessageDeduplicationId: differs: AWS carries the DLQ message's dedup id (= original MessageId, e.g. 5e72fd4c-...); understudy sets it to the new MessageId (movetasks.go:355).
- 07 start on never-DLQ (07-start-never-dlq): matches text "Source queue must be configured as a Dead Letter Queue."; `__type` differs: AWS `com.amazon.coral.service#InvalidParameterValueException`, understudy `com.amazonaws.sqs#InvalidParameterValueException` (codegen/overlays/sqs.json:54-63). Same namespace difference for every InvalidParameterValue below.
- 07 start on deleted queue (07-start-deleted): matches (404 `com.amazonaws.sqs#ResourceNotFoundException` "The resource that you specified for the SourceArn parameter doesn't exist.").
- 07 start second while RUNNING (07-start-second): matches.
- 07 rate 501 (07-start-rate-501): differs: AWS "Value for parameter MaxNumberOfMessagesPerSecond is invalid. Reason: You must enter a number that's between 1 and 500."; understudy "Value 501 for parameter MaxNumberOfMessagesPerSecond is invalid. Reason: Must be between 1 and 500, if provided." (internal/service/sqs/errors.go:183-184).
- 07 destination missing (07-start-dest-missing): matches (404, DestinationArn text).
- 07 cancel COMPLETED (std-07-cancel-completed, fifo-07-cancel-completed): differs: AWS 400 InvalidParameterValue "Only active tasks can be cancelled."; understudy 404 ResourceNotFoundException "Task does not exist." (movetasks.go:182-185).
- 07 cancel RUNNING (07-cancel-running): matches response (`{"ApproximateNumberOfMessagesMoved":0}`); status after differs: AWS lists CANCELLING 3 s later (07-list-after-cancel); understudy goes straight to CANCELLED (movetasks.go:188, state.go:366).
- 07 RedrivePolicy maxReceiveCount 0 / 1001 (07-redrive-count-*): matches text (raw policy echoed).
- 07 RedrivePolicy missing DLQ / invalid ARN (07-redrive-missing-dlq, 07-redrive-invalid-arn): reasons match ("Dead letter target does not exist.", "Invalid value for deadLetterTargetArn."); echoed value differs: AWS echoes the policy as sent (`"maxReceiveCount":"2"`), understudy echoes its canonical form (`"maxReceiveCount":2`) (attributes.go:284, 464).
- 07 FIFO source to standard DLQ (07-redrive-fifo-to-std): differs: AWS reason "Dead-letter queue must be same type of queue as the source.." (sic, two periods); understudy "Dead-letter queue must be the same type of queue as the source queue." (attributes.go:473), plus the echo difference.
- 07 RedriveAllowPolicy denyAll (07-redrive-denied): differs: AWS "Value RedrivePolicy(maxReceiveCount=2, deadLetterTargetArn=<arn>) for parameter RedrivePolicy is invalid. Reason: The value for the dead-letter queue's RedriveAllowPolicy attribute prevents you from completing this request."; understudy "Value <json> ... Reason: Queue <dlq> does not allow <src> to use it as a dead-letter queue." (attributes.go:477).
- hypothesis "redrive via move task gives new IDs": confirmed.

