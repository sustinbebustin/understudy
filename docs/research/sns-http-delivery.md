# SNS HTTP/S delivery: primary-source research

Scope: what Amazon SNS sends to `http` and `https` subscriptions and how it treats the answers. That covers the confirmation handshake, the three POST message types with their headers and bodies, the signatures and the AWS-published validators that check them, delivery policies, retries, dead-letter queues and delivery status logging, and the outbound-network rules an emulator needs to do this safely (the roadmap's SNS HTTP/S delivery, ADR 0015). This note extends `sns.md` and cites it rather than repeating it: subscriptions (`sns.md 3`), delivery formats (`sns.md 5.1`, `5.2`, `5.5`, `5.6`), retries and DLQs (`sns.md 7`) and the source conflicts (`sns.md 9`). Sources were retrieved 2026-10-25: the SNS Developer Guide and API Reference (Markdown versions of the pages), the vendored Smithy model, three AWS-published validators read at pinned commits, LocalStack's AWS-validated snapshots, Terraform AWS provider v6.68.0, and the repo's own R16 recordings.

## 1. Sources and labels

### 1.1 Sources

| Source | Version | What it fixes here |
|---|---|---|
| SNS Developer Guide: [HTTP/S subscriber](https://docs.aws.amazon.com/sns/latest/dg/sns-http-https-endpoint-as-subscriber.html), [headers](https://docs.aws.amazon.com/sns/latest/dg/http-header.html), [SubscriptionConfirmation format](https://docs.aws.amazon.com/sns/latest/dg/http-subscription-confirmation-json.html), [Notification format](https://docs.aws.amazon.com/sns/latest/dg/http-notification-json.html), [UnsubscribeConfirmation format](https://docs.aws.amazon.com/sns/latest/dg/http-unsubscribe-confirmation-json.html), [verifying signatures](https://docs.aws.amazon.com/sns/latest/dg/sns-verify-signature-of-message.html), [verification steps](https://docs.aws.amazon.com/sns/latest/dg/sns-verify-signature-of-message-verify-message-signature.html), [delivery retries](https://docs.aws.amazon.com/sns/latest/dg/sns-message-delivery-retries.html), [endpoint preparation](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.prepare.html), [confirming](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.confirm.html), [raw delivery](https://docs.aws.amazon.com/sns/latest/dg/sns-large-payload-raw-message-delivery.html), [DLQs](https://docs.aws.amazon.com/sns/latest/dg/sns-dead-letter-queues.html), [delivery status](https://docs.aws.amazon.com/sns/latest/dg/sns-topic-attributes.html), [status logging setup](https://docs.aws.amazon.com/sns/latest/dg/topics-attrib.html), [metrics](https://docs.aws.amazon.com/sns/latest/dg/sns-monitoring-using-cloudwatch.html), [deleting](https://docs.aws.amazon.com/sns/latest/dg/sns-delete-subscription-topic.html), [security best practices](https://docs.aws.amazon.com/sns/latest/dg/sns-security-best-practices.html) | retrieved 2026-10-25 | documented behavior |
| SNS API Reference: [Subscribe](https://docs.aws.amazon.com/sns/latest/api/API_Subscribe.html), [ConfirmSubscription](https://docs.aws.amazon.com/sns/latest/api/API_ConfirmSubscription.html), [Unsubscribe](https://docs.aws.amazon.com/sns/latest/api/API_Unsubscribe.html), [GetSubscriptionAttributes](https://docs.aws.amazon.com/sns/latest/api/API_GetSubscriptionAttributes.html) | retrieved 2026-10-25 | operation contracts |
| Smithy model `aws/api-models-aws` | `1cfdfb4bb9ea858e65623e66d73af04bec353201`, vendored at `third_party/api-models-aws/models/sns/service/2010-03-31/sns-2010-03-31.json` | shapes and error traits (1.3) |
| [aws/aws-js-sns-message-validator](https://github.com/aws/aws-js-sns-message-validator) (npm `sns-validator`) | `a6ba4d646dc60912653357660301f3b25f94d686` (0.3.5, 2022-08-19, the latest commit) | 5.3 |
| [aws/aws-php-sns-message-validator](https://github.com/aws/aws-php-sns-message-validator) | `dc6abc2dfe30115f39c5196c7543b9509f511bb8` (2026-09-03) | 5.4 |
| [aws/aws-sdk-ruby](https://github.com/aws/aws-sdk-ruby) `gems/aws-sdk-sns/lib/aws-sdk-sns/message_verifier.rb` | `b8b310a1081a5a667cea0b82cd35bf782aa6cfe3` (latest commit touching the file, 2026-10-23) | 5.5 |
| LocalStack test suite, archived | `localstack@8b9a79f` `tests/aws/services/sns/test_sns.py`, `test_sns.snapshot.json`, `test_sns.validation.json`; the fixture `localstack-core/localstack/testing/pytest/fixtures.py` | third-party-recorded wire shapes (only tests with a `last_validated_date` count) |
| Terraform AWS provider | hashicorp/terraform-provider-aws v6.68.0 `internal/service/sns/topic_subscription.go` | 8.6; the call sequence is in `tf-messaging.md 3.5` |
| R16 recordings | `testdata/parity/sqs/R16/*.json`, `testdata/parity/sns/R16/*.json` (aws-recorded 2026-10-14, us-east-2) | real envelopes, signatures, validation texts |
| Ballerina SNS connector integration test | ballerina-platform/module-ballerinax-aws.sns@51e6a11 `ballerina/tests/topics-tests.bal` (2023-11-02) | one DeliveryPolicy validation text |
| AWS SDK for Go v2 partitions data | aws/aws-sdk-go-v2@aae188a `internal/endpoints/awsrulesfn/partitions.json` | 7.3 |

### 1.2 Evidence labels

- **aws-doc**: stated by an AWS documentation page or the Smithy model.
- **aws-recorded**: in the repo's own recordings (`testdata/parity`).
- **third-party-recorded**: in a LocalStack snapshot whose test has a `last_validated_date` in `test_sns.validation.json`, or in another project's test that runs against real AWS. The receivers in these tests normalize header-name casing, so header names are recorded but their wire casing is not.
- **code**: read from a validator's or client's source at the commit in 1.1.
- **inferred**: a conclusion from the sources above, with the reasoning given.
- **UNVERIFIED**: no source fixes the behavior. Each one names the plausible options and a recording case `SNSH-NN` in section 9.

### 1.3 Smithy facts

- Service traits: `aws.protocols#awsQuery`, `aws.auth#sigv4` with `name: "sns"`, `smithy.api#xmlNamespace` `http://sns.amazonaws.com/doc/2010-03-31/`. `Subscribe`, `ConfirmSubscription` and `Unsubscribe` have no `endpoint` or `hostPrefix` trait (`com.amazonaws.sns#AmazonSimpleNotificationService`, `#Subscribe`, `#ConfirmSubscription`, `#Unsubscribe`).
- `com.amazonaws.sns#SubscribeInput`: `TopicArn` and `Protocol` required, `Endpoint` (`#Endpoint2`), `Attributes`, `ReturnSubscriptionArn` (boolean, default `false`). `com.amazonaws.sns#ConfirmSubscriptionInput`: `TopicArn` and `Token` required, `AuthenticateOnUnsubscribe` (`#authenticateOnUnsubscribe`). `#authenticateOnUnsubscribe`, `#token` and `#Endpoint2` are plain strings: no length, pattern or enum trait. `AuthenticateOnUnsubscribe` is a string, not a boolean.
- Errors and their `aws.protocols#awsQueryError` codes: `Subscribe` can return `AuthorizationError` (403), `FilterPolicyLimitExceeded` (403), `InternalError` (500), `InvalidParameter` (400), `InvalidSecurity` (403), `NotFound` (404), `ReplayLimitExceeded` (403) and `SubscriptionLimitExceeded` (403). `ConfirmSubscription` has the same list without `InvalidSecurity`. `Unsubscribe` has `AuthorizationError`, `InternalError`, `InvalidParameter`, `InvalidSecurity` and `NotFound`. The full shape-to-code table is `sns.md 1`.

## 2. Subscription and confirmation

### 2.1 Subscribe

- For `http` and `https` the endpoint is "the (public) endpoint ... a URL beginning with `http://`" (or `https://`) ([API_Subscribe](https://docs.aws.amazon.com/sns/latest/api/API_Subscribe.html)). A URL without a scheme fails `InvalidParameter` `Invalid parameter: Endpoint must match the specified protocol` (aws-recorded, `testdata/parity/sns/R16/05-http-endpoint-no-scheme.json`).
- "Amazon SNS does not currently support private HTTP(S) endpoints" ([HTTP/S subscriber](https://docs.aws.amazon.com/sns/latest/dg/sns-http-https-endpoint-as-subscriber.html)). `Subscribe` refuses them (SNSH-21, recorded 2026-11-03): `http://127.0.0.1/`, `http://localhost/` and `http://10.0.0.1/` answer 403 `AuthorizationError` "Not authorized to subscribe internal endpoints"; `http://[::1]/` and `http://example.invalid/` answer 400 `InvalidParameter` "Invalid parameter: Unreachable Endpoint". understudy delivers to the loopback and private endpoints its allow-list names, a deliberate deviation for a local emulator.
- A FIFO topic refuses them: `Invalid parameter: Invalid protocol type: http` (aws-recorded, `testdata/parity/sns/R16/04-subscribe-fifo-http.json`).
- An HTTP/S subscription always needs confirmation: "If the endpoint type is HTTP/S or email ... the endpoint owner must run the `ConfirmSubscription` action" ([API_Subscribe](https://docs.aws.amazon.com/sns/latest/api/API_Subscribe.html)). The response's `SubscriptionArn` is the string `pending confirmation`, or the real ARN when `ReturnSubscriptionArn=true` (third-party-recorded 2024-05-13 for `http://example.com`, localstack@8b9a79f tests/aws/services/sns/test_sns.snapshot.json:L4154-L4174, validated test_sns.validation.json:L1112-L1114).
- `Subscribe` may carry `DeliveryPolicy`, `FilterPolicy`, `FilterPolicyScope`, `RawMessageDelivery` and `RedrivePolicy` ([API_Subscribe](https://docs.aws.amazon.com/sns/latest/api/API_Subscribe.html)). Identity and idempotency of a repeated `Subscribe` are in `sns.md 3.1`.
- Credentials in the URL (`https://user:password@domain.com` or `https://user@domain.com`) select Basic or Digest authentication, and "HTTPS URLs are only retrievable from the Amazon SNS `GetSubscriptionAttributes` API action, for principals to which you have granted API access" ([HTTP/S subscriber](https://docs.aws.amazon.com/sns/latest/dg/sns-http-https-endpoint-as-subscriber.html)). Recorded (SNSH-22, 2026-11-03): `GetSubscriptionAttributes` returns the password verbatim, and `ListSubscriptionsByTopic` masks it as `****` (`https://snsuser:****@...`). `ListSubscriptions` was not recorded; it is inferred to mask the same way.

### 2.2 The confirmation POST and the pending period

- SNS POSTs a `SubscriptionConfirmation` "when you create the subscription. Amazon SNS sends this request at that time" ([endpoint preparation](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.prepare.html)). Format: 3.2 and 4.3.
- The confirmation is retried like any message: "If Amazon SNS doesn't receive a successful response from your endpoint, it attempts to deliver the message again. This applies to all messages, including the subscription confirmation message" (same page). Recorded (SNSH-17, 2026-11-03): a subscription without a `DeliveryPolicy` had its confirmation POSTed 4 times about 21 s apart, the default policy's 3 retries at 20 s, and nothing reached its `RedrivePolicy` queue. Whether a `DeliveryPolicy` passed to `Subscribe` governs it is not recorded.
- "Amazon SNS doesn't send notifications to this endpoint until the subscription is confirmed" ([SubscriptionConfirmation format](https://docs.aws.amazon.com/sns/latest/dg/http-subscription-confirmation-json.html)). Messages published while pending are dropped: `m-d-before-confirm` never reached the endpoint after confirmation (SNSH-11, recorded 2026-11-03).
- While pending, `GetSubscriptionAttributes` works and shows `PendingConfirmation` `"true"` and `ConfirmationWasAuthenticated` `"false"`; the subscription's `DeliveryPolicy` and `RawMessageDelivery` can be set (third-party-recorded 2024-10-03, test_sns.snapshot.json:L4593-L4627 and L4745-L4790). In the same recordings the topic's `SubscriptionsPending` still read `"0"` after the pending subscription existed (L4530-L4592), so AWS's counters lag; do not assert on them.
- Listings show a pending subscription's ARN as `PendingConfirmation` (third-party-recorded 2026-01-30 for an email subscription, test_sns.py:L866-L878, validation L602-L603; also the console description in [confirming](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.confirm.html)). This settles the UNVERIFIED in `sns.md 3.4` as third-party-recorded.
- Lifetime: "Confirmation tokens are valid for two days" ([API_Subscribe](https://docs.aws.amazon.com/sns/latest/api/API_Subscribe.html)). "Amazon SNS automatically deletes unconfirmed subscriptions after 48 hours" ([deleting](https://docs.aws.amazon.com/sns/latest/dg/sns-delete-subscription-topic.html)). No source says three days.
- A second `Subscribe` of the same endpoint while the first is pending returns the same subscription ARN and sends a second `SubscriptionConfirmation` with a **new** token; the first token still confirms. Each token was 224 hex characters, not the doc example's 288. A token used after confirmation answers 403 `AuthorizationError` "Subscription already confirmed" (SNSH-12, recorded 2026-11-03).

### 2.3 Confirming

- Two ways: an HTTP GET of `SubscribeURL`, which needs no credentials, or `ConfirmSubscription` with the `Token` ([SubscriptionConfirmation format](https://docs.aws.amazon.com/sns/latest/dg/http-subscription-confirmation-json.html), [confirming](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.confirm.html)). The GET returns a `ConfirmSubscriptionResponse` XML document (namespace `http://sns.amazonaws.com/doc/2010-03-31/`) with the subscription ARN in `ConfirmSubscriptionResult` ([endpoint preparation](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.prepare.html)).
- Recorded GETs of `SubscribeURL` variants (third-party-recorded 2023-10-11, test_sns.snapshot.json:L3706-L3757, test_sns.py:L5258-L5306): `TopicArn=not-an-arn` gives `InvalidParameter` `Invalid parameter: Topic`, `Token=abc123` gives `Invalid parameter: Token`, the topic ARN rewritten to another Region gives `Invalid parameter: Topic`, and a nonexistent topic name in the right Region with the right token confirms the subscription (200). A second GET of the real `SubscribeURL` after that returns 200 with the same ARN. Confirmation is therefore idempotent, which settles the UNVERIFIED in `sns.md 3.2` as third-party-recorded. The token-format errors over the API are in `sns.md 3.2`.
- Token form: the [ConfirmSubscription](https://docs.aws.amazon.com/sns/latest/api/API_ConfirmSubscription.html) example token is 288 lower-case hex characters (counted). The docs state no length, so this is a documented example, not a stated rule. It settles the token length `sns.md 3.2` and `internal/service/sns/subscriptions.go` mark UNVERIFIED as aws-doc.
- `AuthenticateOnUnsubscribe`: "If the value of this parameter is `true` and the request has an AWS signature, then only the topic owner and the subscription owner can unsubscribe the endpoint. The unsubscribe action requires AWS authentication." `ConfirmSubscription` "requires an AWS signature only when the `AuthenticateOnUnsubscribe` flag is set to "true"" ([ConfirmSubscription](https://docs.aws.amazon.com/sns/latest/api/API_ConfirmSubscription.html)). AWS recommends setting it ([security best practices](https://docs.aws.amazon.com/sns/latest/dg/sns-security-best-practices.html)). A GET of `SubscribeURL` carries no signature, so it can never set the flag (inferred).
- `ConfirmationWasAuthenticated` is "`true` if the subscription confirmation request was authenticated" ([GetSubscriptionAttributes](https://docs.aws.amazon.com/sns/latest/api/API_GetSubscriptionAttributes.html)). Recorded (SNSH-13, 2026-11-03): a signed `ConfirmSubscription` with `AuthenticateOnUnsubscribe true` reads back `"true"`; with `yes` it is accepted (200) and reads back `"false"`, so a value other than `true` counts as false, with no error.

### 2.4 Unsubscribing and the UnsubscribeConfirmation

- "You can't delete a subscription that's pending confirmation" ([deleting](https://docs.aws.amazon.com/sns/latest/dg/sns-delete-subscription-topic.html)). The error text Terraform swallows is in `sns.md 3.2`.
- "If the subscription requires authentication for deletion, only the owner of the subscription or the topic's owner can unsubscribe, and an AWS signature is required. If the `Unsubscribe` call does not require authentication and the requester is not the subscription owner, a final cancellation message is delivered to the endpoint" ([Unsubscribe](https://docs.aws.amazon.com/sns/latest/api/API_Unsubscribe.html)).
- Recorded: an unsigned GET of a notification's `UnsubscribeURL` returns 200 `UnsubscribeResponse`, and the endpoint then receives an `UnsubscribeConfirmation` POST (third-party-recorded 2023-10-11, test_sns.snapshot.json:L3771-L3790; 2025-01-24 over a Lambda function URL, L4946-L4970, validation L1127-L1129).
- An unsigned GET of `UnsubscribeURL` for a subscription confirmed with `AuthenticateOnUnsubscribe=true` answers HTTP 403 `AuthorizationError` (Type `Sender`) "This subscription may only be deleted by an authorized user." (SNSH-14, recorded 2026-11-03).
- A signed `Unsubscribe` by the subscription owner sends no `UnsubscribeConfirmation` (SNSH-15, recorded 2026-11-03), as the API text implied.
- The `UnsubscribeConfirmation` carries a new token (recorded `<token:2>`, unlike the confirmation's `<token:1>`, L3779-L3790) and a `SubscribeURL` that "you must visit to re-confirm the subscription" ([UnsubscribeConfirmation format](https://docs.aws.amazon.com/sns/latest/dg/http-unsubscribe-confirmation-json.html)). Visiting it restores the subscription under the **same** ARN: 200 `ConfirmSubscriptionResponse` with that ARN, `PendingConfirmation` `false` and `ConfirmationWasAuthenticated` `false` afterwards, and listed again (SNSH-16, recorded 2026-11-03). How long that token lasts, and what the old ARN answers in between, are not recorded.

## 3. The request on the wire

### 3.1 Method, URL and connection

- `POST` to the endpoint URL over HTTP/1.1. The doc samples show `POST / HTTP/1.1` with `Host`, `Content-Length` and `Connection: Keep-Alive` ([HTTP/S subscriber](https://docs.aws.amazon.com/sns/latest/dg/sns-http-https-endpoint-as-subscriber.html), [headers](https://docs.aws.amazon.com/sns/latest/dg/http-header.html)). The endpoint's path and query string are sent unchanged: `/auto/q` received `x=1&y=2` on every request (SNSH-01, recorded 2026-11-03).
- HTTPS: SNI is supported, and "Amazon SNS will only send messages to HTTPS endpoints that have a server certificate signed by a CA trusted by Amazon SNS" ([HTTP/S subscriber](https://docs.aws.amazon.com/sns/latest/dg/sns-http-https-endpoint-as-subscriber.html), [endpoint preparation](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.prepare.html)).
- Authentication: Basic and Digest from the URL's user information, and "the client service must be able to support the `HTTP/1.1 401 Unauthorized` header response" ([HTTP/S subscriber](https://docs.aws.amazon.com/sns/latest/dg/sns-http-https-endpoint-as-subscriber.html)). SNSH-22 (recorded 2026-11-03): the confirmation reached the endpoint without an `authorization` header, and after the receiver's 401 with `WWW-Authenticate: Basic` no further request arrived, so the subscription stayed pending. It is not excluded that the Lambda function URL receiving it strips `authorization`, so whether SNS sends Basic credentials, first or after a challenge, stays UNVERIFIED; a re-record against a receiver that keeps the header settles it.
- Timeout: "The connection times out in about 15 seconds. If your endpoint does not respond in time, Amazon SNS considers the delivery a failed attempt" ([endpoint preparation](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.prepare.html)). SNSH-05 (recorded 2026-11-03) contradicts it: an endpoint that answered 200 after 20 s got one attempt, counted delivered, so AWS waited at least 20 s.
- Redirects are not followed: no request reached the `Location` of a 301, 302 or 307 (SNSH-06, recorded 2026-11-03). 6.1 gives the classification.

### 3.2 Headers

| Header | SubscriptionConfirmation | Notification | UnsubscribeConfirmation |
|---|---|---|---|
| `x-amz-sns-message-type` | `SubscriptionConfirmation` | `Notification` | `UnsubscribeConfirmation` |
| `x-amz-sns-message-id` | the body's `MessageId` | same | same |
| `x-amz-sns-topic-arn` | topic ARN | topic ARN | topic ARN |
| `x-amz-sns-subscription-arn` | absent | subscription ARN | subscription ARN |
| `x-amz-sns-rawdelivery` | absent | `true` only for raw delivery | absent |
| `Content-Type` | `text/plain; charset=UTF-8`, or the `headerContentType` (3.3) | same | same |
| `User-Agent` | `Amazon Simple Notification Service Agent` | same | same |
| `Accept-Encoding` | `gzip,deflate` | same | same |
| `Content-Length`, `Host`, `Connection: Keep-Alive` | present | present | present |

Sources: header list and samples ([headers](https://docs.aws.amazon.com/sns/latest/dg/http-header.html), [SubscriptionConfirmation format](https://docs.aws.amazon.com/sns/latest/dg/http-subscription-confirmation-json.html), [Notification format](https://docs.aws.amazon.com/sns/latest/dg/http-notification-json.html), [UnsubscribeConfirmation format](https://docs.aws.amazon.com/sns/latest/dg/http-unsubscribe-confirmation-json.html)). The raw-delivery header is documented in [raw delivery](https://docs.aws.amazon.com/sns/latest/dg/sns-large-payload-raw-message-delivery.html) and recorded at test_sns.snapshot.json:L3767 and L4668. Third-party recordings: the confirmation without `x-amz-sns-subscription-arn` (2023-10-11 L3695-L3705, 2024-10-03 L4648-L4658, 2025-01-24 L4902-L4909), the notification with it (L3882-L3893, L4832-L4843, L4934-L4941), and the `UnsubscribeConfirmation` with it (2025-01-24 L4961-L4968). `Accept-Encoding: gzip,deflate` appears in every recorded request and in no doc sample. The `UnsubscribeConfirmation`'s default `Content-Type` is recorded (L4963). For both confirmation types the lack of `x-amz-sns-rawdelivery` is inferred, since raw delivery applies to notifications (4.5). The raw-on recordings enable `RawMessageDelivery` only after `Subscribe` (localstack-core/localstack/testing/pytest/fixtures.py:L645-L672), so their confirmations were most likely sent with raw off, and the one `UnsubscribeConfirmation` recorded with raw on (L3779-L3790) kept no headers. SNSH-01 records both types for a subscription created with raw on (`/auto/raw`).

- Header-name casing: the docs print the `x-amz-sns-*` names in lower case and the standard headers capitalized. The recording receivers normalized casing, and HTTP header names are case-insensitive. Send them as the docs print them (inferred).
- Raw delivery sends "the message body, client IP, and the required headers"; message attributes are not sent ([raw delivery](https://docs.aws.amazon.com/sns/latest/dg/sns-large-payload-raw-message-delivery.html)).

### 3.3 Content-Type

- Default `text/plain; charset=UTF-8` ([headers](https://docs.aws.amazon.com/sns/latest/dg/http-header.html)). A request policy overrides it: `requestPolicy.headerContentType` on a subscription or `defaultRequestPolicy.headerContentType` on a topic ([delivery retries](https://docs.aws.amazon.com/sns/latest/dg/sns-message-delivery-retries.html#creating-delivery-policy)).
- Recorded values are sent verbatim, with no charset added: a topic `application/json` gave a non-raw notification `Content-Type: application/json` (third-party-recorded 2024-10-03, test_sns.snapshot.json:L4836), and a raw subscription's `text/csv` gave `Content-Type: text/csv` (L4663).
- Allowed values: with raw delivery off, or at topic level, `application/json` and `text/plain`; with raw on, also `text/css`, `text/csv`, `text/html`, `text/xml`, `application/atom+xml`, `application/octet-stream`, `application/soap+xml`, `application/x-www-form-urlencoded`, `application/xhtml+xml` and `application/xml` ([delivery retries](https://docs.aws.amazon.com/sns/latest/dg/sns-message-delivery-retries.html#creating-delivery-policy)). The JSON-format pages list `text/plain | application/json | application/xml` for both levels ([subscription format](https://docs.aws.amazon.com/sns/latest/dg/set-sub-attributes-delivery-policy-json.html), [topic format](https://docs.aws.amazon.com/sns/latest/dg/set-topic-attributes-delivery-policy-json.html)), a conflict over `application/xml` with raw off. SNSH-18 (recorded 2026-11-03) settles it for the delivery-retries page: with raw off, `text/csv` and `application/xml` are refused with "Invalid parameter: DeliveryPolicy: requestPolicy: If RawMessageDelivery is disabled the Content-Type can only be set to application/json or text/plain."; a topic's `text/csv` with "Invalid parameter: DeliveryPolicy: http.defaultRequestPolicy: Content-Type set at the Topic DeliveryPolicy level can only be application/json or text/plain."; `application/json; charset=UTF-8` is accepted and stored verbatim.
- `headerContentType` applies to both confirmation types too (SNSH-03, recorded 2026-11-03): a topic's `application/json` gave `/auto/json`'s `SubscriptionConfirmation` and `UnsubscribeConfirmation` `content-type: application/json`, and a subscription's `text/plain` (set at `Subscribe`) gave `/auto/plain`'s both `content-type: text/plain`, no charset added. The earlier third-party recordings set the policy after `Subscribe`, so they did not decide it.

## 4. Bodies

### 4.1 Layout

- The SQS envelope's exact bytes are aws-recorded (`testdata/parity/sqs/R16/01-sqs-enveloped.json`, `01-sqs-enveloped-2.json`). Members are written as `"Key" : value`, one per line, with a two-space indent, an opening `{` line and a closing `}` line. `MessageAttributes` holds one attribute per line, each compact: `"a" : {"Type":"String.Array","Value":"[\"x\",1]"}`. Attribute order is Java `HashMap` order (`testdata/parity/sns/R16/notes.md`).
- The doc samples of all three HTTP message types use the same `"Key" : "value"` layout ([HTTP/S subscriber](https://docs.aws.amazon.com/sns/latest/dg/sns-http-https-endpoint-as-subscriber.html), [endpoint preparation](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.prepare.html)). The third-party recordings parse the bodies, so their bytes are not recorded. SNSH-01 (recorded 2026-11-03) shows the HTTP bodies byte-identical in layout to the SQS envelope: `"Key" : value`, two-space indent, the member order below, attributes one per line, compact, in the order a, b, s, n; `\n`, `\"`, `\\` and `\t` escaped, and `&`, `<`, `>`, `/` and `é` literal. The query string is passed through unchanged.
- Escaping: SNS converts control characters to JSON escapes, and verifiers must unescape before building the string to sign ([endpoint preparation](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.prepare.html)). The `&` in URLs stays literal (R16 bodies).

### 4.2 Notification

Member order: `Type`, `MessageId`, `TopicArn`, `Subject` (only when published with one), `Message`, `Timestamp`, `SignatureVersion`, `Signature`, `SigningCertURL`, `UnsubscribeURL`, `MessageAttributes` (only when the publish had attributes) ([Notification format](https://docs.aws.amazon.com/sns/latest/dg/http-notification-json.html); the order is the doc sample's and the R16 SQS envelope's). A recorded HTTP notification without a subject has no `Subject` member (test_sns.snapshot.json:L3871-L3881). Each attribute is `{"Type": <DataType>, "Value": <string>}` (`sns.md 5.1`). `MessageStructure=json` selects the per-protocol key, `http` or `https`, falling back to `default` (`sns.md 4.1`); an https subscription got its `https` value, raw and enveloped (SNSH-04, recorded 2026-11-03).

```
{
  "Type" : "Notification",
  "MessageId" : "22b80b92-fdea-4c2c-8f9d-bdfb0c7bf324",
  "TopicArn" : "arn:aws:sns:us-west-2:123456789012:MyTopic",
  "Subject" : "My First Message",
  "Message" : "Hello world!",
  "Timestamp" : "2012-05-02T00:54:06.655Z",
  "SignatureVersion" : "1",
  "Signature" : "EXAMPLEw6JRN...",
  "SigningCertURL" : "https://sns.us-west-2.amazonaws.com/SimpleNotificationService-f3ecfb7224c7233fe7bb5f59f96de52f.pem",
  "UnsubscribeURL" : "https://sns.us-west-2.amazonaws.com/?Action=Unsubscribe&SubscriptionArn=arn:aws:sns:us-west-2:123456789012:MyTopic:c9135db0-26c4-47ec-8998-413945fb5a96"
}
```

(Doc sample, [Notification format](https://docs.aws.amazon.com/sns/latest/dg/http-notification-json.html).)

### 4.3 SubscriptionConfirmation

Member order: `Type`, `MessageId`, `Token`, `TopicArn`, `Message`, `SubscribeURL`, `Timestamp`, `SignatureVersion`, `Signature`, `SigningCertURL` ([SubscriptionConfirmation format](https://docs.aws.amazon.com/sns/latest/dg/http-subscription-confirmation-json.html)). It has no `UnsubscribeURL`, `Subject` or `MessageAttributes`.

- `Message`: `You have chosen to subscribe to the topic <topic-arn>.\nTo confirm the subscription, visit the SubscribeURL included in this message.` (doc and third-party-recorded, test_sns.snapshot.json:L3683-L3694).
- `SubscribeURL`: `<service-url>/?Action=ConfirmSubscription&TopicArn=<topic-arn>&Token=<token>`, with the topic ARN and token not percent-encoded (doc; LocalStack's AWS-validated tests assert this exact form, test_sns.py:L5253-L5256 and L5700-L5703).
- The member set is third-party-recorded (L3683-L3694, L4890-L4901). The SQS variant that understudy already sends is `envelope.SQSConfirmation` (`internal/service/sns/envelope/envelope.go`).

### 4.4 UnsubscribeConfirmation

Member order as in 4.3 ([UnsubscribeConfirmation format](https://docs.aws.amazon.com/sns/latest/dg/http-unsubscribe-confirmation-json.html)). `Message`: `You have chosen to deactivate subscription <subscription-arn>.\nTo cancel this operation and restore the subscription, visit the SubscribeURL included in this message.` (doc and third-party-recorded, L3779-L3790, L4949-L4959). The token differs from the original confirmation's (2.4).

### 4.5 Raw delivery

With `RawMessageDelivery=true` the body is the message itself (after `MessageStructure` selection), headers as in 3.2 plus `x-amz-sns-rawdelivery: true`, and no attributes ([raw delivery](https://docs.aws.amazon.com/sns/latest/dg/sns-large-payload-raw-message-delivery.html); third-party-recorded body equality, test_sns.py:L5337-L5339). Raw applies to notifications only (inferred). With raw on, a recorded `UnsubscribeConfirmation` was still the JSON document (L3779-L3790, raw enabled at the time). For a subscription created with `RawMessageDelivery=true`, both confirmations are the JSON document with `text/plain; charset=UTF-8` and no `x-amz-sns-rawdelivery` header (SNSH-01, recorded 2026-11-03).

### 4.6 Field values

- `MessageId`: a UUID; a retry reuses the original's ([headers](https://docs.aws.amazon.com/sns/latest/dg/http-header.html)). Real notification ids are version-5 UUIDs (aws-recorded, `testdata/parity/sns/R16/notes.md`).
- `Timestamp`: UTC, ISO 8601 with exactly three fractional digits and `Z` (`sns.md 5.1`; R16 `2026-10-14T07:06:00.824Z`). For a notification it is "the time (GMT) when the notification was published"; for a confirmation, "when the subscription confirmation was sent" (format pages). A retry resends the first attempt's bytes: three attempts carried the same `MessageId`, `Timestamp` and `Signature` (SNSH-07, recorded 2026-11-03).
- `SigningCertURL`: `https://sns.<region>.amazonaws.com/SimpleNotificationService-<32 hex>.pem`. R16 has `SimpleNotificationService-1e59c4574facfe41babdb2d652f8ebef.pem` in us-east-2 in 2026, and the docs' 2021 us-east-2 sample has `010a507c1833636cd94bdb98bd93083a` ([raw delivery](https://docs.aws.amazon.com/sns/latest/dg/sns-large-payload-raw-message-delivery.html)), so AWS rotates the certificate.
- `UnsubscribeURL`: `https://sns.<region>.amazonaws.com/?Action=Unsubscribe&SubscriptionArn=<subscription-arn>`, not percent-encoded (`sns.md 5.1`).

## 5. Signatures and the AWS validators

### 5.1 String to sign

- Each name and value goes on its own line, `name\nvalue\n`, in byte order of the names, ending with one trailing newline. A Notification signs `Message`, `MessageId`, `Subject` (only if present), `Timestamp`, `TopicArn`, `Type`. A SubscriptionConfirmation or UnsubscribeConfirmation signs `Message`, `MessageId`, `SubscribeURL`, `Timestamp`, `Token`, `TopicArn`, `Type` ([verification steps](https://docs.aws.amazon.com/sns/latest/dg/sns-verify-signature-of-message-verify-message-signature.html); `sns.md 5.6`).
- Conflict: the two confirmation-format pages say the signature covers only "the `Message`, `MessageId`, `Type`, `Timestamp`, and `TopicArn` values" ([SubscriptionConfirmation format](https://docs.aws.amazon.com/sns/latest/dg/http-subscription-confirmation-json.html), [UnsubscribeConfirmation format](https://docs.aws.amazon.com/sns/latest/dg/http-unsubscribe-confirmation-json.html)). The verification page, the PHP and Ruby validators (5.4, 5.5), and LocalStack's AWS-validated test all include `SubscribeURL` and `Token`. That test verifies real AWS signatures of a SubscriptionConfirmation, a Notification and an UnsubscribeConfirmation with exactly the lists above (third-party-recorded 2025-01-24, test_sns.py:L5605-L5640 and L5692-L5736, validation L1127-L1129). Follow the verification page.

### 5.2 Algorithms and key

- `SignatureVersion` `1` is Base64 `SHA1withRSA`; `2` is Base64 `SHA256withRSA` ([Notification format](https://docs.aws.amazon.com/sns/latest/dg/http-notification-json.html)). Both are RSA PKCS #1 v1.5 (LocalStack's AWS-validated verifier uses `padding.PKCS1v15()`, test_sns.py:L5641-L5646; every validator below calls a PKCS #1 v1.5 verify).
- Key size: R16's real signatures are 344 Base64 characters, which is 256 bytes, so the key is 2048-bit RSA (aws-recorded, `testdata/parity/sqs/R16/01-sqs-enveloped.json`). That settles the UNVERIFIED `keyBits` in `internal/service/sns/envelope/key.go`.
- A version-2 topic's HTTP Notification and UnsubscribeConfirmation verify as `SHA256withRSA` over the field lists of 5.1 (SNSH-02, recorded 2026-11-03). Version 2 is also recorded for SQS (`sns.md 5.1`); the Lambda event mislabels it (`sns.md 5.4`).

### 5.3 aws-js-sns-message-validator (npm `sns-validator`)

Code at aws/aws-js-sns-message-validator@a6ba4d6 `index.js`:

- Configuration: `new MessageValidator(hostPattern, encoding)`, and nothing else (L171-L174). The default `hostPattern` is `/^sns\.[a-zA-Z0-9\-]{3,}\.amazonaws\.com(\.cn)?$/` (L7).
- URL check: `url.parse(SigningCertURL)`; requires `protocol === 'https:'`, `path` ending in `.pem`, and `hostPattern.test(parsed.host)` (L92-L98). `host` includes the port, so a pattern for a local emulator must match `localhost:4567`, not `localhost`. Failure: `The certificate is located on an invalid domain.` (L209-L212).
- Certificate fetch: always `https.get`, cached per URL in a module-private object, `Certificate could not be retrieved` unless the status is 200 (L100-L122). It cannot be replaced, so a local certificate must be served over TLS the Node process trusts.
- Required keys: `Message`, `MessageId`, `Timestamp`, `TopicArn`, `Type`, `Signature`, `SigningCertURL`, `SignatureVersion`, plus `SubscribeURL` and `Token` for both confirmation types; otherwise `Message missing required keys.` (L14-L23, L82-L90).
- Fields signed: for `Type === 'SubscriptionConfirmation'`, `Message, MessageId, Subject, SubscribeURL, Timestamp, Token, TopicArn, Type`; for every other type, the same list without `Token`, each only if present (L24-L42, L132-L145). An `UnsubscribeConfirmation` is therefore checked without its `Token`, while AWS signs it with the token (5.1). Inferred: `sns-validator` 0.3.5 rejects every genuine `UnsubscribeConfirmation` with `The message signature is invalid.` (L156). The split came from [PR #4](https://github.com/aws/aws-js-sns-message-validator/pull/4), "Validation of SNS messages of type: Notification should not accept Token attribute".
- Versions `1` (`RSA-SHA1`) and `2` (`RSA-SHA256`); others give `The signature version <v> is not supported.` (L124-L139). Lambda-style `SigningCertUrl`/`UnsubscribeUrl` are renamed and a null `Subject` is dropped (L68-L80). No certificate chain or issuer check.

### 5.4 aws-php-sns-message-validator

Code at aws/aws-php-sns-message-validator@dc6abc2:

- Configuration: `new MessageValidator(?callable $certClient = null, $hostNamePattern = '')` (`src/MessageValidator.php` L66-L74). `certClient` is `function (string $certUrl): string|false` and defaults to `file_get_contents`; the default host pattern is the same as JS (L28-L29).
- URL check: scheme `https`, a host, the whole URL ending `.pem`, and `preg_match($hostPattern, parse_url($url)['host'])`, where `host` has no port; failure `The certificate is located on an invalid domain.` (L201-L214). Fetch failure: `Cannot get the certificate from "<url>".`; bad certificate: `Cannot get the public key from the certificate.`; bad signature: `The message signature is invalid.` (L97-L128).
- Fields signed: one list for every type, `Message, MessageId, Subject, SubscribeURL, Timestamp, Token, TopicArn, Type`, each when `isset` (L163-L191). Versions other than `1`/`2` give `The SignatureVersion "<v>" is not supported.` (L176-L181). `1` uses SHA-1, otherwise SHA-256 (L119-L123).
- `Message::fromRawPostData()` refuses a request without the `x-amz-sns-message-type` header: `SNS message type header not provided.` (`src/Message.php` L42-L51). Required keys: `Message`, `MessageId`, `Timestamp`, `TopicArn`, `Type`, `Signature`, `SigningCertURL`, `SignatureVersion`, plus `SubscribeURL` and `Token` for both confirmation types (L11-L25, L94-L98). No chain or issuer check.

### 5.5 aws-sdk-ruby `Aws::SNS::MessageVerifier`

Code at aws/aws-sdk-ruby@b8b310a `gems/aws-sdk-sns/lib/aws-sdk-sns/message_verifier.rb`:

- Configuration: only `http_proxy` (L43-L49). The host check uses the constant `AWS_HOSTNAMES = [/^sns\.[a-zA-Z0-9\-]{3,}\.amazonaws\.com(\.cn)?$/]` against `uri.host` (L38-L41, L181-L186) and cannot be configured: `signing cert is not hosted by AWS: <uri>`.
- Also required: `https` (`the SigningCertURL must be https, got: <uri>`), a path whose extension is `.pem`, a PEM matching `\A[\s]*-----BEGIN [A-Z]+-----\n[A-Za-z\d+\/\n]+[=]{0,2}\n-----END [A-Z]+-----[\s]*\Z`, and a fetch with `VERIFY_PEER`, attempted at most three times in all (`retry if failed_attempts < 3`; L168-L223).
- Fields signed: the same single list as PHP, skipping nil or empty values (L27-L36, L132-L141). Versions `1`/`2`, else `Invalid SignatureVersion` (L70-L78). No chain or issuer check.

### 5.6 What an emulator-signed message can pass

| Check | sns-validator 0.3.5 | PHP validator | Ruby verifier |
|---|---|---|---|
| `SigningCertURL` scheme | `https:` | `https` | `https` |
| `.pem` test | URL path (with query) | whole URL | path extension |
| Host test | configurable regex on host with port | configurable regex on host | fixed AWS pattern |
| Certificate fetch | `https.get`, fixed | configurable callable | `Net::HTTP` with `VERIFY_PEER`, fixed |
| Chain or issuer check | none | none | none |
| `UnsubscribeConfirmation` fields | without `Token` (fails real AWS messages) | with `Token` | with `Token` |

Inferred from the code above:

- A message passes an AWS validator only if its `SigningCertURL` is `https://<host>[:port]/...pem`, the validator's host pattern accepts that host, and the validator can fetch the certificate over TLS it trusts. A self-signed certificate is fine because none of the three checks the chain or the issuer. The AWS guidance to "check that the certificate is issued by Amazon SNS" ([verifying signatures](https://docs.aws.amazon.com/sns/latest/dg/sns-verify-signature-of-message.html)) is not implemented by any of them; a hand-written verifier that implements it cannot pass a non-AWS certificate.
- JS and PHP can pass with a configured host pattern. Ruby cannot without stubbing `verify_hosted_by_aws!` or replacing the constant.
- An emulator must never put an AWS host in `SigningCertURL` to pass the default pattern. The validator would then fetch AWS's real certificate, the emulator's signature would fail against it, and the receiver would have made a request to real AWS.

## 6. Outcomes, retries and dead letters

### 6.1 Response classes

- The retries page and the subscriber page: "Amazon SNS considers all 5XX errors and 429 (too many requests sent) errors as retryable. These errors are subject to the delivery policy. All other errors are considered as permanent failures and retries will not be attempted" ([delivery retries](https://docs.aws.amazon.com/sns/latest/dg/sns-message-delivery-retries.html#creating-delivery-policy), [HTTP/S subscriber](https://docs.aws.amazon.com/sns/latest/dg/sns-http-https-endpoint-as-subscriber.html)).
- The endpoint-preparation page: a response after about 15 s, or "a status code outside the range of 200–4xx", is a failed attempt ([endpoint preparation](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.prepare.html)). Read literally, 3xx and 4xx would then be successes. This is the conflict in `sns.md 9`.
- A timeout is a failed attempt and is retried (the endpoint-preparation note above: "retries about 35 seconds after the previous attempt"). For connection-level failures the sources conflict. The DLQ and retries pages call an endpoint that "becomes unavailable" a server-side error, which is retried ([DLQs](https://docs.aws.amazon.com/sns/latest/dg/sns-dead-letter-queues.html), [delivery retries](https://docs.aws.amazon.com/sns/latest/dg/sns-message-delivery-retries.html)). The `RedrivePolicy` description in [API_Subscribe](https://docs.aws.amazon.com/sns/latest/api/API_Subscribe.html) and [GetSubscriptionAttributes](https://docs.aws.amazon.com/sns/latest/api/API_GetSubscriptionAttributes.html) gives "when the subscribed endpoint is unreachable" as its example of a client error, and "Amazon SNS doesn't retry the message delivery that fails because of a client-side error" ([DLQs](https://docs.aws.amazon.com/sns/latest/dg/sns-dead-letter-queues.html)). UNVERIFIED: whether connection refusal, a connect timeout, a reset, a DNS failure and a TLS failure are retried, or are permanent failures that go straight to the DLQ. SNSH-24 was n/a on 2026-11-03 (its EC2 receiver needs an instance type the account's free plan refuses). DNS and TLS failures would need a receiver that is confirmed first and then broken, which no batch builds.
- Recorded (SNSH-05, 2026-11-03), which settles the conflict for the endpoint-preparation page's reading:
  - 2xx (201, 204, 299): one attempt, success;
  - 3xx (301, 302, 307) and 4xx other than 429 (400, 403, 404, 410): one attempt, counted delivered (`NumberOfNotificationsDelivered`), no retry, nothing to the DLQ;
  - 429 and 5xx: retried (3 attempts with `numRetries` 2, 5 s apart);
  - after exhausted retries only 2 of 5 subscriptions (`/auto/s502`, `/auto/s503`) reached the DLQ (`NumberOfNotificationsRedrivenToDlq` 2, `...FailedToRedriveToDlq` 0); `/auto/s500`, `/auto/s429` and `/auto/raw/s500` were not redriven. Not explained, and the Lambda receiver was partly throttled; a re-record with fewer concurrent subscriptions should settle which exhausted deliveries are redriven.
- The emulator's `sns.http.status-classes` keeps the retries page's reading (`retries-doc`), a recorded deviation until it is changed.
- Redirects are not followed (SNSH-06, recorded 2026-11-03); under the recorded classes the 3xx then counts as delivered. The emulator never follows one (7.4).
- Response bodies are not used by any documented behavior; read and discard them (inferred).

### 6.2 Delivery policy documents

- Shapes and constraints: `sns.md 3.3` and `7.3`. Topic-level replacement on write, `disableSubscriptionOverrides:false` injection and `EffectiveDeliveryPolicy` defaults: `sns.md 2.2`.
- A subscription's `DeliveryPolicy` reads back in a canonical re-serialized form. Setting `{"requestPolicy":{"headerContentType":"text/csv"}}` reads back as `{"healthyRetryPolicy":null,"sicklyRetryPolicy":null,"throttlePolicy":null,"requestPolicy":{"headerContentType":"text/csv"},"guaranteed":false}` (third-party-recorded 2024-10-03, test_sns.snapshot.json:L4593-L4604; input at test_sns.py:L5513-L5521). A full `healthyRetryPolicy` sent as `minDelayTarget, maxDelayTarget, numRetries, numNoDelayRetries, numMinDelayRetries, numMaxDelayRetries, backoffFunction` reads back reordered to `minDelayTarget, maxDelayTarget, numRetries, numMaxDelayRetries, numNoDelayRetries, numMinDelayRetries, backoffFunction` (L4745-L4763; input at localstack-core/localstack/testing/pytest/fixtures.py:L647-L660).
- Each write replaces the whole subscription policy. After the fixture's full policy, the `text/csv` write read back `healthyRetryPolicy: null` (L4596-L4597).
- No doc describes `sicklyRetryPolicy` or `guaranteed`. The fixture sends `sicklyRetryPolicy: null` and `guaranteed: false` and AWS accepts them (L4745-L4790), but the `text/csv` read-back shows the same two values without either being sent (L4598, L4603). So the recordings show only that the members are accepted as null and false. SNSH-18 (recorded 2026-11-03) shows a non-null `sicklyRetryPolicy` is parsed and validated like the healthy one ("Invalid parameter: DeliveryPolicy: sicklyRetryPolicy.minDelayTarget must be specified"); `guaranteed: true` was not reached, so whether it is accepted or applied stays UNVERIFIED.
- `EffectiveDeliveryPolicy` of a subscription, from the two recordings (third-party-recorded, L4605-L4621 and L4764-L4782). Each top-level member is the subscription's value when non-null, else the topic's `default*` value (a topic `application/json` filled `requestPolicy`), else, for `healthyRetryPolicy` only, the system default `{"minDelayTarget":20,"maxDelayTarget":20,"numRetries":3,"numMaxDelayRetries":0,"numNoDelayRetries":0,"numMinDelayRetries":0,"backoffFunction":"linear"}`. `sicklyRetryPolicy` and `throttlePolicy` stay `null` when unset, and `guaranteed` is `false`. Recorded (SNSH-19, 2026-11-03): a fresh HTTP subscription has no `DeliveryPolicy` key, and its `EffectiveDeliveryPolicy` has `requestPolicy` `{"headerContentType":"text/plain; charset=UTF-8"}` and the system default healthy policy; the canonical read-back form above matches. UNVERIFIED: whether a partial `healthyRetryPolicy` is filled in (AWS refused the partial policy, 6.7) and what `disableSubscriptionOverrides: true` changes (the topic policy carrying it was refused for a missing `minDelayTarget`).

### 6.3 Retry schedule

- Attempts: `numRetries` is "the total number of retries, including immediate, pre-backoff, backoff, and post-backoff retries", default 3 ([delivery retries](https://docs.aws.amazon.com/sns/latest/dg/sns-message-delivery-retries.html#creating-delivery-policy)), and the default is "up to three retries with a delay between failed attempts set at 20 seconds" ([endpoint preparation](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.prepare.html)). So attempts = 1 + `numRetries` (inferred). The retries page's own example, 50 retries, says "a total of 50 attempts", one fewer. The same page's built-in policy tables count the same way: 3 + 2 + 10 + 100,000 retries are "100,015 times" in total, and 0 + 2 + 10 + 38 are "50 attempts". SNSH-09 (recorded 2026-11-03) counted 1 + `numRetries` attempts: 9 for 8, 10 for 9, 2 for 1.
- Phases, in order: `numNoDelayRetries` with no delay, then `numMinDelayRetries` at `minDelayTarget`, then `numRetries - numNoDelayRetries - numMinDelayRetries - numMaxDelayRetries` backoff retries, then `numMaxDelayRetries` at `maxDelayTarget`. In the backoff phase "`minDelayTarget` – Sets the delay for the first retry attempt", "`maxDelayTarget` – Sets the delay for the final retry attempt", and `backoffFunction` "determines the algorithm Amazon SNS uses to calculate the delays for all retry attempts between the first and last retries" ([delivery retries](https://docs.aws.amazon.com/sns/latest/dg/sns-message-delivery-retries.html#creating-delivery-policy)).
- The delay runs from the end of the failed attempt (inferred): "if the delivery failure is caused by a timeout, Amazon SNS retries about 35 seconds after the previous attempt" under the 20 s default ([endpoint preparation](https://docs.aws.amazon.com/sns/latest/dg/SendMessageToHttp.prepare.html)), and 35 s is the 15 s timeout plus the 20 s delay. The page does not say where the delay starts for a non-timeout failure. Batch B's `/auto/sleep` path was meant to measure the timeout case, but AWS waited out its 20 s answer and counted it delivered (SNSH-05, 3.1). Every recorded delay ran about 1 s over its target (SNSH-09).
- Backoff shapes: the docs give only a graph for 10 retries from 5 s to 260 s. Exponential "reaching the maximum delay the quickest", linear "increases steadily", arithmetic and geometric "steeper than linear but less rapid than exponential" (same page, figure description). Recorded (SNSH-09, 2026-11-03), gaps between attempts for a backoff phase from 2 s to 64 s, each about 1 s over its target:
  - linear: 3.1, 11.1, 20.1, 29.1, 38.1, 47.1, 56.1, 64.1, even steps from min to max;
  - arithmetic: 3.1, 5.1, 9.1, 16.1, 25.1, 36.1, 49.1, 65.1, close to the squares 2^2..8^2;
  - geometric: 3.1, 4.1, 7.1, 13, 21.1, 33.1, 47.1, 65.1;
  - exponential: 3.1, 3.1, 5.1, 7.1, 12.1, 21.1, 36.1, 64.1;
  - a single backoff retry (min 5, max 40) waits `minDelayTarget` (6.1);
  - phases ran in the documented order (0.1, 0.1, 4.1, 4.1, 4.1, 17.1, 31.1, 31, 31.1 for no-delay 2, min-delay 3 at 3 s, backoff 3 to 30 s, max-delay 2);
  - attempts = 1 + `numRetries`.
  The exact formulas of the arithmetic, geometric and exponential curves are not derived; `sns.http.backoff-curves` keeps `linear-for-all`, a recorded deviation for those three.
- "Amazon SNS applies jittering to delivery retries" (same page). Two runs agreed within 0.1 s from the second retry on: no jitter was visible (SNSH-10, recorded 2026-11-03).
- "The total policy retry time for an HTTP/S endpoint cannot be greater than 3,600 seconds. This is a hard limit" (same page). A violating policy is refused at write: "Invalid parameter: DeliveryPolicy: healthyRetryPolicy: Total policy retry time cannot be greater than 3600 seconds" (topic: "... http.defaultHealthyRetryPolicy: Total policy retry time ...") (SNSH-18, recorded 2026-11-03).

### 6.4 Throttling

`throttlePolicy.maxReceivesPerSecond` (at least 1; default no throttling) limits "the maximum average number of message deliveries per second, per subscription". It "is designed to maintain an average delivery rate and doesn't enforce a strict cap". Self-throttled messages "are re-queued and continue to be delivered as capacity allows, without consuming retry attempts" ([delivery retries](https://docs.aws.amazon.com/sns/latest/dg/sns-message-delivery-retries.html#creating-delivery-policy)). With `maxReceivesPerSecond` 1, a burst of 10 messages all arrived within 0.3 s (gaps 0.01-0.16 s): no pacing was visible for a burst that size (SNSH-20, recorded 2026-11-03). How AWS paces a burst longer than 10 is unrecorded (SNSH-27 would show the averaging window), so understudy stores the setting without applying it and does not pace deliveries.

### 6.5 Dead-letter queue

- `RedrivePolicy` applies to any subscription. A client-side error is not retried, and a server-side error is retried until the policy is exhausted; either way the message then goes to the DLQ, or is discarded when there is none ([DLQs](https://docs.aws.amazon.com/sns/latest/dg/sns-dead-letter-queues.html)). The constraints are in `sns.md 7.4`.
- LocalStack's HTTP DLQ tests (`test_redrive_policy_http_subscription`, `test_dlq_external_http_endpoint`) are not in the validation file, so they record nothing about AWS. Recorded (SNSH-08, 2026-11-03): with raw off the DLQ body is the HTTP notification envelope byte for byte, with no SQS message attributes (`MessageAttributes` stays inside the envelope) and `SenderId` `AROA...:AWS-CLOUDCAST`. The raw form is UNVERIFIED: the raw subscription's message was not redriven (6.1).
- LocalStack's comment "AWS doesn't send to the DLQ if the UnsubscribeConfirmation fails to be delivered" (test_sns.py:L5457) is from an unvalidated test. A `SubscriptionConfirmation` that exhausted its retries did not reach the DLQ (SNSH-17, recorded 2026-11-03); the `UnsubscribeConfirmation` is inferred to match.

### 6.6 Delivery status logging and metrics

- Delivery status logging covers HTTPS among other endpoint types ([delivery status](https://docs.aws.amazon.com/sns/latest/dg/sns-topic-attributes.html)) through the topic attributes `HTTPSuccessFeedbackRoleArn`, `HTTPFailureFeedbackRoleArn` and `HTTPSuccessFeedbackSampleRate` (`sns.md 2.2`). The log group is `sns/<region>/<account-id>/<topic-name>` ([status logging setup](https://docs.aws.amazon.com/sns/latest/dg/topics-attrib.html)), and failures go to `sns/<region>/<account-id>/<topic-name>/Failure`. A record is `{"notification":{"messageMD5Sum","messageId","topicArn","timestamp":"2026-11-03 17:16:30.207"},"delivery":{"deliveryId","destination","providerResponse","dwellTimeMs","attempts"?,"statusCode"},"status":"SUCCESS"|"FAILURE"}`; confirmations are logged too, without `messageMD5Sum`, and `attempts` appeared on only one of two failure records (SNSH-23, recorded 2026-11-03).
- Metrics: "For HTTP or HTTPS endpoints, the metric [`NumberOfNotificationsFailed`] includes every failed delivery attempt, including retries that follow the initial attempt", unlike other endpoints, where it counts once per message. `NumberOfNotificationsDelivered`, the `NumberOfNotificationsFilteredOut*` family and `NumberOfNotificationsRedrivenToDlq` also apply ([metrics](https://docs.aws.amazon.com/sns/latest/dg/sns-monitoring-using-cloudwatch.html)).

### 6.7 Limits and validation texts

| Rule | Value or text | Source |
|---|---|---|
| endpoint scheme | `Invalid parameter: Endpoint must match the specified protocol` | aws-recorded, `testdata/parity/sns/R16/05-http-endpoint-no-scheme.json` |
| http(s) on a FIFO topic | `Invalid parameter: Invalid protocol type: http` | aws-recorded, `testdata/parity/sns/R16/04-subscribe-fifo-http.json` |
| malformed `DeliveryPolicy` | `Invalid parameter: DeliveryPolicy: Invalid JSON: Unexpected character ('n' (code 110)) at [line: 1, column: 2]: was expecting double-quote to start field name` | aws-recorded, `testdata/parity/sns/R16/x-delivery-policy-malformed.json` |
| unknown member (topic) | `Invalid parameter: DeliveryPolicy: Unexpected JSON member: http.nope` | aws-recorded, `testdata/parity/sns/R16/x-delivery-policy-unknown-key.json` |
| `numRetries` over 100 (topic) | `Invalid parameter: DeliveryPolicy: http.defaultHealthyRetryPolicy.numRetries must be less than or equal to 100`; at CreateTopic `Invalid parameter: Attributes Reason: DeliveryPolicy: ...` | third-party-recorded, ballerina-platform/module-ballerinax-aws.sns@51e6a11 ballerina/tests/topics-tests.bal:L182-L184 and the setTopicAttributes test at L510-L512 (input `numRetries: 300`, L40-L57; the tests run against real AWS with the account's keys, `ballerina/tests/init.bal` L24-L38) |
| unknown member (subscription) | `Invalid parameter: DeliveryPolicy: Unexpected JSON member: nope` | aws-recorded, SNSH-18 (2026-11-03) |
| retry policy members required | `Invalid parameter: DeliveryPolicy: healthyRetryPolicy.numRetries must be specified`, then `... healthyRetryPolicy.minDelayTarget must be specified`, checked in that order before any bound; `sicklyRetryPolicy` alike | aws-recorded, SNSH-18 (2026-11-03) |
| total retry time over 3600 s | `Invalid parameter: DeliveryPolicy: healthyRetryPolicy: Total policy retry time cannot be greater than 3600 seconds` (topic: `http.defaultHealthyRetryPolicy: ...`) | aws-recorded, SNSH-18 (2026-11-03) |
| unknown `backoffFunction` | `Invalid parameter: DeliveryPolicy: Invalid JSON member value [healthyRetryPolicy.backoffFunction]: the value [cubic] is not a valid backoff function` | aws-recorded, SNSH-18 (2026-11-03) |
| `numRetries` a string | `Invalid parameter: DeliveryPolicy: healthyRetryPolicy.numRetries type mismatch, expecting integer, received a string` | aws-recorded, SNSH-18 (2026-11-03) |
| `maxReceivesPerSecond` 0 | `Invalid parameter: DeliveryPolicy: throttlePolicy.maxReceivesPerSecond must be &gt;= 1` (`>` XML-escaped) | aws-recorded, SNSH-18 (2026-11-03) |
| content type with raw off | 3.3 | aws-recorded, SNSH-18 (2026-11-03) |
| other bounds | `minDelayTarget` 1 to `maxDelayTarget`; `maxDelayTarget` up to 3600; `numRetries` 0 to 100; phase counts at least 0 | aws-doc ([delivery retries](https://docs.aws.amazon.com/sns/latest/dg/sns-message-delivery-retries.html#creating-delivery-policy)); texts UNVERIFIED: SNSH-18's inputs were incomplete policies, refused for the missing members first |
| `SubscribeURL` errors | `Invalid parameter: Token`, `Invalid parameter: Topic` | third-party-recorded (2.3) |
| pending unsubscribe | 400 `InvalidParameter` `Invalid parameter: SubscriptionArn Reason: Cannot unsubscribe a subscription that is pending confirmation` | aws-recorded, SNSH-25 (2026-11-03); Terraform matches the substring (hashicorp/terraform-provider-aws v6.68.0 internal/service/sns/topic_subscription.go:L276; `sns.md 3.2`) |
| unsigned unsubscribe of an authenticated subscription | 403 `AuthorizationError` `This subscription may only be deleted by an authorized user.` | aws-recorded, SNSH-14 (2026-11-03) |
| internal endpoints | 403 `AuthorizationError` `Not authorized to subscribe internal endpoints`; 400 `InvalidParameter` `Invalid parameter: Unreachable Endpoint` | aws-recorded, SNSH-21 (2026-11-03) |
| token | 288 hex characters (the documented example); 224 recorded (SNSH-12); valid two days | aws-doc (2.2, 2.3), aws-recorded |
| unconfirmed subscription | deleted after 48 hours | aws-doc (2.2) |
| request timeout | about 15 s documented; at least 20 s observed | aws-doc (3.1); aws-recorded, SNSH-05 |
| signing key | RSA 2048 | aws-recorded (5.2) |

## 7. Outbound network for an emulator

### 7.1 Threat model

- An emulator that POSTs to subscriber URLs is a server-side request forgery primitive. By default understudy's AWS API verifies no signature (`sigv4: parse`), and Lambda function code reaches it through the container listener, which serves the AWS API (`ARCHITECTURE.md 14.5`, `20`). Any such caller can `Subscribe` any URL and then `Publish`. With `RawMessageDelivery=true` and `requestPolicy.headerContentType: application/json` (allowed with raw, 3.3), the body and content type of a POST are fully under the caller's control. A Docker Engine on `tcp://localhost:2375` would accept that as a container-create request. Loopback is therefore not a safe default.
- ADR 0015 already decides "outbound delivery runs only to hosts in `sns.httpDelivery.allow` (empty by default, so disabled)". Keep that default.

### 7.2 Allow-list grammar

```
--sns-http-allow HOST:PORT     repeatable; variable UNDERSTUDY_SNS_HTTP_ALLOW (comma-separated); file key "snsHttpAllow" (a list)
entry = host ":" port
host  = "localhost" | IPv4 literal | "[" IPv6 literal "]" | DNS name (ASCII letters, digits, "-", ".")
port  = 1-65535 | "*"
```

- The flag and file key follow the flat camelCase convention of `docs/configuration.md`; ADR 0015's dotted name `sns.httpDelivery.allow` is the same setting and the ADR should be amended to the final name.
- Matching: the subscription URL's host, lower-cased, with any trailing dot and the IPv6 brackets removed and IPv4-mapped IPv6 unmapped, must equal an entry's host. The URL's port, or 80 for `http` and 443 for `https` when it has none, must equal the entry's port, or the entry's port must be `*`. The scheme does not matter. There are no host wildcards and no CIDR ranges: every reachable host is named. `*` ports exist for test receivers on ephemeral ports (`127.0.0.1:*`).
- An entry is checked when the setting loads, and a malformed one stops `serve` with the entry and the grammar in the error. An entry that names a hard-refused host (7.3) is refused there too, rather than silently ignored.
- The public Go API (`understudytest`) can add its own receiver's `127.0.0.1:<port>` entry; that is an option for the implementation, not something this research requires.

### 7.3 Hard refusals

The emulator refuses these no matter what the allow-list says. AWS hosts fall under invariant 1 (never reach real AWS). understudy's own listeners fall under the request-forgery reasoning of 7.1, and ADR 0004 already routes service-to-service traffic through `kernel.Invoke`, never HTTP loopback, so no legitimate delivery targets them. The refusals:

- Host names equal to or under any partition DNS suffix or dual-stack suffix (aws/aws-sdk-go-v2@aae188a internal/endpoints/awsrulesfn/partitions.json): `amazonaws.com`, `api.aws`, `amazonaws.com.cn`, `api.amazonwebservices.com.cn`, `amazonaws.eu`, `api.amazonwebservices.eu`, `c2s.ic.gov`, `api.aws.ic.gov`, `sc2s.sgov.gov`, `api.aws.scloud`, `cloud.adc-e.uk`, `api.cloud-aws.adc-e.uk`, `csp.hci.ic.gov`, `api.aws.hci.ic.gov`. Also the whole `aws` top-level domain, which covers `on.aws`, the Lambda function URL domain.
- Addresses, checked after resolution: the instance metadata endpoints `169.254.169.254` and `fd00:ec2::254`, the ECS endpoint `169.254.170.2`, all link-local addresses (`169.254.0.0/16`, `fe80::/10`), unspecified addresses (`0.0.0.0`, `::`), and multicast.
- understudy's own listeners (main, TLS, container, the per-environment Runtime API listeners) and the configured Docker Engine address.
- A refused delivery is a journaled `Refused{Reason}` outcome. It is never retried, never sent to a DLQ and never reported as an AWS-shaped failure, because AWS would have reached the endpoint and the emulator chose not to.

### 7.4 Client settings

- The dialer checks the actual remote address in its `Control` hook against the entry and 7.3: an entry named `localhost` must connect to a loopback address, an IP entry to that IP. This defeats DNS rebinding between match and connect.
- No proxy: `Proxy: nil`, so `HTTP_PROXY`/`HTTPS_PROXY` cannot route a delivery off the machine. Never follow redirects (`CheckRedirect` returns the last response). HTTP/1.1 only. Read at most a bounded response body and discard it. Send no AWS credentials and none of the caller's headers.
- Timeouts: the 15 s request deadline is network I/O and runs on the wall clock, like function-timeout enforcement in the domain contract (`ARCHITECTURE.md 9`, `lambda-runtime-contract.md 3`). The delays between attempts are AWS semantics and run on the emulator clock (`ARCHITECTURE.md 9`, "delivery retries").
- TLS: verify against the system roots plus understudy's own TLS certificate, plus an optional extra CA bundle flag. Do not skip verification, because AWS requires a trusted CA (3.1). A receiver with an untrusted certificate fails the way an unreachable one does (6.1, inferred).

## 8. Implications for understudy

### 8.1 Operations and coverage notes

No operation is newly bound or unbound. The statuses and exact notes in `docs/coverage.md`:

| Operation | Status | Note |
|---|---|---|
| Subscribe | partial | `sqs and lambda endpoints are delivered to, and their failed deliveries go to the RedrivePolicy queue; http and https endpoints get a signed SubscriptionConfirmation when their host and port are in --sns-http-allow, and otherwise stay subscribed with a readable token but receive nothing; email and email-json stay subscribed with a readable token but receive nothing; sms, application and firehose answer NotImplemented; the quota of 10,000 filter policies per account is not enforced` |
| Publish, PublishBatch | partial | `fan-out to sqs and lambda subscriptions, and to http and https subscriptions whose host and port are in --sns-http-allow, after their filter policies, with raw or enveloped delivery, the DeliveryPolicy retry schedule and the RedrivePolicy dead-letter queue; every backoffFunction steps linearly from minDelayTarget to maxDelayTarget, without jitter; other http and https endpoints, email and email-json receive nothing` |
| SetSubscriptionAttributes | partial | `DeliveryPolicy healthyRetryPolicy and requestPolicy apply to http and https deliveries; throttlePolicy, sicklyRetryPolicy and guaranteed are stored and echoed, never applied, so deliveries are not paced (AWS's pacing above a burst of 10 is unrecorded, SNSH-20); the quota of 10,000 filter policies per account is not enforced` |
| SetTopicAttributes | partial | `Policy, KmsMasterKeyId, TracingConfig, ArchivePolicy and the delivery-status attributes are stored and echoed, never enforced; the http defaults in DeliveryPolicy apply to http and https subscriptions` |
| ConfirmSubscription, Unsubscribe, GetSubscriptionAttributes | full | |
| UnsubscribeLink (raw) | partial | `the UnsubscribeConfirmation reaches the endpoint only when its host and port are in --sns-http-allow` |
| SigningCert, ConfirmSubscriptionLink (raw) | full | |

Delivery status logging stays stored-only, under the SetTopicAttributes note; the journal is the emulator's record of each attempt (6.6).

### 8.2 Delivery engine

1. `internal/delivery` gains a `Target` variant `HTTPEndpoint{URL, Kind, Header, Body}`, where `Kind` is a closed enum `Notification | SubscriptionConfirmation | UnsubscribeConfirmation`. It also gains `Mechanism` `SNSToHTTP` and a `RetryPolicy` variant `SNSHTTP` holding the resolved phases (`NoDelay`, `MinDelay`, `Backoff`, `MaxDelay` counts, both delay targets, the backoff function) and `MaxReceivesPerSecond` (0 for none). SNS resolves the policy from the effective policy (6.2) when it enqueues the delivery. When AWS reads the policy is undocumented, and it does not matter to a test that sets the policy before publishing.
2. An HTTP attempt is tracked work (`DeliveryAttempt`) that calls a new egress package, not `kernel.Invoke`. That package (for example `internal/egress`) is the only code that dials out; it owns the allow-list, the refusals and the client of 7.4, and needs a row in `internal/ref/deps_test.go`. Delivery-phase fault rules run first (ADR 0008), so tests can inject a 500, a 429 or a timeout without a receiver. Each attempt emits a `journal.Delivery` with the host and port, status, class, wall duration and attempt number.
3. Outcome classes follow 6.1 under `sns.http.status-classes`. A retryable outcome arms the next attempt at the completion instant plus the phase delay (6.3). A permanent outcome, or retries exhausted, goes to the `RedrivePolicy` queue when one is set and is discarded otherwise; filtered messages never go to it (ADR 0015). A refusal is terminal (7.3).
4. Control messages: `Subscribe` enqueues the `SubscriptionConfirmation`; an unauthenticated unsubscribe enqueues the `UnsubscribeConfirmation` (2.4, `sns.http.unsubscribe-confirmation`). Both retry under the effective policy and never go to a DLQ (`sns.http.control-dlq`).
5. Throttling: `throttlePolicy` is stored and read back and does not pace attempts (6.4, SNSH-20).
6. While pending, a subscription is skipped at fan-out and nothing is held for it (`sns.http.pending-messages`, 2.2).
7. `settle` counts in-flight HTTP attempts. `clock advance` replays the schedule deterministically; the outcomes still depend on the receiver.

### 8.3 Envelopes, links and signing

1. New pure builders in `internal/service/sns/envelope`: `HTTPNotification`, `HTTPConfirmation` and `HTTPUnsubscribeConfirmation`, with the member order of 4.2 to 4.4 and the layout of 4.1 (golden fixtures from SNSH-01 once recorded). There is also a header builder for 3.2. Write the header names as the docs print them by assigning the `http.Header` map keys directly, since `Header.Set` would canonicalize `x-amz-sns-*`.
2. Signing reuses `envelope.StringToSign` and `ConfirmationStringToSign`; the `UnsubscribeConfirmation` uses the confirmation field list, including `Token` (5.1). Do not drop `Token` to suit `sns-validator` 0.3.5.
3. Links: `SigningCertURL`, `SubscribeURL` and `UnsubscribeURL` stay under `/_understudy/sns/` (`internal/service/sns/links.go`, `sns.md` Implications 7). For HTTP deliveries, base them on the TLS listener (`https://localhost:<tls port>`) when `--tls` is on, so the validators' `https` and `.pem` checks can pass (5.6), and on the calling client's endpoint otherwise. Never on an AWS host. `ARCHITECTURE.md 4` places these links on the main listener's `/_understudy/` prefix, so the TLS listener must serve the three SNS link routes too (it need not serve the admin API).
4. Raw delivery: the body is the message after `MessageStructure` selection with the key `http` or `https` (then `default`), with `x-amz-sns-rawdelivery: true` and no attributes (4.5).
5. Comment updates the evidence now supports: `keyBits` 2048 in `internal/service/sns/envelope/key.go` is aws-recorded (5.2); the 288-hex token length in `internal/service/sns/subscriptions.go` is aws-doc (2.3); confirmation idempotency and the `PendingConfirmation` listing ARN are third-party-recorded (2.2, 2.3).

### 8.4 Fidelity switches

| Name | Default | Label | Grounds | Settled by |
|---|---|---|---|---|
| `sns.http.status-classes` | `retries-doc` (2xx success; 429, 5xx, timeout, connection failure retryable; everything else permanent). AWS counts 3xx and other 4xx as delivered (6.1), a recorded deviation | inferred | 6.1 | SNSH-05; SNSH-24 n/a |
| `sns.http.redirects` | `not-followed` (a 3xx is the attempt's status) | aws-recorded (SNSH-06) | 3.1, 6.1 | SNSH-06 |
| `sns.http.retry-body` | `identical` (retries resend the first attempt's bytes) | aws-recorded (SNSH-07) | 4.6 | SNSH-07 |
| `sns.http.dlq-body` | `as-sqs` (envelope, or raw body plus attributes as SQS attributes; the raw form is not recorded) | aws-recorded (SNSH-08) | 6.5 | SNSH-08 |
| `sns.http.backoff-curves` | `linear-for-all` (each backoff retry steps evenly from `minDelayTarget` to `maxDelayTarget`; one backoff retry waits `minDelayTarget`). Linear and the one-retry rule match; AWS's other three curves differ (6.3), a recorded deviation | inferred | 6.3 | SNSH-09 |
| `sns.http.retry-jitter` | `none` | aws-recorded (SNSH-10) | 6.3 | SNSH-10 |
| `sns.http.pending-messages` | `dropped` | aws-recorded (SNSH-11) | 2.2 | SNSH-11 |
| `sns.http.unsubscribe-confirmation` | `unauthenticated-only` (sent for an unsigned unsubscribe, not for a signed one) | aws-recorded (SNSH-15) | 2.4 | SNSH-15 |
| `sns.http.control-dlq` | `never` (failed confirmations are discarded) | aws-recorded (SNSH-17) | 6.5 | SNSH-17 |
| `sns.http.control-content-type` | `as-request-policy` (`headerContentType` applies to all three types); was `text-plain` until 2026-11-03 | aws-recorded (SNSH-03) | 3.3 | SNSH-03 |
| `sns.http.basic-auth` | `preemptive` (Basic credentials from the URL on the first request; Digest not implemented). SNSH-22 saw no `authorization` header at all, possibly stripped by its receiver | inferred | 3.1 | SNSH-22 |

`sns.http.resubscribe-pending` (`resend-same-token`) is retired: a repeated `Subscribe` while pending sends a new token and every token sent confirms, as SNSH-12 recorded (2.2).

`linear-for-all` is deliberately not a guess at the other three curves: it stays inside the documented bounds and the coverage note says so. No switch is needed for attempts = 1 + `numRetries` (recorded, SNSH-09). The 15 s timeout is aws-doc, and SNSH-05 saw AWS wait at least 20 s (3.1), a deviation.

### 8.5 Limits and texts

1. Keep the recorded texts in 6.7 byte-exact. Replace the inferred `failed to parse JSON.` text for `DeliveryPolicy` (`internal/service/sns/topic_attributes.go`, `subscription_attrs.go`) with the recorded `Invalid JSON: ...` Jackson form; reject unknown members at topic level (`testdata/parity/sns/R16/notes.md` already lists both as differences). Both texts are recorded for `SetTopicAttributes` only.
2. Keep the inferred bound texts in `internal/service/sns/delivery_policy.go`. Only the `numRetries` upper bound is recorded among the bounds. SNSH-18 (2026-11-03) recorded the required `numRetries` and `minDelayTarget` members, the 3,600 s total-time check, the `backoffFunction` and type-mismatch texts, the `maxReceivesPerSecond` text, the content-type sets for raw and non-raw (3.3) and the subscription-level malformed-JSON and unknown-member texts (6.7); understudy applies all but the malformed-JSON text (`internal/service/sns/delivery_policy.go`), and checks the 3,600 s total against the sum of its own retry schedule's delays (6.3), an inferred boundary.
3. A subscription's `DeliveryPolicy` reads back in AWS's canonical form (6.2), third-party-recorded; Terraform normalizes either form (8.6).

### 8.6 Terraform: apply and an empty second plan

1. `aws_sns_topic_subscription` with protocol `http`/`https` and the default `endpoint_auto_confirms = false` sends `Subscribe` with `ReturnSubscriptionArn=true`, does not wait, and reads `GetSubscriptionAttributes` (hashicorp/terraform-provider-aws v6.68.0 internal/service/sns/topic_subscription.go:L168-L174, L184-L193, L436-L451; call sequence in `tf-messaging.md 3.5`). This works with HTTP delivery off.
2. With `endpoint_auto_confirms = true` it polls `PendingConfirmation` until `"false"` for `confirmation_timeout_in_minutes` (default 1, wall time; L46-L50, L71-L75, L184-L193). Then `ListSubscriptionsByTopic` must list the real ARN (L210). That needs an allow-listed receiver that GETs `SubscribeURL` within the minute. The confirmation POST must therefore go out as soon as `Subscribe` returns, even on a frozen clock, which a due timer gives (`ARCHITECTURE.md 9`).
3. Second plan: `endpoint` and `protocol` must read back verbatim. `pending_confirmation` and `confirmation_was_authenticated` are computed and never diff. The `delivery_policy` diff suppression unmarshals into a struct of `guaranteed`, `healthyRetryPolicy`, `sicklyRetryPolicy` and `throttlePolicy` with `omitempty` and compares compact JSON (L509-L514, L576-L611). Nulls, zero values and `requestPolicy` therefore drop out, and AWS's canonical read-back (6.2) shows no diff. Do not return a `DeliveryPolicy` that was never set (`tf-messaging.md 3.5`).
4. Destroying a pending subscription: `Unsubscribe` fails 400 `InvalidParameter` "Invalid parameter: SubscriptionArn Reason: Cannot unsubscribe a subscription that is pending confirmation" (SNSH-25, recorded 2026-11-03), Terraform swallows it (L276), and the subscription stays until the 48 h expiry, as in AWS (2.2).

### 8.7 Validators and the guide

The user-visible change needs a `docs/guide/` section whose commands were run against a build:

- `understudy serve --tls --sns-http-allow 127.0.0.1:<port>`, then `eval "$(understudy env --tls)"`, which sets `NODE_EXTRA_CA_CERTS` (`docs/guide/sdks.md`, TLS).
- `sns-validator`: `new MessageValidator(/^localhost:4567$/)`, where the host includes the port (5.3); its `UnsubscribeConfirmation` failure matches AWS.
- PHP: `new MessageValidator(null, '/^localhost$/')`, with PHP trusting the TLS certificate, or a `certClient` (5.4).
- Ruby `Aws::SNS::MessageVerifier`: cannot accept a non-AWS host (5.5).
- An `openssl dgst -verify` recipe against the certificate at `SigningCertURL`, as in the AWS verification steps.

### 8.8 Never faked

- No AWS host name in any link, header or body the emulator sends, and no connection to one (7.3).
- No delivery reported as made when it was refused, and no refusal dressed as an AWS failure: no retry, no DLQ (7.3).
- No redirect followed, no proxy used, no credentials of the caller forwarded (7.4).
- No DLQ error attributes and no delivery-status log records invented (`sns.md 7.4`, 6.6).
- No signature computed over a field list other than the one AWS signs (5.1), even where a validator expects one.

## 9. Recording queue

For a human with credentials in the recording-kit shell (`docs/recording-kit.md` sections 1-2: `$P`, `$R`, `$A`, `$Q`, `$REC`, `rec`, `recurl`); agents never run these. The receiver is a Lambda function URL, as in LocalStack's AWS-validated HTTP test (`create_sns_http_endpoint_and_queue`, tests/aws/services/sns/conftest.py, which forwards each request to SQS); this one logs each request to CloudWatch Logs and answers by path. A function URL normalizes header-name casing, so header casing stays doc-only. Batch I alone needs a receiver that can refuse connections, so it runs a short-lived EC2 instance; Batch J adds an API Gateway HTTP API in front of the function for the cases a function URL cannot serve. Costs are Lambda, CloudWatch Logs, SNS, SQS and API Gateway at a few hundred requests plus about 15 minutes of one `t4g.nano` with a public IPv4 address, under USD 0.15 in total. The captures distill as `SNSH` items (`docs/recording-kit.md` section 6): AWS calls under `testdata/parity/sns/SNSH/`, log reads under `testdata/parity/logs/SNSH/`.

### 9.1 Queue

| ID | Settles | Switch or claim | Batch | Read from the capture |
|---|---|---|---|---|
| SNSH-01 | HTTP body layout of all three types, attributes block, query string passed through, confirmations of a subscription created raw | 4.1, 3.1, 3.2, 4.5 | A | `body` strings in `A-receiver`; `query` of `/auto/q`; `headers` and `body` of the `/auto/raw` SubscriptionConfirmation and UnsubscribeConfirmation |
| SNSH-02 | Version-2 signature over HTTP | 5.2 | A | `A-verify.txt` lines 3 and 4 say `Verified OK` |
| SNSH-03 | `headerContentType` on confirmations | `sns.http.control-content-type` | A | `content-type` of the `/auto/json` and `/auto/plain` confirmations and their UnsubscribeConfirmations |
| SNSH-04 | `MessageStructure` key for https | 4.2 | A | the `/auto/a` body of `A-publish-structure` is `for-https` |
| SNSH-05 | Classes of 2xx, 3xx, 4xx; DLQ for permanent failures | `sns.http.status-classes` | B | attempts per path in `B-receiver`; which paths reached `B-dlq-*` |
| SNSH-06 | Redirect following | `sns.http.redirects` | B | any POST at `/auto/redirected` |
| SNSH-07 | Retry body identity | `sns.http.retry-body` | B | `Timestamp`, `Signature`, `MessageId` across `/auto/s500` attempts |
| SNSH-08 | DLQ message form for HTTP, raw and enveloped | `sns.http.dlq-body` | B | `B-dlq-*` bodies and message attributes |
| SNSH-09 | Backoff formulas, a single backoff retry, phase order, attempt count | `sns.http.backoff-curves` | C | gaps between attempts per path in `C-receiver` |
| SNSH-10 | Jitter | `sns.http.retry-jitter` | C | the same gaps for `m-c-1` against `m-c-2` |
| SNSH-11 | Messages published while pending | `sns.http.pending-messages` | D | does `m-d-before-confirm` ever reach `/hold/a` |
| SNSH-12 | Re-subscribe while pending | `sns.http.resubscribe-pending` | D | count of confirmations at `/hold/a`, tokens equal or not, `D-confirm-*` results, ARN in `D-subscribe-hold-again` |
| SNSH-13 | `ConfirmationWasAuthenticated`; `AuthenticateOnUnsubscribe` parsing | 2.3 | D | `D-attrs-confirmed`, `D-confirm-flag-yes`, `D-attrs-flag` |
| SNSH-14 | Refusal of an unsigned unsubscribe | `errUnsubscribeNeedsCredentials` | D | `D-unsubscribe-link-authenticated.http` status and body |
| SNSH-15 | UnsubscribeConfirmation after a signed owner unsubscribe | `sns.http.unsubscribe-confirmation` | D | any UnsubscribeConfirmation at `/hold/a` after `D-unsubscribe-signed` |
| SNSH-16 | Restore via the UnsubscribeConfirmation link | 2.4 | D | `D-restore-link.http`, `D-attrs-restored`, `D-list` |
| SNSH-17 | Confirmation retries and DLQ | `sns.http.control-dlq` | D | attempts and spacing at `/cfail/x`; `D-dlq` |
| SNSH-18 | DeliveryPolicy validation texts (malformed JSON at subscription level included), 3,600 s cap, content-type sets | 6.7, 3.3, 8.5 | E | each `E-*` error text or acceptance |
| SNSH-19 | DeliveryPolicy read-back and effective-policy merge | 6.2 | E | `E-attrs-*` |
| SNSH-20 | Throttle pacing | 6.4 | F | arrival times at `/auto/thr` |
| SNSH-21 | Subscribe checks for loopback, private and unresolvable endpoints | 2.1 | G | `G-subscribe-*` results |
| SNSH-22 | Basic auth and endpoint masking | `sns.http.basic-auth` | G | `authorization` header per attempt at `/auth/auto`; `Endpoint` in `G-attrs-auth` and `G-list` |
| SNSH-23 | Delivery status log groups and schema for HTTPS | 6.6 | H | `H-log-groups`, `H-events-*` |
| SNSH-24 | Retry class of a refused and of a dropped connection | `sns.http.status-classes`, 6.1 | I | `I-failed-*` sums; `I-dlq` `SentTimestamp` against `I-publish-time.txt` |
| SNSH-25 | Code, status and full text of `Unsubscribe` on a pending subscription | 6.7, 8.6 | E | `E-unsubscribe-pending` error |
| SNSH-26 | Request timeout: endpoints answering after 25, 35 and 50 s | 3.1, 6.3, `sns.http.status-classes` | J | attempts and gaps per `/auto/sleepNN` path in `J-receiver-timeout` |
| SNSH-27 | Throttle pacing over a 60-message burst | 6.4 | J | `SentTimestamp` of the 60 messages in `J-thr-recv-*` |
| SNSH-28 | Which exhausted 429 and 5xx deliveries are redriven, one subscription at a time; the raw DLQ body | 6.1, 6.5, `sns.http.status-classes`, `sns.http.dlq-body` | J | `J-dlq-redrive-*` bodies per path; attempts in `J-receiver-redrive-*` |
| SNSH-29 | Backoff curves with two more parameter sets | 6.3, `sns.http.backoff-curves` | J | gaps between attempts per path in `J-receiver-backoff-*` |
| SNSH-30 | Basic auth through a receiver that keeps `authorization`; whether a function URL strips it | 3.1, `sns.http.basic-auth` | J | `J-receiver-probe`; `authorization` per request at `/auth/auto` in `J-receiver-auth` |

Recorded 2026-11-03 in us-east-2 (`testdata/parity/sns/SNSH/notes.md`); SNSH-24 was n/a (the free plan refuses its EC2 instance type). Recipe changes the run showed are needed before a re-record:

- SNSH-18: give each bound input a complete policy (`numRetries` and `minDelayTarget` at least); AWS refused the incomplete ones for the missing members first, so the bound texts stay unrecorded. The same holds for SNSH-19's `disableSubscriptionOverrides` topic policy.
- SNSH-05 and SNSH-09: the account's Lambda concurrency of 10 throttled batch B's 16 simultaneous deliveries; fewer concurrent subscriptions, or a receiver that is not a function URL, would settle the DLQ anomaly in 6.1.
- SNSH-22: use a receiver that is known to keep the `authorization` header.

Batch J (9.12) carries the follow-ups: the delivery timeout (SNSH-26), a longer throttle burst (SNSH-27), the redrive anomaly with one subscription at a time (SNSH-28), more backoff parameter sets (SNSH-29) and Basic auth through an API Gateway receiver (SNSH-30). The SNSH-18 and SNSH-19 amendments above are still to be made in Batch E.

### 9.2 Shared setup

About 10 minutes, no cost. It creates the receiver function `$P-snsh`, its role `$P-snsh-role`, a public function URL and the DLQ `$P-snsh-dlq`.

```sh
mkdir -p "$REC/SNSH" && cd "$REC/SNSH"
cat >snsh.py <<'EOF'
import base64, json, time, urllib.request

def handler(event, context):
    path = event.get("rawPath") or "/"
    seg = [s for s in path.split("/") if s]
    h = event.get("headers") or {}
    body = event.get("body") or ""
    if event.get("isBase64Encoded"):
        body = base64.b64decode(body).decode("utf-8", "replace")
    kind = h.get("x-amz-sns-message-type", "")
    print(json.dumps({"t": time.time(), "path": path, "query": event.get("rawQueryString", ""),
                      "kind": kind, "headers": h, "body": body}))
    if "auth" in seg and "authorization" not in h:
        return {"statusCode": 401, "headers": {"WWW-Authenticate": 'Basic realm="snsh"'}, "body": ""}
    if kind == "SubscriptionConfirmation":
        if "cfail" in seg:
            return {"statusCode": 500, "body": ""}
        if "auto" in seg:
            with urllib.request.urlopen(json.loads(body)["SubscribeURL"], timeout=10) as r:
                print(json.dumps({"confirmed": path, "status": r.status,
                                  "type": r.headers.get("Content-Type"), "xml": r.read().decode()}))
        return {"statusCode": 200, "body": ""}
    for s in seg:
        if s == "sleep" or (s[:5] == "sleep" and s[5:].isdigit()):   # /sleep waits 20 s, /sleepNN NN s
            time.sleep(int(s[5:] or 20))
            print(json.dumps({"slept": path}))
    code = 200
    for s in seg:
        if s[:1] == "s" and s[1:].isdigit():
            code = int(s[1:])
    headers = {}
    if 300 <= code < 400:
        headers["Location"] = "https://" + h.get("host", "") + "/auto/redirected"
    return {"statusCode": code, "headers": headers, "body": ""}
EOF
zip -q snsh.zip snsh.py
aws iam create-role --role-name "$P-snsh-role" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam attach-role-policy --role-name "$P-snsh-role" \
  --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
sleep 10
aws lambda create-function --function-name "$P-snsh" --runtime python3.13 --handler snsh.handler \
  --timeout 60 --role "arn:aws:iam::$A:role/$P-snsh-role" --zip-file fileb://snsh.zip
aws lambda wait function-active-v2 --function-name "$P-snsh"
U=$(aws lambda create-function-url-config --function-name "$P-snsh" --auth-type NONE --query FunctionUrl --output text)
aws lambda add-permission --function-name "$P-snsh" --statement-id url \
  --action lambda:InvokeFunctionUrl --principal '*' --function-url-auth-type NONE
aws lambda add-permission --function-name "$P-snsh" --statement-id url-invoke \
  --action lambda:InvokeFunction --principal '*' --invoked-via-function-url
curl -sS -o /dev/null -w '%{http_code}\n' -X POST "${U}probe"     # expect 200
aws sqs create-queue --queue-name "$P-snsh-dlq"
DLQ="arn:aws:sqs:$R:$A:$P-snsh-dlq"
aws sqs set-queue-attributes --queue-url "$Q/$P-snsh-dlq" --attributes "$(jq -nc --arg q "$DLQ" \
  --arg t "arn:aws:sns:$R:$A:$P-snsh-*" '{Policy: ({Version: "2012-10-17", Statement: [{Effect: "Allow",
  Principal: {Service: "sns.amazonaws.com"}, Action: "sqs:SendMessage", Resource: $q,
  Condition: {ArnLike: {"aws:SourceArn": $t}}}]} | tojson)}')"

LG="/aws/lambda/$P-snsh"
now_ms() { echo $(($(date +%s) * 1000)); }
snshtopic() {  # snshtopic <name>: make topic $P-snsh-<name> and set TOPIC
  TOPIC=$(aws sns create-topic --name "$P-snsh-$1" --query TopicArn --output text)
}
snshsub() {  # snshsub <step> <path> [attributes-json]: subscribe ${U}<path> to $TOPIC, print the ARN
  local extra=()
  [ -n "${3:-}" ] && extra=(--attributes "$3")
  rec "SNSH/$1" aws sns subscribe --topic-arn "$TOPIC" --protocol https \
    --notification-endpoint "${U}${2#/}" --return-subscription-arn "${extra[@]}"
  sed -n 's/^ *"SubscriptionArn": "\([^"]*\)".*/\1/p' "$REC/SNSH/$1.out"
}
snshlog() {  # snshlog <step> <start-ms>: record the receiver's log since start-ms
  sleep 20
  rec "SNSH/$1" aws logs filter-log-events --log-group-name "$LG" --start-time "$2"
}
snshmsgs() {  # snshmsgs <path> <kind>: bodies that reached <path> as <kind>, oldest first, one per line
  aws logs filter-log-events --log-group-name "$LG" --output json | jq -c --arg p "$1" --arg k "$2" \
    '.events[].message | fromjson? | select(.path == $p and .kind == $k) | (.body | fromjson? // .body)'
}
getlink() {  # getlink <step> <url>: unsigned GET of an SNS link, files laid out as recurl's
  curl -sS -i -v --http1.1 "$2" >"$REC/SNSH/$1.http" 2>"$REC/SNSH/$1.trace"
}
snshdlq() {  # snshdlq <step>: record up to 10 DLQ messages
  rec "SNSH/$1" aws sqs receive-message --queue-url "$Q/$P-snsh-dlq" --max-number-of-messages 10 \
    --wait-time-seconds 5 --attribute-names All --message-attribute-names All
}
snshdrop() {  # snshdrop: unsubscribe what can be, then delete $TOPIC
  for s in $(aws sns list-subscriptions-by-topic --topic-arn "$TOPIC" --query 'Subscriptions[].SubscriptionArn' --output text); do
    [ "$s" = PendingConfirmation ] || aws sns unsubscribe --subscription-arn "$s"
  done
  aws sns delete-topic --topic-arn "$TOPIC"
}
```

If the CLI rejects `--invoked-via-function-url`, skip that line; the probe's 200 shows that the URL works without it. A 403 means the second permission is needed.

### 9.3 Batch A: wire shapes (SNSH-01 to SNSH-04)

About 15 minutes, under USD 0.01.

```sh
snshtopic a; T0=$(now_ms)
SA=$(snshsub A-subscribe /auto/a)
SR=$(snshsub A-subscribe-raw /auto/raw '{"RawMessageDelivery":"true"}')
snshsub A-subscribe-query '/auto/q?x=1&y=2' >/dev/null
sleep 20
rec SNSH/A-attrs aws sns get-subscription-attributes --subscription-arn "$SA"
ATTRS='{"s":{"DataType":"String","StringValue":"v"},"n":{"DataType":"Number","StringValue":"1.5"},"b":{"DataType":"Binary","BinaryValue":"AAEC"},"a":{"DataType":"String.Array","StringValue":"[\"x\",1]"}}'
rec SNSH/A-publish aws sns publish --topic-arn "$TOPIC" --subject subj \
  --message $'line1\nline2 "q" & <x> \\ /\té' --message-attributes "$ATTRS"
rec SNSH/A-publish-structure aws sns publish --topic-arn "$TOPIC" --message-structure json \
  --message '{"default":"d","http":"for-http","https":"for-https"}'
aws sns set-topic-attributes --topic-arn "$TOPIC" --attribute-name SignatureVersion --attribute-value 2
rec SNSH/A-publish-v2 aws sns publish --topic-arn "$TOPIC" --message m-v2
sleep 15
getlink A-unsubscribe-link "$(snshmsgs /auto/a Notification | tail -1 | jq -r .UnsubscribeURL)"
# a raw body carries no UnsubscribeURL, so build the documented form (4.6)
getlink A-unsubscribe-link-raw "https://sns.$R.amazonaws.com/?Action=Unsubscribe&SubscriptionArn=$SR"
sleep 15
snshdrop
snshtopic a-json
aws sns set-topic-attributes --topic-arn "$TOPIC" --attribute-name DeliveryPolicy \
  --attribute-value '{"http":{"defaultRequestPolicy":{"headerContentType":"application/json"}}}'
snshsub A-subscribe-json /auto/json >/dev/null
snshsub A-subscribe-plain /auto/plain '{"DeliveryPolicy":"{\"requestPolicy\":{\"headerContentType\":\"text/plain\"}}"}' >/dev/null
sleep 20
rec SNSH/A-publish-json aws sns publish --topic-arn "$TOPIC" --message m-json
sleep 15
for p in json plain; do
  getlink "A-unsubscribe-link-$p" "$(snshmsgs "/auto/$p" Notification | tail -1 | jq -r .UnsubscribeURL)"
done
snshlog A-receiver "$T0"

verify() {  # verify <body-file> <field>...: openssl's verdict on the string to sign built from the fields
  local f=$1; shift
  curl -sS "$(jq -r .SigningCertURL "$f")" -o cert.pem
  openssl x509 -pubkey -noout -in cert.pem >pub.pem
  jq -r .Signature "$f" | base64 -d >sig.bin
  local alg=-sha1; [ "$(jq -r .SignatureVersion "$f")" = 2 ] && alg=-sha256
  jq -j '. as $m | $ARGS.positional[] | select($m[.] != null) | "\(.)\n\($m[.])\n"' --args "$@" <"$f" |
    openssl dgst "$alg" -verify pub.pem -signature sig.bin
}
snshmsgs /auto/a SubscriptionConfirmation | tail -1 >conf.json
snshmsgs /auto/a Notification | head -1 >note.json
snshmsgs /auto/a Notification | tail -1 >note-v2.json
snshmsgs /auto/a UnsubscribeConfirmation | tail -1 >unsub.json
{
  verify conf.json Message MessageId SubscribeURL Timestamp Token TopicArn Type
  verify note.json Message MessageId Subject Timestamp TopicArn Type
  verify note-v2.json Message MessageId Subject Timestamp TopicArn Type
  verify unsub.json Message MessageId SubscribeURL Timestamp Token TopicArn Type
  verify unsub.json Message MessageId Subject SubscribeURL Timestamp TopicArn Type   # sns-validator 0.3.5's list
  verify unsub.json Message MessageId Timestamp TopicArn Type                        # the format page's list
} 2>&1 | tee "$REC/SNSH/A-verify.txt"
rm -f cert.pem pub.pem sig.bin
```

Expected: lines 1 to 4 `Verified OK`, lines 5 and 6 `Verification failure` (5.1, 5.3). Line 4 is the version-2 `UnsubscribeConfirmation` that SNSH-02 needs alongside line 3. Teardown: `snshdrop` (topic `a-json`).

### 9.4 Batch B: response classes (SNSH-05 to SNSH-08)

About 10 minutes, under USD 0.01.

```sh
snshtopic b
DP='{"healthyRetryPolicy":{"numRetries":2,"minDelayTarget":5,"maxDelayTarget":5}}'
RP=$(jq -nrc --arg q "$DLQ" '{deadLetterTargetArn: $q} | tojson')
for p in s201 s204 s299 s301 s302 s307 s400 s403 s404 s410 s429 s500 s502 s503 sleep; do
  snshsub "B-subscribe-$p" "/auto/$p" "$(jq -nc --arg d "$DP" --arg r "$RP" '{DeliveryPolicy: $d, RedrivePolicy: $r}')" >/dev/null
done
snshsub B-subscribe-raw500 /auto/raw/s500 "$(jq -nc --arg d "$DP" --arg r "$RP" \
  '{DeliveryPolicy: $d, RedrivePolicy: $r, RawMessageDelivery: "true"}')" >/dev/null
sleep 30
T0=$(now_ms)
rec SNSH/B-publish aws sns publish --topic-arn "$TOPIC" --subject sb --message m-b \
  --message-attributes '{"k":{"DataType":"String","StringValue":"v"}}'
sleep 180
snshlog B-receiver "$T0"
snshdlq B-dlq-1; snshdlq B-dlq-2; snshdlq B-dlq-3
```

Read per path: one attempt means no retry, three means retried. `/auto/sleep` attempts about 15 s plus 5 s apart give the timeout and show that the delay starts when the attempt fails (6.3). Teardown: `snshdrop; aws sqs purge-queue --queue-url "$Q/$P-snsh-dlq"; sleep 60`.

### 9.5 Batch C: retry schedule (SNSH-09, SNSH-10)

About 25 minutes, under USD 0.01.

```sh
snshtopic c
for f in linear arithmetic geometric exponential; do
  snshsub "C-subscribe-$f" "/auto/$f/s500" "$(jq -nc --arg f "$f" \
    '{DeliveryPolicy: ({healthyRetryPolicy: {numRetries: 8, minDelayTarget: 2, maxDelayTarget: 64, backoffFunction: $f}} | tojson)}')" >/dev/null
done
snshsub C-subscribe-phases /auto/phases/s500 "$(jq -nc '{DeliveryPolicy: ({healthyRetryPolicy: {numRetries: 9,
  numNoDelayRetries: 2, numMinDelayRetries: 2, numMaxDelayRetries: 2, minDelayTarget: 3, maxDelayTarget: 30,
  backoffFunction: "linear"}} | tojson)}')" >/dev/null
snshsub C-subscribe-one /auto/one/s500 "$(jq -nc '{DeliveryPolicy: ({healthyRetryPolicy: {numRetries: 1,
  minDelayTarget: 5, maxDelayTarget: 40, backoffFunction: "linear"}} | tojson)}')" >/dev/null
sleep 30
T0=$(now_ms); rec SNSH/C-publish-1 aws sns publish --topic-arn "$TOPIC" --message m-c-1
sleep 600
rec SNSH/C-publish-2 aws sns publish --topic-arn "$TOPIC" --message m-c-2
sleep 600
snshlog C-receiver "$T0"
```

Expected for `/auto/phases/s500`: 10 attempts, gaps 0, 0, 3, 3, then three backoff gaps from 3 to 30, then 30, 30. Teardown: `snshdrop`.

### 9.6 Batch D: confirmation lifecycle (SNSH-11 to SNSH-17)

About 15 minutes, under USD 0.01.

```sh
snshtopic d; T0=$(now_ms)
SH=$(snshsub D-subscribe-hold /hold/a)
rec SNSH/D-publish-pending aws sns publish --topic-arn "$TOPIC" --message m-d-before-confirm
sleep 20
snshsub D-subscribe-hold-again /hold/a >/dev/null
sleep 20
TOK1=$(snshmsgs /hold/a SubscriptionConfirmation | head -1 | jq -r .Token)
TOK2=$(snshmsgs /hold/a SubscriptionConfirmation | tail -1 | jq -r .Token)
rec SNSH/D-confirm-first aws sns confirm-subscription --topic-arn "$TOPIC" --token "$TOK1" \
  --authenticate-on-unsubscribe true
rec SNSH/D-confirm-second aws sns confirm-subscription --topic-arn "$TOPIC" --token "$TOK2" \
  --authenticate-on-unsubscribe true
rec SNSH/D-attrs-confirmed aws sns get-subscription-attributes --subscription-arn "$SH"
SF=$(snshsub D-subscribe-flag /hold/b)
sleep 20
rec SNSH/D-confirm-flag-yes aws sns confirm-subscription --topic-arn "$TOPIC" \
  --token "$(snshmsgs /hold/b SubscriptionConfirmation | tail -1 | jq -r .Token)" --authenticate-on-unsubscribe yes
rec SNSH/D-attrs-flag aws sns get-subscription-attributes --subscription-arn "$SF"
sleep 60
rec SNSH/D-publish-confirmed aws sns publish --topic-arn "$TOPIC" --message m-d-after-confirm
sleep 15
getlink D-unsubscribe-link-authenticated "$(snshmsgs /hold/a Notification | tail -1 | jq -r .UnsubscribeURL)"
rec SNSH/D-unsubscribe-signed aws sns unsubscribe --subscription-arn "$SH"
SS=$(snshsub D-subscribe-restore /auto/restore)
sleep 20
rec SNSH/D-publish-restore aws sns publish --topic-arn "$TOPIC" --message m-d-restore
sleep 15
getlink D-unsubscribe-link "$(snshmsgs /auto/restore Notification | tail -1 | jq -r .UnsubscribeURL)"
sleep 20
getlink D-restore-link "$(snshmsgs /auto/restore UnsubscribeConfirmation | tail -1 | jq -r .SubscribeURL)"
rec SNSH/D-attrs-restored aws sns get-subscription-attributes --subscription-arn "$SS"
rec SNSH/D-list aws sns list-subscriptions-by-topic --topic-arn "$TOPIC"
snshsub D-subscribe-cfail /cfail/x "$(jq -nc --arg q "$DLQ" '{RedrivePolicy: ({deadLetterTargetArn: $q} | tojson)}')" >/dev/null
sleep 150
snshlog D-receiver "$T0"
snshdlq D-dlq
```

If `D-confirm-second` fails, `TOK2` no longer confirms; if `TOK1 = TOK2`, the repeat resent the same token. Teardown: `snshdrop; aws sqs purge-queue --queue-url "$Q/$P-snsh-dlq"`.

### 9.7 Batch E: delivery policy texts, read-back and pending unsubscribe (SNSH-18, SNSH-19, SNSH-25)

About 10 minutes, no cost. No message is delivered; the subscriptions stay pending.

```sh
snshtopic e
SE=$(snshsub E-subscribe /hold/e)
rec SNSH/E-attrs-fresh aws sns get-subscription-attributes --subscription-arn "$SE"
dp() { rec "SNSH/E-$1" aws sns set-subscription-attributes --subscription-arn "$SE" \
  --attribute-name DeliveryPolicy --attribute-value "$2"; }
dp malformed '{not json'
dp retries-101 '{"healthyRetryPolicy":{"numRetries":101}}'
dp retries-minus-1 '{"healthyRetryPolicy":{"numRetries":-1}}'
dp min-0 '{"healthyRetryPolicy":{"minDelayTarget":0}}'
dp min-over-max '{"healthyRetryPolicy":{"minDelayTarget":30,"maxDelayTarget":20}}'
dp max-3601 '{"healthyRetryPolicy":{"minDelayTarget":1,"maxDelayTarget":3601}}'
dp phases-over '{"healthyRetryPolicy":{"numRetries":3,"numNoDelayRetries":2,"numMinDelayRetries":2}}'
dp total-over-3600 '{"healthyRetryPolicy":{"numRetries":100,"numMaxDelayRetries":100,"minDelayTarget":3600,"maxDelayTarget":3600}}'
dp backoff-cubic '{"healthyRetryPolicy":{"backoffFunction":"cubic"}}'
dp backoff-upper '{"healthyRetryPolicy":{"backoffFunction":"LINEAR"}}'
dp retries-string '{"healthyRetryPolicy":{"numRetries":"3"}}'
dp rps-0 '{"throttlePolicy":{"maxReceivesPerSecond":0}}'
dp csv-not-raw '{"requestPolicy":{"headerContentType":"text/csv"}}'
dp xml-not-raw '{"requestPolicy":{"headerContentType":"application/xml"}}'
dp json-charset '{"requestPolicy":{"headerContentType":"application/json; charset=UTF-8"}}'
dp unknown-member '{"nope":1}'
dp sickly-guaranteed '{"sicklyRetryPolicy":{"numRetries":1},"guaranteed":true}'
dp partial '{"healthyRetryPolicy":{"numRetries":5}}'
rec SNSH/E-attrs-partial aws sns get-subscription-attributes --subscription-arn "$SE"
tp() { rec "SNSH/E-topic-$1" aws sns set-topic-attributes --topic-arn "$TOPIC" \
  --attribute-name DeliveryPolicy --attribute-value "$2"; }
tp total-over-3600 '{"http":{"defaultHealthyRetryPolicy":{"numRetries":100,"numMaxDelayRetries":100,"minDelayTarget":3600,"maxDelayTarget":3600}}}'
tp csv '{"http":{"defaultRequestPolicy":{"headerContentType":"text/csv"}}}'
tp override '{"http":{"defaultHealthyRetryPolicy":{"numRetries":7},"disableSubscriptionOverrides":true}}'
rec SNSH/E-attrs-override aws sns get-subscription-attributes --subscription-arn "$SE"
SE2=$(snshsub E-subscribe-plain /hold/e2)
rec SNSH/E-attrs-plain-under-override aws sns get-subscription-attributes --subscription-arn "$SE2"
rec SNSH/E-unsubscribe-pending aws sns unsubscribe --subscription-arn "$SE"
```

Teardown: `snshdrop`; the pending subscriptions go with the topic, or after 48 hours at the latest (2.2).

### 9.8 Batch F: throttling (SNSH-20)

About 5 minutes, under USD 0.01.

```sh
snshtopic f
snshsub F-subscribe /auto/thr "$(jq -nc '{DeliveryPolicy: ({throttlePolicy: {maxReceivesPerSecond: 1}} | tojson)}')" >/dev/null
sleep 20; T0=$(now_ms)
for i in $(seq 1 10); do aws sns publish --topic-arn "$TOPIC" --message "m-f-$i" >/dev/null & done; wait
sleep 60
snshlog F-receiver "$T0"
```

Teardown: `snshdrop`.

### 9.9 Batch G: endpoint forms and credentials (SNSH-21, SNSH-22)

About 10 minutes, no cost. The password `snspass` is a throwaway literal that protects nothing.

```sh
snshtopic g; T0=$(now_ms)
n=0
for e in http://127.0.0.1/ http://localhost/ http://10.0.0.1/ 'http://[::1]/' http://example.invalid/; do
  n=$((n + 1))
  rec "SNSH/G-subscribe-$n" aws sns subscribe --topic-arn "$TOPIC" --protocol http \
    --notification-endpoint "$e" --return-subscription-arn
done
rec SNSH/G-subscribe-auth aws sns subscribe --topic-arn "$TOPIC" --protocol https \
  --notification-endpoint "https://snsuser:snspass@${U#https://}auth/auto" --return-subscription-arn
SG=$(sed -n 's/^ *"SubscriptionArn": "\([^"]*\)".*/\1/p' "$REC/SNSH/G-subscribe-auth.out")
sleep 30
rec SNSH/G-publish aws sns publish --topic-arn "$TOPIC" --message m-g
sleep 20
rec SNSH/G-attrs-auth aws sns get-subscription-attributes --subscription-arn "$SG"
rec SNSH/G-list aws sns list-subscriptions-by-topic --topic-arn "$TOPIC"
snshlog G-receiver "$T0"
```

Read: whether the first request to `/auth/auto` already carries `authorization`, or only the retry after the 401. Teardown: `snshdrop`.

### 9.10 Batch H: delivery status logging (SNSH-23)

About 10 minutes, under USD 0.01. It adds the role `$P-snsh-logs` and the log groups under `sns/$R/$A/`.

```sh
aws iam create-role --role-name "$P-snsh-logs" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name "$P-snsh-logs" --policy-name logs --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents","logs:PutMetricFilter","logs:PutRetentionPolicy"],"Resource":"*"}]}'
sleep 10
snshtopic h
for a in HTTPSuccessFeedbackRoleArn HTTPFailureFeedbackRoleArn; do
  aws sns set-topic-attributes --topic-arn "$TOPIC" --attribute-name "$a" --attribute-value "arn:aws:iam::$A:role/$P-snsh-logs"
done
aws sns set-topic-attributes --topic-arn "$TOPIC" --attribute-name HTTPSuccessFeedbackSampleRate --attribute-value 100
snshsub H-subscribe-ok /auto/ok >/dev/null
snshsub H-subscribe-fail /auto/s500 "$(jq -nc '{DeliveryPolicy: ({healthyRetryPolicy: {numRetries: 1,
  minDelayTarget: 1, maxDelayTarget: 1}} | tojson)}')" >/dev/null
sleep 20
rec SNSH/H-publish aws sns publish --topic-arn "$TOPIC" --message m-h
sleep 120
rec SNSH/H-log-groups aws logs describe-log-groups --log-group-name-prefix "sns/$R/$A/$P-snsh-h"
for g in $(aws logs describe-log-groups --log-group-name-prefix "sns/$R/$A/$P-snsh-h" --query 'logGroups[].logGroupName' --output text); do
  rec "SNSH/H-events-$(basename "$g")" aws logs filter-log-events --log-group-name "$g"
done
```

Teardown: `snshdrop`, then the role and log groups in 9.13.

### 9.11 Batch I: refused and dropped connections (SNSH-24)

About 15 minutes, under USD 0.01. A Lambda function URL cannot refuse a connection, so this batch runs a `t4g.nano` in the default VPC whose user data confirms its subscriptions, answers 200 on ports 8080 and 8081 for 300 seconds, and then exits. After that, port 8080 refuses connections, and port 8081 drops them once its ingress rule is revoked. Without a default VPC, `run-instances` fails; skip the batch.

```sh
DPI=$(jq -nc --arg q "$DLQ" '{DeliveryPolicy: ({healthyRetryPolicy: {numRetries: 2, minDelayTarget: 5,
  maxDelayTarget: 5}} | tojson), RedrivePolicy: ({deadLetterTargetArn: $q} | tojson)}')
AMI=$(aws ssm get-parameter --name /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64 \
  --query Parameter.Value --output text)
SGI=$(aws ec2 create-security-group --group-name "$P-snsh-i" --description "SNSH-24 receiver" \
  --query GroupId --output text)
for port in 8080 8081; do
  aws ec2 authorize-security-group-ingress --group-id "$SGI" --protocol tcp --port "$port" --cidr 0.0.0.0/0
done
cat >snsh-i.py <<'EOF'
#!/usr/bin/python3
import json, os, re, threading, urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CONFIRM = re.compile(r"^https://sns\.[a-z0-9-]+\.amazonaws\.com/\?Action=ConfirmSubscription&")

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if self.headers.get("x-amz-sns-message-type") == "SubscriptionConfirmation":
            url = json.loads(body)["SubscribeURL"]
            if CONFIRM.match(url):
                urllib.request.urlopen(url, timeout=10).read()
        self.send_response(200)
        self.end_headers()

for port in (8080, 8081):
    threading.Thread(target=ThreadingHTTPServer(("", port), H).serve_forever, daemon=True).start()
threading.Event().wait(300)
os._exit(0)
EOF
IID=$(aws ec2 run-instances --image-id "$AMI" --instance-type t4g.nano --security-group-ids "$SGI" \
  --user-data file://snsh-i.py --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=$P-snsh-i}]" \
  --query 'Instances[0].InstanceId' --output text)
aws ec2 wait instance-running --instance-ids "$IID"
IP=$(aws ec2 describe-instances --instance-ids "$IID" \
  --query 'Reservations[0].Instances[0].PublicIpAddress' --output text)
until curl -s -o /dev/null --max-time 3 -X POST "http://$IP:8080/"; do sleep 5; done      # receiver up
snshtopic i-refused; TR=$TOPIC
rec SNSH/I-subscribe-refused aws sns subscribe --topic-arn "$TR" --protocol http \
  --notification-endpoint "http://$IP:8080/refused" --return-subscription-arn --attributes "$DPI"
snshtopic i-dropped; TD=$TOPIC
rec SNSH/I-subscribe-dropped aws sns subscribe --topic-arn "$TD" --protocol http \
  --notification-endpoint "http://$IP:8081/dropped" --return-subscription-arn --attributes "$DPI"
sleep 15
for s in refused dropped; do                # both must read PendingConfirmation "false"; stop here if not
  rec "SNSH/I-attrs-$s" aws sns get-subscription-attributes --subscription-arn \
    "$(sed -n 's/^ *"SubscriptionArn": "\([^"]*\)".*/\1/p' "$REC/SNSH/I-subscribe-$s.out")"
done
while curl -s -o /dev/null --max-time 3 -X POST "http://$IP:8080/"; do sleep 10; done    # receiver gone
aws ec2 revoke-security-group-ingress --group-id "$SGI" --protocol tcp --port 8081 --cidr 0.0.0.0/0
curl -s -o /dev/null --max-time 3 "http://$IP:8080/"; echo "8080 exit $? (7: refused)"
curl -s -o /dev/null --max-time 3 "http://$IP:8081/"; echo "8081 exit $? (28: dropped)"
TS=$(date +%s); echo "$TS" >"$REC/SNSH/I-publish-time.txt"
rec SNSH/I-publish-refused aws sns publish --topic-arn "$TR" --message m-i-refused
rec SNSH/I-publish-dropped aws sns publish --topic-arn "$TD" --message m-i-dropped
sleep 120
snshdlq I-dlq
sleep 240                                   # SNS metrics arrive a few minutes late
for s in refused dropped; do
  rec "SNSH/I-failed-$s" aws cloudwatch get-metric-statistics --namespace AWS/SNS \
    --metric-name NumberOfNotificationsFailed --dimensions "Name=TopicName,Value=$P-snsh-i-$s" \
    --start-time "$((TS - 600))" --end-time "$((TS + 900))" --period 60 --statistics Sum
done
```

Read per topic: a `NumberOfNotificationsFailed` sum of 3 means SNS retried (every HTTP attempt counts, 6.6), 1 means it did not. The DLQ message's `SentTimestamp` minus `I-publish-time.txt` agrees: 10 s or more after a retried delivery, about 0 s after a refused one that was not retried. Teardown:

```sh
aws ec2 terminate-instances --instance-ids "$IID" >/dev/null
aws ec2 wait instance-terminated --instance-ids "$IID"
aws ec2 delete-security-group --group-id "$SGI"
for TOPIC in "$TR" "$TD"; do snshdrop; done
aws sqs purge-queue --queue-url "$Q/$P-snsh-dlq"
rm -f snsh-i.py
```

### 9.12 Batch J: follow-ups to the 2026-11-03 run (SNSH-26 to SNSH-30)

About 60 minutes, under USD 0.05 (Lambda, CloudWatch Logs, SNS, SQS and a few hundred API Gateway HTTP API requests). Run each part in order; each ends with its own teardown, and the API Gateway part removes what it adds. Throughout, the receiver must not be throttled by the account's Lambda concurrency (10 on a new account), which muddied SNSH-05: no part has more than four deliveries in flight at once through the function, and the throttle burst bypasses Lambda.

**SNSH-26: request timeout.** About 7 minutes. SNSH-05's endpoint answered after 20 s and was counted delivered, against the documented "about 15 seconds". The function's 60-second timeout (9.2) lets it hold an answer for 25, 35 and 50 s.

```sh
aws sqs purge-queue --queue-url "$Q/$P-snsh-dlq"; sleep 60
DPJ=$(jq -nc --arg q "$DLQ" '{DeliveryPolicy: ({healthyRetryPolicy: {numRetries: 2, minDelayTarget: 5,
  maxDelayTarget: 5}} | tojson), RedrivePolicy: ({deadLetterTargetArn: $q} | tojson)}')
snshtopic j-timeout; T0=$(now_ms)
for p in sleep25 sleep35 sleep50; do snshsub "J-subscribe-$p" "/auto/$p" "$DPJ" >/dev/null; done
sleep 30
rec SNSH/J-publish-timeout aws sns publish --topic-arn "$TOPIC" --message m-j-timeout
sleep 300
snshlog J-receiver-timeout "$T0"
snshdlq J-dlq-timeout
snshdrop
```

Read per path: one Notification request means AWS waited that long and counted it delivered; three requests mean it gave up and retried. The function finishes its sleep and logs `slept` either way, so count the requests (`kind` `Notification`), not the `slept` lines. For a retried path, the gap between requests minus the 5-second delay is the timeout, and it shows whether the delay runs from the end of the failed attempt (6.3). Marks the timeout line of 3.1 recorded (AWS's limit between the last delivered and the first retried path), and the timeout case of 6.1 and 6.3.

**SNSH-28: which exhausted deliveries are redriven.** About 13 minutes. SNSH-05 redrove only 2 of the 5 exhausted subscriptions while 16 deliveries competed for 10 Lambda environments. This runs each path alone, on a topic of its own.

```sh
aws sqs purge-queue --queue-url "$Q/$P-snsh-dlq"; sleep 60
for p in s429 s500 s502 s503 raw/s500; do
  n=${p/\//-}; snshtopic "j-redrive-$n"; T0=$(now_ms)
  attrs=$DPJ; [ "$p" = raw/s500 ] && attrs=$(jq -c '. + {RawMessageDelivery: "true"}' <<<"$DPJ")
  snshsub "J-subscribe-redrive-$n" "/auto/$p" "$attrs" >/dev/null
  sleep 20
  rec "SNSH/J-publish-redrive-$n" aws sns publish --topic-arn "$TOPIC" --message "m-j-redrive-$n" \
    --message-attributes '{"k":{"DataType":"String","StringValue":"v"}}'
  sleep 60
  snshlog "J-receiver-redrive-$n" "$T0"
  snshdlq "J-dlq-redrive-$n"
  snshdrop
done
```

`snshdlq` does not delete, so each read also shows the earlier paths' dead letters; match each to its path by the message text `m-j-redrive-<path>`. Read per path: three requests in `J-receiver-redrive-<path>` (retried and exhausted), and whether a dead letter for it arrived. Five of five redriven means SNSH-05's gaps came from the throttled receiver, and marks the "2 of 5" line of 6.1 explained; fewer, with every attempt reaching the function, is a real AWS rule to describe. The `raw-s500` dead letter, if it arrives, is the raw DLQ form that 6.5 and `sns.http.dlq-body` leave UNVERIFIED: record its body and message attributes.

**SNSH-29: backoff curves with two more parameter sets.** About 22 minutes. SNSH-09 recorded one curve per function, from 2 s to 64 s over 8 retries; two more sets give each formula more points to be fitted to. Each set runs on a topic of its own, so at most four deliveries are in flight.

```sh
for set in '6 4 100' '4 10 30'; do
  read -r nr lo hi <<<"$set"
  snshtopic "j-backoff-$nr"
  for f in linear arithmetic geometric exponential; do
    snshsub "J-subscribe-backoff-$nr-$f" "/auto/b$nr-$f/s500" "$(jq -nc --arg f "$f" --argjson n "$nr" --argjson lo "$lo" --argjson hi "$hi" \
      '{DeliveryPolicy: ({healthyRetryPolicy: {numRetries: $n, minDelayTarget: $lo, maxDelayTarget: $hi, backoffFunction: $f}} | tojson)}')" >/dev/null
  done
  sleep 30; T0=$(now_ms)
  rec "SNSH/J-publish-backoff-$nr" aws sns publish --topic-arn "$TOPIC" --message "m-j-backoff-$nr"
  sleep 600
  snshlog "J-receiver-backoff-$nr" "$T0"
  snshdrop
done
```

Read per path the gaps between requests, each expected about 1 s over its target as in SNSH-09. With SNSH-09's curve they give three parameter sets per function: fit one formula per function to all three and check it against each, or record that none fits. A fitted formula marks the "exact formulas ... are not derived" line of 6.3 recorded and lets `sns.http.backoff-curves` drop `linear-for-all` for that function; a formula someone fitted to SNSH-09 alone is confirmed or corrected here.

**SNSH-27 and SNSH-30: an API Gateway receiver.** About 18 minutes. An HTTP API in front of the same function (payload 2.0, which passes the `authorization` header to it) serves the Basic-auth case, and a route that sends each request straight to an SQS queue serves the throttle burst, so its 60 deliveries never meet the Lambda concurrency limit and each one's arrival is the message's `SentTimestamp`.

```sh
aws sqs create-queue --queue-name "$P-snsh-thr" --attributes VisibilityTimeout=900 >/dev/null
THRQ="arn:aws:sqs:$R:$A:$P-snsh-thr"
aws iam create-role --role-name "$P-snsh-apigw" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"apigateway.amazonaws.com"},"Action":"sts:AssumeRole"}]}' >/dev/null
aws iam put-role-policy --role-name "$P-snsh-apigw" --policy-name send --policy-document \
  "$(jq -nc --arg q "$THRQ" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:"sqs:SendMessage",Resource:$q}]}')"
sleep 15
API=$(aws apigatewayv2 create-api --name "$P-snsh" --protocol-type HTTP --query ApiId --output text)
IQ=$(aws apigatewayv2 create-integration --api-id "$API" --integration-type AWS_PROXY --integration-subtype SQS-SendMessage \
  --payload-format-version 1.0 --credentials-arn "arn:aws:iam::$A:role/$P-snsh-apigw" \
  --request-parameters "$(jq -nc --arg q "$Q/$P-snsh-thr" '{QueueUrl: $q, MessageBody: "$request.body"}')" \
  --query IntegrationId --output text)
IL=$(aws apigatewayv2 create-integration --api-id "$API" --integration-type AWS_PROXY --payload-format-version 2.0 \
  --integration-uri "arn:aws:lambda:$R:$A:function:$P-snsh" --query IntegrationId --output text)
aws apigatewayv2 create-route --api-id "$API" --route-key 'POST /thr' --target "integrations/$IQ" >/dev/null
aws apigatewayv2 create-route --api-id "$API" --route-key 'ANY /{proxy+}' --target "integrations/$IL" >/dev/null
aws apigatewayv2 create-stage --api-id "$API" --stage-name '$default' --auto-deploy >/dev/null
aws lambda add-permission --function-name "$P-snsh" --statement-id apigw --action lambda:InvokeFunction \
  --principal apigateway.amazonaws.com --source-arn "arn:aws:execute-api:$R:$A:$API/*"
GW="https://$API.execute-api.$R.amazonaws.com/"
sleep 10

# SNSH-30, probe: which receiver keeps the header (the handler answers 401 under /auth/ without one)
T0=$(now_ms)
curl -sS -o /dev/null -w 'gateway %{http_code}\n' -u probe:probe -X POST "${GW}probe/auth/gw" | tee "$REC/SNSH/J-probe.txt"
curl -sS -o /dev/null -w 'function-url %{http_code}\n' -u probe:probe -X POST "${U}probe/auth/url" | tee -a "$REC/SNSH/J-probe.txt"
snshlog J-receiver-probe "$T0"

# SNSH-30: Basic auth from the endpoint URL, through the gateway
snshtopic j-auth; T0=$(now_ms)
rec SNSH/J-subscribe-auth aws sns subscribe --topic-arn "$TOPIC" --protocol https \
  --notification-endpoint "https://snsuser:snspass@${GW#https://}auth/auto" --return-subscription-arn
sleep 30
rec SNSH/J-publish-auth aws sns publish --topic-arn "$TOPIC" --message m-j-auth
sleep 20
snshlog J-receiver-auth "$T0"
snshdrop

# SNSH-27: 60 messages at maxReceivesPerSecond 1, arrivals timed by SQS
snshtopic j-thr
rec SNSH/J-subscribe-thr aws sns subscribe --topic-arn "$TOPIC" --protocol https --notification-endpoint "${GW}thr" \
  --attributes "$(jq -nc '{DeliveryPolicy: ({throttlePolicy: {maxReceivesPerSecond: 1}} | tojson)}')" --return-subscription-arn
sleep 20
TOK=$(aws sqs receive-message --queue-url "$Q/$P-snsh-thr" --wait-time-seconds 10 --query 'Messages[0].Body' --output text | jq -r .Token)
rec SNSH/J-confirm-thr aws sns confirm-subscription --topic-arn "$TOPIC" --token "$TOK"
aws sqs purge-queue --queue-url "$Q/$P-snsh-thr"; sleep 60
date +%s%3N >"$REC/SNSH/J-thr-publish-ms.txt"
for b in 1 2 3 4 5 6; do
  aws sns publish-batch --topic-arn "$TOPIC" --publish-batch-request-entries \
    "$(jq -nc --arg b "$b" '[range(10) | {Id: "e\(.)", Message: "m-j-thr-\($b)-\(.)"}]')" >/dev/null
done
sleep 300
for i in 1 2 3 4 5 6 7 8; do
  rec "SNSH/J-thr-recv-$i" aws sqs receive-message --queue-url "$Q/$P-snsh-thr" --max-number-of-messages 10 \
    --wait-time-seconds 5 --attribute-names SentTimestamp
done
snshdrop
```

Read for SNSH-30: `J-probe.txt` and `J-receiver-probe` first. A gateway 200 with an `authorization` header logged shows this receiver keeps it; a function-URL 401 shows the function URL strips it, which explains SNSH-22, and a function-URL 200 shows it does not, so SNSH-22's result stands as AWS's. Then in `J-receiver-auth`, whether the first `/auth/auto` request already carries `authorization` (preemptive), only the request after the 401 does (challenge), or none does. Marks the Basic-auth line of 3.1 recorded and settles `sns.http.basic-auth`. If the gateway itself drops the header, stop: the run settles nothing beyond SNSH-22, and the notes say so.

Read for SNSH-27: the 60 `SentTimestamp` values against `J-thr-publish-ms.txt` (`date +%s%3N` needs GNU date; on macOS use `gdate` or `python3 -c 'import time; print(int(time.time()*1000))'`). About one per second over a minute means AWS paces at the average; all within a few seconds means it does not pace a burst this size either; a burst followed by about one per second shows the averaging window's length. Marks the pacing line of 6.4 recorded. Fewer than 60 messages means some deliveries are still queued or failed: receive again before teardown.

Teardown:

```sh
aws apigatewayv2 delete-api --api-id "$API"
aws lambda remove-permission --function-name "$P-snsh" --statement-id apigw
aws sqs delete-queue --queue-url "$Q/$P-snsh-thr"
aws iam delete-role-policy --role-name "$P-snsh-apigw" --policy-name send
aws iam delete-role --role-name "$P-snsh-apigw"
aws sqs purge-queue --queue-url "$Q/$P-snsh-dlq"
```

### 9.13 Teardown

The recording kit's section 5 sweep does not list `sns/` log groups or this queue's EC2 instance, so delete them here:

```sh
for t in $(aws sns list-topics --query "Topics[?contains(TopicArn, ':$P-snsh-')].TopicArn" --output text); do
  TOPIC=$t; snshdrop
done
for i in $(aws ec2 describe-instances --filters "Name=tag:Name,Values=$P-snsh-i" \
    "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' --output text); do
  aws ec2 terminate-instances --instance-ids "$i" >/dev/null
  aws ec2 wait instance-terminated --instance-ids "$i"
done
aws ec2 delete-security-group --group-name "$P-snsh-i" 2>/dev/null || true
for a in $(aws apigatewayv2 get-apis --query "Items[?Name=='$P-snsh'].ApiId" --output text); do aws apigatewayv2 delete-api --api-id "$a"; done
aws sqs delete-queue --queue-url "$Q/$P-snsh-thr" 2>/dev/null
aws iam delete-role-policy --role-name "$P-snsh-apigw" --policy-name send 2>/dev/null
aws iam delete-role --role-name "$P-snsh-apigw" 2>/dev/null
aws sqs delete-queue --queue-url "$Q/$P-snsh-dlq"
aws lambda delete-function-url-config --function-name "$P-snsh"
aws lambda delete-function --function-name "$P-snsh"
for g in "$LG" $(aws logs describe-log-groups --log-group-name-prefix "sns/$R/$A/$P-snsh" \
    --query 'logGroups[].logGroupName' --output text); do
  aws logs delete-log-group --log-group-name "$g"
done
aws iam detach-role-policy --role-name "$P-snsh-role" \
  --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
aws iam delete-role --role-name "$P-snsh-role"
aws iam delete-role-policy --role-name "$P-snsh-logs" --policy-name logs
aws iam delete-role --role-name "$P-snsh-logs"
rm -f snsh.py snsh.zip
```

Keep `conf.json`, `note.json`, `note-v2.json`, `unsub.json` and `A-verify.txt` in `$REC/SNSH` with the captures. None of them carries a credential.
