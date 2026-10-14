# R01

- Date: 2026-10-14T06:30Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Understudy paths are under /home/austin/dev/local-aws-stack.

- step 2 re-receive (ApproximateReceiveCount 2, same MessageId, new handle; 02-recv1/02-recv2): matches.
- step 3 stale delete H1 while H2 current (03-delete-stale): differs: AWS 200 empty body AND the message is deleted (03-recv-after-stale-delete and 03-recv-after-6s both `{}`, 6 s after a 5 s timeout); understudy matches only the latest handle, so H1 is a silent no-op and the message reappears (internal/service/sqs/visibility.go:140, state.go:321).
- step 3 delete H2 after message gone (03-delete-h2): matches (200, empty body).
- step 4 delete current then stale on a gone message (04-delete-current, 04-delete-stale-gone): matches (200, empty body both).
- step 5 CMV stale H1 while H2 current (05-cmv-stale): differs: AWS 200 empty body; understudy InvalidParameterValue "...Message does not exist or is not available for visibility timeout change." (visibility.go:209).
- step 5 CMV current (05-cmv-current): matches.
- step 5 CMV after delete, current and stale handle (05-cmv-deleted, 05-cmv-stale-deleted): text, code and status match (400, `x-amzn-query-error: InvalidParameterValue;Sender`, "Value <H> for parameter ReceiptHandle is invalid. Reason: Message does not exist or is not available for visibility timeout change."); `__type` differs: AWS `com.amazon.coral.service#InvalidParameterValueException`, understudy `com.amazonaws.sqs#InvalidParameterValueException` (codegen/overlays/sqs.json:54-63).
- step 6 batch HTTP status (06-cmv-batch, 06-delete-batch): matches (200).
- step 6 batch stale entry: delete-batch matches (Successful); cmv-batch differs: AWS Successful, understudy Failed ReceiptHandleIsInvalid "Message does not exist or is not available for visibility timeout change" (visibility.go:180-183, errors.go:245).
- step 6 batch current entry: matches (Successful).
- step 6 batch garbage entry: matches (`ReceiptHandleIsInvalid`, SenderFault true, `The input receipt handle "garbage" is not a valid receipt handle.`).
- step 6 batch handle from another queue: differs: AWS Failed `ReceiptHandleIsInvalid` `The receipt handle "<H>" is not valid for this queue.` SenderFault true (both batches); understudy handles carry no queue identity, so delete-batch reports Successful and cmv-batch reports the not-available text (visibility.go:140, 209).
- single-call vs batch code: settled: single-call deleted-message CMV is InvalidParameterValue (05-cmv-deleted); batch uses ReceiptHandleIsInvalid for garbage/other-queue. Batch form for a deleted message not recorded.
- step 7 FIFO delete with expired, never re-received handle (07-fifo-delete-expired): matches (400 InvalidParameterValue "...Reason: The receipt handle has expired."), `__type` namespace differs as above.
- step 7 FIFO delete H1 after re-receive (07-fifo-delete-h1-after-rereceive): differs: AWS still 400 "The receipt handle has expired."; understudy finds no message for a non-latest handle and returns 200 (visibility.go:140).
- step 7 FIFO delete H2 (07-fifo-delete-h2) and recv3 `{}`: matches.
- step 8 handle form (08-handles.txt): differs: AWS 412 chars, `AQEB` + base64, no shared prefix beyond `AQEB` across receives; understudy `AQEB` + 32 chars (internal/ids/ids.go:54). Cosmetic.
- backlog decision: AWS deletes on any handle of an existing standard message ("any handle for an existing message deletes it"); CMV with a stale standard handle succeeds.

