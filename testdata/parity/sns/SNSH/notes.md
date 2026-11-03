# SNSH notes

- Date: 2026-11-03 (16:37-17:19 UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/sns-http-delivery.md section 9. Session changes: helper files (snsh.py, snsh.zip, conf.json, note.json, note-v2.json, unsub.json, cert files) live in `$REC/_work/SNSH`, not in the item directory (distill-parity refuses `.json`/`.py`/`.zip` there); each batch in its own subshell; `D-tokens.txt` added (only "TOK1 = TOK2" or "TOK1 != TOK2"); 9.12 deletes topics and SNS log groups by exact name, not by prefix; batch I got an instance guard, an EXIT trap that terminates the instance, and 5/10-minute bounds on its wait loops; `>/dev/null` on calls that print ids.
- Caveats that affect several readings:
  - The account's Lambda concurrency limit is 10 (`get-account-settings`), shared with concurrent recordings. Batch B's 16 simultaneous deliveries were partly throttled by the function URL (a Lambda 429): s201, s204, s404, s410, s500 and s502 have no logged first attempt, and their first logged attempt comes 6 s (the 5 s retry delay) after the others. Batch C's m-c-1 first gaps are inflated the same way; m-c-2 is clean.
  - `*-receiver` steps are paginated `filter-log-events`: the `.out` holds every event, the `.http` ends on the last (empty) page.
  - `snshdrop` prints "Invalid parameter: SubscriptionArn Reason: An ARN must have at least 6 elements, not 1" for subscriptions that `list-subscriptions-by-topic` reports as `Deleted` (after an unsubscribe link); it skips only `PendingConfirmation`. Harmless: the topic delete follows.

## SNSH-01 (4.1, 3.1, 3.2, 4.5)

- HTTP body layout: matches 4.1/R16 (`"Key" : value`, two-space indent, member order Type, MessageId, TopicArn, Subject, Message, Timestamp, SignatureVersion, Signature, SigningCertURL, UnsubscribeURL, MessageAttributes; attributes one per line, compact, order a, b, s, n). `\n`, `\"`, `\\`, `\t` escaped; `&`, `<`, `>`, `/` and `é` literal
- query string passed through: matches (`/auto/q` received `x=1&y=2` on every request)
- headers: matches 3.2 (`user-agent: Amazon Simple Notification Service Agent`, `accept-encoding: gzip,deflate`, `x-amz-sns-subscription-arn` absent on SubscriptionConfirmation, present on Notification and UnsubscribeConfirmation; `x-amz-sns-rawdelivery: true` on raw notifications only)
- `/auto/raw` SubscriptionConfirmation (subscription created raw): JSON envelope, `text/plain; charset=UTF-8`, no `x-amz-sns-rawdelivery` (matches the inference in 4.5)
- `/auto/raw` UnsubscribeConfirmation: JSON envelope, no rawdelivery header
- unsubscribe links (signed-less GET of UnsubscribeURL, and the built raw form): 200 `UnsubscribeResponse`, followed by an UnsubscribeConfirmation POST (matches 2.4)
- SubscribeURL GET from the receiver: 200 `text/xml`

## SNSH-02 (5.2)

- `A-verify.txt` lines 1-4 `Verified OK`, lines 5-6 `Verification failure`: matches (line 3 version-2 Notification, line 4 version-2 UnsubscribeConfirmation, SHA256withRSA)

## SNSH-03 (sns.http.control-content-type)

- differs: `headerContentType` also applies to confirmations: `/auto/json` SubscriptionConfirmation and UnsubscribeConfirmation `content-type: application/json` (topic `defaultRequestPolicy`), `/auto/plain` both `content-type: text/plain` (subscription `requestPolicy`, no charset added). Switch default `text-plain` is wrong; AWS = applies to all three types

## SNSH-04 (4.2)

- matches: `/auto/a` Message `for-https`; raw subscription body `for-https`

## SNSH-05 (sns.http.status-classes)

- 2xx (201, 204, 299): matches success (one attempt each, not counting a Lambda throttle)
- 3xx (301, 302, 307): differs from `retries-doc` (permanent failure): treated as delivered, one attempt, no DLQ. SNS `NumberOfNotificationsDelivered` for the topic = 11 at 16:40 = 3x2xx + 3x3xx + 4x4xx + sleep
- 4xx other than 429 (400, 403, 404, 410): differs: treated as delivered (no retry, no DLQ), i.e. the endpoint-preparation page's "outside 200-4xx" reading, not the retries page
- 429 and 5xx: retried (3 attempts with numRetries 2, 5 s apart): matches
- DLQ after exhausted retries: differs/unclear: only 2 messages reached the DLQ (subscriptions /auto/s502 and /auto/s503; SQS `NumberOfMessagesSent` 2, SNS `NumberOfNotificationsRedrivenToDlq` 2, `...FailedToRedriveToDlq` 0). /auto/s500, /auto/s429 and /auto/raw/s500 also exhausted 3 attempts and were not redriven, and no later attempt appeared in the receiver log. Not explained; worth a re-record with fewer concurrent subscriptions
- `/auto/sleep` (200 after 20 s): differs from the 15 s timeout (3.1): one attempt, counted delivered, no retry. AWS waited at least 20 s
- `NumberOfNotificationsFailed` 19 at 16:40 = 15 retryable attempts + 4 throttled first attempts: every HTTP attempt counts (matches 6.6)

## SNSH-06 (sns.http.redirects)

- matches `not-followed`: no request reached `/auto/redirected`

## SNSH-07 (sns.http.retry-body)

- matches `identical`: the three `/auto/s503` attempts carry byte-identical bodies (same MessageId, Timestamp, Signature)

## SNSH-08 (sns.http.dlq-body)

- enveloped: matches `as-sqs`: the DLQ body is the HTTP notification envelope byte for byte (only UnsubscribeURL differs between the two subscriptions' copies); SQS message attributes none (`MessageAttributes` stays inside the envelope); SenderId `AROA...:AWS-CLOUDCAST`
- raw: n/a (the raw subscription's message was not redriven, see SNSH-05)

## SNSH-09 (sns.http.backoff-curves), from m-c-2 (gaps between attempts, s; each includes about 1 s over the target)

- attempt count: matches 1 + numRetries (9 for numRetries 8, 10 for 9, 2 for 1)
- linear 2..64: 3.1, 11.1, 20.1, 29.1, 38.1, 47.1, 56.1, 64.1: matches even steps from min to max
- arithmetic: 3.1, 5.1, 9.1, 16.1, 25.1, 36.1, 49.1, 65.1: differs from `linear-for-all` (close to squares 2^2..8^2)
- geometric: 3.1, 4.1, 7.1, 13, 21.1, 33.1, 47.1, 65.1: differs from `linear-for-all`
- exponential: 3.1, 3.1, 5.1, 7.1, 12.1, 21.1, 36.1, 64.1: differs from `linear-for-all`
- single backoff retry (min 5, max 40): 6.1 (m-c-2), i.e. `minDelayTarget`: matches the switch's one-retry rule
- phases: 10 attempts, gaps 0.1, 0.1, 4.1, 4.1, 4.1, 17.1, 31.1, 31, 31.1: matches the expected "0, 0, 3, 3, three backoff 3..30, then 30, 30"
- the delays are about target + 1 s in every phase except no-delay

## SNSH-10 (sns.http.retry-jitter)

- matches `none`: m-c-1 and m-c-2 gaps agree within 0.1 s from the second retry on (first gaps of m-c-1 are throttle/cold-start inflated)

## SNSH-11 (sns.http.pending-messages)

- matches `dropped`: `m-d-before-confirm` never reached `/hold/a`

## SNSH-12 (sns.http.resubscribe-pending)

- differs from `resend-same-token`: the second Subscribe sent a second SubscriptionConfirmation with a different token (2 confirmations, 2 distinct tokens, each 224 hex characters, not the doc example's 288); same subscription ARN returned
- `D-confirm-first` (TOK1): 200, confirms; `D-confirm-second` (TOK2 after confirmation): 403 `AuthorizationError` "Subscription already confirmed"

## SNSH-13 (2.3)

- `--authenticate-on-unsubscribe true` signed: `ConfirmationWasAuthenticated` `true`: matches
- `--authenticate-on-unsubscribe yes`: accepted (200), `ConfirmationWasAuthenticated` `false`: a value other than `true` counts as false, no error

## SNSH-14 (errUnsubscribeNeedsCredentials)

- differs: unsigned GET of UnsubscribeURL on an authenticated subscription: HTTP 403 `AuthorizationError` (Type Sender) "This subscription may only be deleted by an authorized user." (understudy infers `InvalidParameter`)

## SNSH-15 (sns.http.unsubscribe-confirmation)

- matches `unauthenticated-only`: no UnsubscribeConfirmation reached `/hold/a` after the signed `D-unsubscribe-signed`

## SNSH-16 (2.4)

- restore link: 200 `ConfirmSubscriptionResponse` with the same subscription ARN as before; `D-attrs-restored` `PendingConfirmation` false, `ConfirmationWasAuthenticated` false; `D-list` shows it with its ARN

## SNSH-17 (sns.http.control-dlq)

- confirmation retries: 4 POSTs to `/cfail/x` about 21 s apart (the default policy, 3 retries at 20 s; the subscription had no DeliveryPolicy)
- matches `never`: `D-dlq` empty

## SNSH-18 (6.7, 3.3, 8.5)

- malformed JSON (subscription level): matches the recorded topic-level text "Invalid parameter: DeliveryPolicy: Invalid JSON: Unexpected character ('n' (code 110)) at [line: 1, column: 2]: was expecting double-quote to start field name"
- unknown member (subscription): "Invalid parameter: DeliveryPolicy: Unexpected JSON member: nope"
- a `healthyRetryPolicy` must carry `numRetries`, then `minDelayTarget` (checked in that order): "Invalid parameter: DeliveryPolicy: healthyRetryPolicy.numRetries must be specified" (min-0, min-over-max, max-3601, backoff-upper) and "... healthyRetryPolicy.minDelayTarget must be specified" (retries-101, retries-minus-1, phases-over, partial). Not in the doc. So the bound texts for these inputs stay unrecorded: differs from the recipe's intent, the recipe's inputs are incomplete policies
- sicklyRetryPolicy: "Invalid parameter: DeliveryPolicy: sicklyRetryPolicy.minDelayTarget must be specified" (so `sicklyRetryPolicy` is parsed and validated like the healthy one; `guaranteed: true` not reached)
- total over 3600 s: rejected at write: "Invalid parameter: DeliveryPolicy: healthyRetryPolicy: Total policy retry time cannot be greater than 3600 seconds" (topic: "... http.defaultHealthyRetryPolicy: Total policy retry time ...")
- backoffFunction `cubic`: "Invalid parameter: DeliveryPolicy: Invalid JSON member value [healthyRetryPolicy.backoffFunction]: the value [cubic] is not a valid backoff function"
- numRetries as a string: "Invalid parameter: DeliveryPolicy: healthyRetryPolicy.numRetries type mismatch, expecting integer, received a string"
- maxReceivesPerSecond 0: "Invalid parameter: DeliveryPolicy: throttlePolicy.maxReceivesPerSecond must be &gt;= 1" (`>` XML-escaped)
- `text/csv` and `application/xml` with raw off: "Invalid parameter: DeliveryPolicy: requestPolicy: If RawMessageDelivery is disabled the Content-Type can only be set to application/json or text/plain." (settles the 3.3 conflict: `application/xml` refused)
- `application/json; charset=UTF-8`: accepted and stored verbatim
- topic `text/csv`: "Invalid parameter: DeliveryPolicy: http.defaultRequestPolicy: Content-Type set at the Topic DeliveryPolicy level can only be application/json or text/plain."
- all errors 400 `InvalidParameter`

## SNSH-19 (6.2)

- fresh HTTP subscription: no `DeliveryPolicy` key; `EffectiveDeliveryPolicy` has `requestPolicy` `{"headerContentType":"text/plain; charset=UTF-8"}` (matches the inference) and the system default healthy policy
- canonical read-back: `{"healthyRetryPolicy":null,"sicklyRetryPolicy":null,"throttlePolicy":null,"requestPolicy":{...},"guaranteed":false}`: matches
- partial `healthyRetryPolicy` fill-in: n/a (the partial policy was refused, SNSH-18)
- `disableSubscriptionOverrides: true`: n/a (the topic policy was refused: "Invalid parameter: DeliveryPolicy: http.defaultHealthyRetryPolicy.minDelayTarget must be specified"); `E-attrs-override` and `E-attrs-plain-under-override` therefore show no topic override

## SNSH-20 (6.4)

- differs from even pacing: all 10 messages reached `/auto/thr` within 0.3 s (gaps 0.01-0.16 s) with `maxReceivesPerSecond` 1. No throttling visible for a 10-message burst

## SNSH-21 (2.1)

- differs: loopback and private endpoints refused at Subscribe: `http://127.0.0.1/`, `http://localhost/`, `http://10.0.0.1/` -> 403 `AuthorizationError` "Not authorized to subscribe internal endpoints"; `http://[::1]/` and `http://example.invalid/` -> 400 `InvalidParameter` "Invalid parameter: Unreachable Endpoint"

## SNSH-22 (sns.http.basic-auth)

- differs from `preemptive`: the confirmation to `/auth/auto` arrived without an `authorization` header; after the receiver's 401 with `WWW-Authenticate: Basic`, no further request arrived (subscription stayed pending, `m-g` never delivered). Caveat: not excluded that the Lambda function URL strips `authorization`
- `G-attrs-auth` `Endpoint`: password returned verbatim (`https://snsuser:<password>@...`)
- `G-list` `Endpoint`: masked as `https://snsuser:****@...`

## SNSH-23 (6.6)

- log groups: `sns/us-east-2/<account>/us1103-snsh-h` (success) and `sns/us-east-2/<account>/us1103-snsh-h/Failure`
- record: `{"notification":{"messageMD5Sum","messageId","topicArn","timestamp":"2026-11-03 17:16:30.207"},"delivery":{"deliveryId","destination","providerResponse":"OK"|"Internal Server Error","dwellTimeMs","attempts"?,"statusCode"},"status":"SUCCESS"|"FAILURE"}`; confirmations are logged too, without `messageMD5Sum`; `attempts` appears on only one of the two failure records

## SNSH-24: n/a (free plan: `run-instances` for `t4g.nano` answers 400 `InvalidParameterCombination` "The specified instance type is not eligible for Free Tier."; no instance was created, the security group was deleted)

## SNSH-25 (6.7, 8.6)

- matches the substring, full text now recorded: 400 `InvalidParameter` "Invalid parameter: SubscriptionArn Reason: Cannot unsubscribe a subscription that is pending confirmation"

## Teardown

Topics, function, URL, roles, DLQ, receiver and SNS log groups deleted. Four pending subscription records of deleted topics (us1103-snsh-d, -e twice, -g) still list as `PendingConfirmation`; Unsubscribe refuses pending subscriptions (SNSH-25) and AWS removes them within 48 hours.
