# Amazon SES (API v1 and v2): primary-source research

Scope: what roadmap item 9 needs for SES "in a journal-only form for notification paths": the send operations of both SES APIs, the identity operations and verification states, the sandbox rules as they apply to each send, the mailbox simulator, quotas, stored templates, the scope of configuration sets, the Terraform resources that create sender identities, and how an emulator that never sends mail should complete verification and expose sent messages to tests. Sources, all retrieved 2026-10-25: the Smithy models at the `aws/api-models-aws` pin, the SES Developer Guide and the SES API References v1 and v2 (docs.aws.amazon.com, Markdown versions), LocalStack's AWS-validated SES snapshots, `hashicorp/terraform-provider-aws` v6.68.0, `aws-sdk-go-v2`, boto (v2) SDK source, the AWS SDK code examples, and third-party captures of real SES output where named. Nothing here was run against AWS.

## 1. Sources and evidence labels

| Label | Meaning |
|---|---|
| model | Smithy JSON AST at commit `1cfdfb4bb9ea858e65623e66d73af04bec353201`: [ses-2010-12-01.json][m1] (v1) and [sesv2-2019-09-27.json][m2] (v2). Shapes are cited as Smithy ids, such as `com.amazonaws.ses#SendEmailRequest`. Both models are vendored at this commit under `third_party/api-models-aws/models/ses/` and `.../sesv2/`. |
| aws-doc | AWS documentation on docs.aws.amazon.com or aws.amazon.com, linked per claim. |
| third-party-recorded | LocalStack snapshot entry recorded against real AWS, counted only when [test_ses.validation.json][lsv] gives the test a `last_validated_date` (localstack@8b9a79f, Apache-2.0). Of the 33 entries in [test_ses.snapshot.json][lss], 23 are validated; the 10 `SetIdentityHeadersInNotificationsEnabled` entries are not, and this note does not rely on them. Three validated tests carry no snapshot and only assert an error code (`test_special_tags_send_email` x2, `test_send_email_raises_message_rejected`). |
| provider | Terraform provider source or acceptance test at tag v6.68.0. The acceptance tests run against real AWS, so an asserted attribute value is evidence of what AWS returned, in Terraform's flattened form. |
| sdk-source | AWS SDK or AWS code-example source at a pinned commit. |
| capture | Real SES output captured by an unrelated project (email corpora, test fixtures copied from responses). Weaker than third-party-recorded: provenance is the project's, not stated as a recording. |
| inferred | Reasoned from the above. |

UNVERIFIED marks behavior no source fixes; each names its recording case in section 12. LocalStack and moto implementation code are references only; neither is evidence of AWS behavior.

## 2. Which API: v1, v2 or both

### 2.1 Service identity from the models

| | v1 | v2 |
|---|---|---|
| Service shape | `com.amazonaws.ses#SimpleEmailService` | `com.amazonaws.sesv2#SimpleEmailService_v2` |
| Protocol | `aws.protocols#awsQuery`, Version `2010-12-01`, `smithy.api#xmlNamespace` `http://ses.amazonaws.com/doc/2010-12-01/` | `aws.protocols#restJson1` |
| `aws.auth#sigv4` name | `ses` ([m1 L6495][m1-sig]) | `ses` ([m2 L13539][m2-sig]) |
| `aws.api#service` | sdkId `SES`, endpointPrefix `email`, arnNamespace `ses`, cloudTrailEventSource `ses.amazonaws.com` | sdkId `SESv2`, endpointPrefix `email`, arnNamespace `ses`, cloudTrailEventSource `sesv2.amazonaws.com` |
| Operations | 71 | 116 |
| Regional endpoint | `https://email.{Region}.{dnsSuffix}` (FIPS `email-fips.`) | the same hosts |

Both APIs sign with the same name and resolve to the same host, so one emulator endpoint receives both; they differ in protocol and operation set (model). The v2 endpoint rules add one path the v1 rules lack: when an operation binds the `EndpointId` context parameter (only `SendEmail` and `SendBulkEmail` carry it), the resolved endpoint is `https://{EndpointId}.endpoints.email.{dnsSuffix}` with auth scheme `sigv4a`, signingName `ses`, signingRegionSet `["*"]` (model, `smithy.rules#endpointBdd` of `SimpleEmailService_v2`). The result list also holds a custom-`Endpoint` result carrying the same `sigv4a` scheme, beside the plain custom-`Endpoint` result, so a client that sets both `Endpoint` and `EndpointId` signs SigV4A (inferred from the result list; the BDD nodes were not decoded).

Neither model carries `aws.iam` traits. The service reference settles the prefix: both SDKs' operations are listed under prefix `ses` (`third_party/aws-service-reference/actions.json`, retrieved 2026-10-26; `SendEmail` lists sdk `ses` and `sesv2`). `SendEmail` and `SendRawEmail` have empty `authorized` rows there, so their primary actions `ses:SendEmail` and `ses:SendRawEmail` come from the same-name rule (`primaryFirst` in `internal/codegen/iam.go`; smithy-models.md 8).

### 2.2 What clients call

- AWS's own JavaScript v3 examples use the v1 client for sending and identities: `SendEmailCommand` from `@aws-sdk/client-ses` with `Source`, `Destination.ToAddresses`, `Message.Subject`/`Body.Text`/`Body.Html` and `Charset: "UTF-8"`, and separate examples for `VerifyEmailIdentity`, `VerifyDomainIdentity`, `ListIdentities`, `DeleteIdentity`, templates and `SendTemplatedEmail` (sdk-source, [ses_sendemail.js L17-L64][ex-js], [directory listing][ex-js-dir]). The attachment example notes that "nodemailer wraps the SES SDK and calls SendRawEmail" ([send-with-attachments.js L7-L14][ex-js-att]); what current nodemailer releases call was not checked. The Python examples ship both `ses` and `sesv2` directories ([python/example_code][ex-py]).
- The Developer Guide's raw-sending page is "Sending raw email using the Amazon SES API v2" and uses v2 `SendEmail` with `Content.Raw` ([send-email-raw][d-raw]). Message size is 10 MB on v1 and 40 MB on v2 or SMTP ([quotas][d-quotas]).
- Terraform's `aws_ses_*` resources use the v1 client and `aws_sesv2_*` resources the v2 client (provider, section 9).
- v2 `SendEmail` takes one of `Content.Simple`, `Content.Raw` or `Content.Template` (model, `com.amazonaws.sesv2#EmailContent`).

### 2.3 Decision

Implement both, over one state model. AWS's own examples send with v1 `SendEmail` and its guide sends raw mail with v2 `SendEmail`, and Terraform creates identities through either API (2.2), so a stack that verifies its sender with one API (`aws_ses_email_identity`, v1) and sends with the other (a Lambda on the v2 SDK) is to be expected (inferred). Terraform derives the same ARN `arn:aws:ses:<region>:<account>:identity/<name>` for the v1 and the v2 identity resource ([v1 L146-L148][tf-ei-arn], [v2 L358-L360][tf-v2-arn]), which is consistent with one identity store behind both APIs. UNVERIFIED: that an identity, template or configuration set created through one API is visible through the other (plausible: shared, as the shared ARNs suggest; alternative: separate stores) (SES-12).

## 3. Wire protocols

### 3.1 v1: awsQuery

- `POST /` with `Content-Type: application/x-www-form-urlencoded`, `Action=<Op>&Version=2010-12-01`; lists as `Name.member.N`, maps as `Name.entry.N.key`/`.value`, nested members dotted ([Smithy awsQuery](https://smithy.io/2.0/aws/protocols/aws-query-protocol.html)). The model carries no `xmlName` or `xmlFlattened` trait anywhere (model, searched), so every list and map uses the default names: `Destination.ToAddresses.member.1`, `Tags.member.1.Name`, `Identities.member.1`, and `GetIdentityVerificationAttributes` answers `<VerificationAttributes><entry><key>..</key><value>..</value></entry></VerificationAttributes>`.
- Blobs travel base64: `RawMessage.Data` "needs to base64-encoded if you are accessing Amazon SES directly through the HTTPS interface. If you are accessing Amazon SES using an Amazon Web Services SDK, the SDK takes care of the base 64-encoding for you" (model, `com.amazonaws.ses#RawMessage`).
- Success: `<{Op}Response xmlns="http://ses.amazonaws.com/doc/2010-12-01/"><{Op}Result>..</{Op}Result><ResponseMetadata><RequestId>..</RequestId></ResponseMetadata></{Op}Response>` (Smithy awsQuery, model namespace).
- Error envelope: a fixture copied from an SES response shows `<ErrorResponse xmlns="http://ses.amazonaws.com/doc/2010-12-01/">`, then `<Error><Type>Sender</Type><Code>MessageRejected</Code><Message>..</Message></Error>`, then `<RequestId>`, with no `<Detail/>`, status 400 (capture, [ymirapp/wordpress-plugin SesClientTest.php L100][c-ymir]). That matches the namespace-on-envelope pattern recorded for SNS, IAM, STS and CloudWatch ([error-matrix.md](error-matrix.md) section 3). UNVERIFIED: the exact bytes, `xmlns` and absence of `Detail` for SES (SES-01), and what an unknown `Action` returns (SNS answers with the `AWSFault/2005-15-09` namespace; SES unknown) (SES-24).

### 3.2 v2: restJson1

- Paths under `/v2/email/`, JSON bodies, `httpLabel` members in the path (model). The operations this note needs:

| Operation | Method and path (pinned model) |
|---|---|
| `SendEmail` | `POST /v2/email/outbound-emails` |
| `CreateEmailIdentity` | `POST /v2/email/identities` |
| `GetEmailIdentity` | `GET /v2/email/identities/{EmailIdentity}` |
| `ListEmailIdentities` | `POST /v2/email/list-identities` |
| `DeleteEmailIdentity` | `DELETE /v2/email/identities/{EmailIdentity}` |
| `GetAccount` | `GET /v2/email/account` |
| `CreateEmailTemplate` | `POST /v2/email/templates` |
| `GetEmailTemplate` | `GET /v2/email/templates/{TemplateName}` |
| `ListConfigurationSets` | `POST /v2/email/list-configuration-sets` |
| `TagResource`, `UntagResource`, `ListTagsForResource` | `POST /v2/email/tags`, `DELETE /v2/email/tags`, `GET /v2/email/tags` with `ResourceArn` |

- Two bindings changed on 2026-09-29, in the last commit to touch the sesv2 model before the pin (the pin, dated 2026-10-06, is seven commits later): `ListEmailIdentities` moved from `GET /v2/email/identities` (with `NextToken` and `PageSize` as `httpQuery`) to `POST /v2/email/list-identities` with a new `Filter` member, and `ListConfigurationSets` from `GET /v2/email/configuration-sets` to `POST /v2/email/list-configuration-sets` (model history: [a877f97][m2-old] has the GET forms, [430b75c][m2-new] the POST forms; no other binding differs between the two). aws-sdk-go-v2 `service/sesv2` v1.77.0 (2026-09-29) is the first Go release with the POST form, "Added Filter support for ListTenants, ListEmailIdentities, and ListConfigurationSets APIs" ([CHANGELOG][go-cl], [schemas.go L459-L464][go-schema]); v1.76.0 still binds `GET /v2/email/identities` ([v1.76.0 schemas.go L459-L463][go-schema-76]). SDK and CLI releases generated from a model older than that commit send the GET form (inferred). Terraform v6.68.0 pins sesv2 v1.77.0 ([go.mod L262][tf-gomod]) but never calls `ListEmailIdentities` (no match in the provider at that tag); only its sesv2 sweeper calls v2 `ListConfigurationSets` ([sweep.go L26-L29][tf-v2-sweep]).
- Errors: the restJson1 protocol puts the error shape name in `x-amzn-ErrorType` and the members in the body ([Smithy restJson1](https://smithy.io/2.0/aws/protocols/aws-restjson1-protocol.html)); every SES v2 error shape has a single member `message` (model). UNVERIFIED: the exact header value and body bytes SES v2 sends (`{"message":"..."}` plausible) (SES-02).
- An unknown v2 path is answered by AWS in some 404 form; UNVERIFIED which (SES-24).

### 3.3 Error tables

v1 errors in this note's scope (model; `awsQueryError` code and status; every shape is `client`):

| Shape | Wire Code | HTTP | Members |
|---|---|---|---|
| `MessageRejected` | `MessageRejected` | 400 | message |
| `MailFromDomainNotVerifiedException` | `MailFromDomainNotVerifiedException` | 400 | message |
| `AccountSendingPausedException` | `AccountSendingPausedException` | 400 | message |
| `ConfigurationSetDoesNotExistException` | `ConfigurationSetDoesNotExist` | 400 | ConfigurationSetName, message |
| `ConfigurationSetSendingPausedException` | `ConfigurationSetSendingPausedException` | 400 | ConfigurationSetName, message |
| `TemplateDoesNotExistException` | `TemplateDoesNotExist` | 400 | TemplateName, message |
| `AlreadyExistsException` | `AlreadyExists` | 400 | Name, message |
| `InvalidTemplateException` | `InvalidTemplate` | 400 | TemplateName, message |
| `LimitExceededException` | `LimitExceeded` | 400 | message |
| `ConfigurationSetAlreadyExistsException` | `ConfigurationSetAlreadyExists` | 400 | ConfigurationSetName, message |
| `InvalidConfigurationSetException` | `InvalidConfigurationSet` | 400 | message |

The send operations declare `AccountSendingPausedException`, `ConfigurationSetDoesNotExistException`, `ConfigurationSetSendingPausedException`, `MailFromDomainNotVerifiedException` and `MessageRejected`; `SendTemplatedEmail` adds `TemplateDoesNotExistException`. The identity operations declare no errors at all (model). Unmodeled errors come from the common set: `InvalidParameterValue` 400, `MissingParameter` 400, `ValidationError` 400, `ThrottlingException` 400, `MalformedQueryString` 404 and others ([CommonErrors v1][d-ce1]). Sending-rate throttling arrives as Code `Throttling` with message `Maximum sending rate exceeded.` (aws-doc, [AWS blog, 2013-02-01][d-blog]); its status is UNVERIFIED (SES-21).

v2 errors (model; `smithy.api#httpError`):

| Shape | HTTP | Fault |
|---|---|---|
| `AccountSuspendedException` | 400 | client |
| `AlreadyExistsException` | 400 | client |
| `BadRequestException` | 400 | client |
| `ConcurrentModificationException` | 500 | server |
| `ConflictException` | 409 | client |
| `InternalServiceErrorException` | 500 | server |
| `InvalidNextTokenException` | 400 | client |
| `LimitExceededException` | 400 | client |
| `MailFromDomainNotVerifiedException` | 400 | client |
| `MessageRejected` | 400 | client |
| `NotFoundException` | 404 | client |
| `SendingPausedException` | 400 | client |
| `TooManyRequestsException` | 429 | client |

v2 `SendEmail` declares `AccountSuspendedException`, `BadRequestException`, `LimitExceededException`, `MailFromDomainNotVerifiedException`, `MessageRejected`, `NotFoundException`, `SendingPausedException`, `TooManyRequestsException` ([API_SendEmail v2][d2-send]).

### 3.4 One signing name, two services: what understudy must change

- Settled by [ADR 0019](../adr/0019-shared-signing-names.md): `registry.Registry.BySigningName` returns every service of a name in registration order, and an awsQuery service and a REST service with a route table share a name with no special rule, since they have no protocol family in common.
- Edge rule 7 claims form-encoded requests by `(Action, Version)` and confirms the signing name ([ARCHITECTURE.md](../ARCHITECTURE.md) 4.1 rule 7), so `ses` claims v1 by `(Action, 2010-12-01)`. Rule 8 tries the route tables of the name's REST services in registration order (ADR 0019), so a REST request signed for `ses` goes to `sesv2`'s table, and a request it does not match is `sesv2`'s unknown operation.
- SigV4A is refused at parse with "SigV4A (AWS4-ECDSA-P256-SHA256) is not supported" ([internal/auth/parse.go L243](../../internal/auth/parse.go)), so a v2 `SendEmail` carrying `EndpointId` (section 2.1) fails before reaching SES.

## 4. Send operations

### 4.1 v1 SendEmail

- Input `com.amazonaws.ses#SendEmailRequest`: `Source` (required, `Address`), `Destination` (required: `ToAddresses`, `CcAddresses`, `BccAddresses`, each a list of `Address`), `Message` (required: `Subject` `Content` required, `Body` required with optional `Text` and `Html`, each `Content` = `Data` required + optional `Charset`), `ReplyToAddresses`, `ReturnPath`, `SourceArn`, `ReturnPathArn`, `Tags` (`MessageTag` list of `Name`/`Value`, both required), `ConfigurationSetName`. Output `MessageId` (required). No length or pattern traits on any of these strings (model).
- Rules (model documentation of `SendEmail`): the sender must be "a verified email address or domain", otherwise "an 'Email address not verified' error"; in the sandbox only verified recipients or mailbox simulator addresses; maximum message size 10 MB; at least one recipient across To, CC and BCC; "If a recipient email address is invalid (that is, it is not in the format UserName@[SubDomain.]Domain.TopLevelDomain), the entire message is rejected, even if the message contains other recipients that are valid"; at most 50 recipients; every recipient counts against the 24-hour quota.
- `Source`, `ReturnPath` and the destination addresses must be 7-bit ASCII, domains in Punycode, no Punycode in the local part or friendly name; a Unicode friendly name uses MIME encoded-word (model, `SendEmailRequest.Source`, `Destination`). `ReturnPath` must itself be verified (model, `SendEmailRequest.ReturnPath`).
- UNVERIFIED: the answer to a `Body` with neither `Text` nor `Html` (SES-05).

### 4.2 v1 SendRawEmail

- Input `com.amazonaws.ses#SendRawEmailRequest`: `RawMessage.Data` (required, blob), optional `Source`, `Destinations` (list of `Address`), `FromArn`, `SourceArn`, `ReturnPathArn`, `Tags`, `ConfigurationSetName`. Without `Source`, "you must specify a 'From' address in the raw text of the message"; a given `Source` takes precedence over a `Return-Path` header for feedback (model).
- The raw message must have a header and body separated by a blank line, all required header fields, well-formed MIME parts, supported attachment types, lines of at most 1,000 characters (RFC 5321) (model, `SendRawEmailRequest.RawMessage`); at most 500 MIME parts ([send-email-raw][d-raw]). SES "automatically applies its own Message-ID and Date headers", overwriting any given; the headers `X-SES-SOURCE-ARN`, `X-SES-FROM-ARN`, `X-SES-RETURN-PATH-ARN` stand in for the ARN parameters and are removed before sending (model, `SendRawEmail`). `X-SES-CONFIGURATION-SET` and `X-SES-MESSAGE-TAGS: name1=value1, name2=value2` carry the configuration set and tags ([event-publishing-send-email][d-xses]).
- The addresses in `Source` and `Destinations` "define the envelope sender and recipients, respectively" ([send-email-raw][d-raw]). That sentence says "the Amazon SES API v2 `SendEmail` operation", whose model has no `Source` or `Destinations` member; the names are v1 `SendRawEmail`'s, so this note reads it as a statement about v1 (inferred). UNVERIFIED: which recipients the sandbox check uses when `Destinations` is empty (the `To`, `Cc`, `Bcc` headers, plausibly) and when both are present (plausibly `Destinations` alone) (SES-20); the error when neither `Source` nor a `From` header is present (SES-20). Recorded: `SendRawEmail` with `Destinations=["invalid@example.com"]` and a raw `From: origin@example.com` (unverified) fails with Code `MessageRejected` (third-party-recorded 2025-09-11, [test_ses.py L1162-L1170][ls-raw-test], [validation L89-L97][ls-raw-val]; message text not captured).

### 4.3 v1 SendTemplatedEmail

- Input `com.amazonaws.ses#SendTemplatedEmailRequest`: `Source`, `Destination`, `Template` (template name) and `TemplateData` (string, length 0 to 262,144) required; optional `TemplateArn`, `ReplyToAddresses`, `ReturnPath`, `SourceArn`, `ReturnPathArn`, `Tags`, `ConfigurationSetName` (model).
- "If your call to the SendTemplatedEmail operation includes all of the required parameters, Amazon SES accepts it and returns a Message ID. However, if Amazon SES can't render the email because the template contains errors, it doesn't send the email. Additionally, because it already accepted the message, Amazon SES doesn't return a message stating that it was unable to send the email" (model, `SendTemplatedEmail`). Rendering failures surface only as Rendering Failure events ([send-personalized-email-api][d-tmpl]).

### 4.4 v2 SendEmail

- Input `com.amazonaws.sesv2#SendEmailRequest`: `Content` (required) and, all optional in the model, `FromEmailAddress`, `FromEmailAddressIdentityArn`, `Destination`, `ReplyToAddresses`, `FeedbackForwardingEmailAddress` (+`IdentityArn`), `EmailTags`, `ConfigurationSetName`, `EndpointId`, `TenantName` (length min 1), `ListManagementOptions` (`ContactListName` required, `TopicName`), `ConfigurationOverrides.Tracking`. Output `MessageId` (`OutboundMessageId`, not marked required) (model).
- `Content` (`EmailContent`) members `Simple`, `Raw`, `Template`, none required in the model. `Simple` (`Message`): `Subject` and `Body` required, `Headers` (0 to 15 `MessageHeader`; `Name` 1 to 126 characters matching `^[!-9;-@A-~]+$`, `Value` 1 to 995 matching `^[ -~]*$`), `Attachments` (`RawContent` blob and `FileName` 0 to 255 required; `ContentType` 1 to 78; `ContentDisposition`, `ContentDescription`, `ContentId`, `ContentTransferEncoding`). `Template`: `TemplateName` (length min 1), `TemplateArn`, `TemplateContent` (inline `Subject`/`Text`/`Html`), `TemplateData` (0 to 262,144), `Headers`, `Attachments`. Inline template content supports "only simple substitions" (model).
- `FromEmailAddress` "has to be verified"; `FromEmailAddressIdentityArn` overrides the raw `X-SES-SOURCE-ARN` and `X-SES-FROM-ARN` headers; `TenantName` makes the send succeed "only if all referenced resources (identities, configuration sets, and templates) are associated with this tenant" (model).
- The response member may be returned for a message SES never sends: "It's possible for Amazon SES to accept a message without sending it", for example a virus attachment or invalid personalization content (model, `SendEmailResponse.MessageId`).
- UNVERIFIED: the error when `Content` holds none or more than one of `Simple`/`Raw`/`Template`, and when a `Simple` send has no `FromEmailAddress` (SES-23).

### 4.5 Message tags

- Model documentation (v1 `com.amazonaws.ses#MessageTag`, v2 `com.amazonaws.sesv2#MessageTag`): name and value "Contain only ASCII letters (a-z, A-Z), numbers (0-9), underscores (_), or dashes (-)" and "Contain 256 characters or fewer".
- Recorded texts, all Code `InvalidParameterValue`, Type `Sender`, status 400, from v1 `SendEmail` (third-party-recorded 2024-07-30, [snapshot L592-L735][ls-tags], [validation L35-L61][ls-tags-val]). The doubled space after `'_',` is in every recording:

| Tag | Message |
|---|---|
| name `test_invalid_name:123`, value `test` | `Invalid tag name <test_invalid_name:123>: only alphanumeric ASCII characters, '_',  '-' are allowed.` |
| name `test`, value `test_invalid_value:123` | `Invalid tag value <test_invalid_value:123>: only alphanumeric ASCII characters, '_',  '-' , '.', '@' are allowed.` |
| both invalid | the name message |
| name of 2,100 valid characters | `Tag name cannot exceed 255 characters.` |
| value of 2,200 valid characters | `Tag value cannot exceed 255 characters.` |
| name `test_invalid_name@123`, value of 2,200 characters | the name charset message (name checked before value) |
| name `""`, value `""` | `The tag name must be specified.` |
| name `""`, value `test` | `The tag name must be specified.` |
| name `test`, value `""` | `The tag value must be specified.` |

- The value message admits `.` and `@`, which the documentation does not. Names `ses:feedback-id-a` and `ses:feedback-id-b` pass validation (the call then fails `MessageRejected` on the unverified sender) (third-party-recorded 2024-07-30, [test_ses.py L925-L953][ls-special], [validation L80-L85][ls-tags-val]); these are the documented "up to two" `ses:feedback-id-<a or b>` tags ([monitor-using-event-publishing][d-tags]). The auto-tags `ses:caller-identity`, `ses:configuration-set`, `ses:from-domain`, `ses:outgoing-ip`, `ses:source-ip`, `ses:source-tls-version`, `ses:outgoing-tls-version` are SES-applied (same page). The recorded `Send` event also carries `ses:operation` (`SendEmail`), which that list omits (third-party-recorded 2023-08-25, [snapshot L66-L85][ls-event]).
- UNVERIFIED: whether 255 or 256 characters is the boundary for names and values (the recordings used 2,100 and 2,200), and whether other `ses:`-prefixed names are accepted (LocalStack strips any `ses:` prefix before the check; plausible on AWS: only the two feedback-id names) (SES-16).
- The recordings used an unverified `Source` (`user-<uid>@example.com`) and recipient `success@example.com` ([test_ses.py L886-L923][ls-tags-test]): tag validation runs before the identity check.

### 4.6 Address validation

- Documented: the `UserName@[SubDomain.]Domain.TopLevelDomain` rule, whole-message rejection on one invalid recipient (4.1), 7-bit ASCII only, Punycode domains, no SMTPUTF8 (model). Documented error categories without codes: "There are no recipients specified", "There are non-ASCII characters in the email address" ([troubleshoot-error-messages][d-err]).
- Historical wording the boto (v2) SDK matched in SES error bodies, codes not stated: `Address blacklisted.`, `Email address is not verified.`, `Daily message quota exceeded.`, `Maximum sending rate exceeded.`, `Domain ends with dot.`, `Local address contains control or whitespace`, `Illegal address`, `Identity.*is not verified`, `ownership not confirmed` (sdk-source, [boto/ses/connection.py L130-L170 at 5506aba][boto2]). `Domain ends with dot.` and `Missing final '@domain'` are also Jakarta Mail `InternetAddress` validation messages ([InternetAddress.java](https://github.com/jakartaee/mail-api/blob/master/api/src/main/java/jakarta/mail/internet/InternetAddress.java)), which suggests SES validates with that parser (inferred).
- UNVERIFIED: today's Code and Message for an address without `@`, with whitespace, with `..` in the domain, with a trailing dot, an empty `Source`, and for v2 (SES-04); no recipients (SES-05); 51 recipients (SES-06).

### 4.7 MessageId

- Documented examples: `EXAMPLE78603177f-7a5433e7-8edb-42ae-af10-f0181f34d6ee-000000` (v1 `SendEmail`), `EXAMPLEf3f73d99b-c63fb06f-d263-41f8-a0fb-d0dc67d56c07-000000` (v1 `SendRawEmail`) (model examples), `0000014644fe5ef6-9a483358-9170-4cb4-a269-f5dcdf415321-000000` ([notification-examples][d-notif]). The shape is 16 hex characters, `-`, a UUID, `-000000`: 60 characters.
- Real IDs captured by third parties decode as `01` + a 2-hex code + 12 hex characters of Unix epoch milliseconds at acceptance, then a version-4 UUID and `-000000` (capture, inferred):

| MessageId prefix | Region (from `sourceArn`) | Hex ms decodes to | `mail.timestamp` | Source |
|---|---|---|---|---|
| `0100017e4bccb684` | us-east-1 | 2022-01-12T01:00:14Z | 2022-01-12T01:00:14.340Z | [laravel-ses-event-manager L95-L99][c-iad] |
| `01010158992bd11e` | us-west-2 | 2016-11-25T01:48:55Z | 2016-11-25T01:48:55.454Z | [sisimai lhost-amazonses-13.eml L40][c-pdx] |
| `0102016498525570` | eu-west-1 | 2018-07-14T10:24:50Z | 2018-07-14T10:24:48.000Z | [bundle-aws-ses-monitor L47-L48][c-dub] |

- The `MessageId` is the identifier events and the `Message-ID` header refer to: event `mail.messageId` equals `commonHeaders.messageId` for a `SendEmail` (third-party-recorded 2023-08-25, [snapshot L28-L63][ls-event]).
- UNVERIFIED: the region code for any other region, whether v2 `SendEmail` IDs have the same shape, and whether the 12 hex characters are exactly acceptance time (SES-07).

## 5. Identities and verification

### 5.1 The identity model

All from [creating-identities][d-ident] unless noted:

- An identity is an email address or a domain; both kinds can coexist. Verifying a domain lets every subdomain and every address in it send, without separate identities ("Subdomains and email addresses inherit verification from the parent domain"); such an inheriting address is "limited to straightforward email sending" and must be verified itself for configuration sets, delegate sending and per-address settings. With only the domain verified, the API shows the domain as verified and the address as unverified.
- Identities are per Region: "To send email from the same domain or email address in more than one AWS Region, you must create and verify a separate identity for each Region." At most 10,000 identities per Region, domains and addresses combined (also [quotas][d-quotas]).
- "Domain names are case-insensitive. If you verify example.com, you can send from EXAMPLE.com also." "Email addresses *are* case sensitive. If you verify sender@EXAMPLE.com, you can't send email from sender@example.com unless you verify sender@example.com as well."
- Labels: with `sender@example.com` verified, `sender+myLabel@example.com` may be the "From" or "Return-Path" address without more verification.
- DNS labels up to 63 characters, whole domain up to 255 (RFC 1034).
- ARN: `arn:aws:ses:<region>:<account>:identity/<identity>` (provider, 2.3).

### 5.2 Verification states

- v1 `com.amazonaws.ses#VerificationStatus`: `Pending`, `Success`, `Failed`, `TemporaryFailure`, `NotStarted`. v2 `com.amazonaws.sesv2#VerificationStatus`: `PENDING`, `SUCCESS`, `FAILED`, `TEMPORARY_FAILURE`, `NOT_STARTED` ("The verification process hasn't been initiated for the identity") (model). v2 `DkimStatus` has the same five values (model).
- Email address: "Pending" until the owner clicks the link in the verification email; clicked within 24 hours, it becomes "Success"; otherwise "Failed", and verification restarts from the beginning (model, `GetIdentityVerificationAttributes`). The email comes from `no-reply-aws@amazon.com` within five minutes, subject "Amazon Web Services - Email Address Verification Request in region {RegionName}", and its link expires 24 hours after sending ([creating-identities, verify email][d-ident-email]).
- Domain (v1): "Pending" while SES searches for the TXT record; "Success" when found; "Failed" if not found within 72 hours; "The domain's verification status also changes to 'Success' when it is DKIM verified" (model, `VerifyDomainIdentityResponse.VerificationToken`). Domain (v2): verified when SES detects the DKIM CNAME records; "it can take 72 hours or more" (model, `CreateEmailIdentity`).
- UNVERIFIED: whether the change to `Failed` happens exactly 24 hours (address) or 72 hours (domain) after the start, or at a later periodic check (SES-27).
- The v1 waiter `IdentityExists` polls `GetIdentityVerificationAttributes` every 3 s minimum until every `VerificationAttributes.*.VerificationStatus` is `Success` (model, `smithy.waiters#waitable`).

### 5.3 v1 identity operations

| Operation | Input | Output | Notes |
|---|---|---|---|
| `VerifyEmailIdentity` | `EmailAddress` (required) | empty | sends the verification email; at most one call per second |
| `VerifyDomainIdentity` | `Domain` (required) | `VerificationToken` (required) | token example `eoEmxw+YaYhb3h3iVJHuXMJXqeu1q1/wwmvjuEXAMPLE`: 44 base64 characters, the encoding of 32 bytes (inferred) |
| `GetIdentityVerificationAttributes` | `Identities` (required list) | `VerificationAttributes` (required map identity -> {`VerificationStatus` required, `VerificationToken`}) | at most 100 identities per call, one call per second; example `{"example.com":{"VerificationToken":"EXAMPLE3VYb9EDI2nTOQRi/Tf6MI/6bD6THIGiP1MVY=","VerificationStatus":"Success"}}` |
| `ListIdentities` | `IdentityType` (`EmailAddress` or `Domain`), `NextToken`, `MaxItems` (1 to 1000 per docs) | `Identities` (required), `NextToken` | "regardless of verification status"; keep the same `IdentityType` across pages |
| `DeleteIdentity` | `Identity` (required) | empty | |
| `VerifyEmailAddress`, `ListVerifiedEmailAddresses`, `DeleteVerifiedEmailAddress` | | | "Deprecated", replaced by the three above |

Sources: model shapes and documentation; [API_ListIdentities][d1-list] for the `MaxItems` range. The model sets no constraints on `Address`, `Domain`, `Identity` (all plain `string`).

UNVERIFIED: whether an identity unknown to SES is omitted from `VerificationAttributes` (plausible: omitted, which is what Terraform relies on in section 9.1) or returned with a status, whether an email identity's entry has no `VerificationToken`, and the error for more than 100 identities (SES-08). UNVERIFIED: the error for `VerifyEmailIdentity` with a malformed address, whether calling it again on a `Pending` or `Success` identity changes its status, and what `ListVerifiedEmailAddresses` lists (SES-09). UNVERIFIED: whether a second `VerifyDomainIdentity` returns the same token (SES-10). UNVERIFIED: the order of `ListIdentities` and the shape of its `NextToken` (SES-14). UNVERIFIED: whether `DeleteIdentity` of an unknown identity succeeds (SES-11).

### 5.4 v2 identity operations

- `CreateEmailIdentity`: `EmailIdentity` (required, `Identity`, length min 1), `Tags`, `DkimSigningAttributes` (domains only: Bring Your Own DKIM selector and private key, or the Easy DKIM key length), `ConfigurationSetName` (the identity's default set). Output `IdentityType`, `VerifiedForSendingStatus` (default false), `DkimAttributes`; the response shape's documentation says "If the email identity is an email address, this object is empty" (UNVERIFIED: whether that means `{}` or only an absent `DkimAttributes`; SES-11 step 4 records it). Errors `AlreadyExistsException`, `BadRequestException`, `ConcurrentModificationException`, `LimitExceededException`, `NotFoundException`, `TooManyRequestsException` (model).
- `GetEmailIdentity` output: `IdentityType` (`EMAIL_ADDRESS`, `DOMAIN`; `MANAGED_DOMAIN` "is not supported"), `FeedbackForwardingStatus`, `VerifiedForSendingStatus`, `DkimAttributes` (`SigningEnabled`, `Status`, `Tokens`, `SigningHostedZone`, `SigningAttributesOrigin`, `NextSigningKeyLength`, `CurrentSigningKeyLength`, `LastKeyGenerationTimestamp`), `MailFromAttributes`, `Policies`, `Tags`, `ConfigurationSetName`, `VerificationStatus`, `VerificationInfo` (`LastCheckedTimestamp`, `LastSuccessTimestamp`, `ErrorType`, `SOARecord`) (model).
- What AWS returned right after `CreateEmailIdentity`, as Terraform's acceptance tests assert (provider, [email_identity_test.go L23-L59, L61-L89][tf-v2-test]):

| Attribute | Email address | Domain |
|---|---|---|
| `identity_type` | `EMAIL_ADDRESS` | `DOMAIN` |
| `verification_status` | `PENDING` | `PENDING` |
| `verified_for_sending_status` | false | false |
| DKIM block present | yes (1) | yes (1) |
| `current_signing_key_length` | empty | `RSA_2048_BIT` |
| `last_key_generation_timestamp` | empty | an RFC 3339 time |
| `next_signing_key_length` | `RSA_1024_BIT` | `RSA_2048_BIT` |
| `signing_attributes_origin` | `AWS_SES` | `AWS_SES` |
| DKIM `status` | `NOT_STARTED` | `PENDING` |
| DKIM `tokens` | 0 | 3 |

  So `GetEmailIdentity` returns a non-null `DkimAttributes` for an email address too. UNVERIFIED: the raw JSON (which members are absent versus empty, `Policies`, `Tags`, `MailFromAttributes`, `VerificationInfo` for each kind) (SES-13).
- `ListEmailIdentities` (3.2 for both bindings): `Filter` map with keys `IDENTITY_NAME_CONTAINS`, `IDENTITY_TYPE`, `VERIFICATION_STATUS` (values length min 1), `NextToken`, `PageSize` ("at least 0, and can be no more than 1000"); output `EmailIdentities` of `IdentityInfo` {`IdentityType`, `IdentityName`, `SendingEnabled`, `VerificationStatus`} and `NextToken`; it lists verified and unverified identities "associated with Amazon SES and Amazon Pinpoint" (model).
- `DeleteEmailIdentity`: errors `BadRequestException`, `ConcurrentModificationException`, `NotFoundException`, `TooManyRequestsException` (model). Terraform treats `NotFoundException` on delete as already deleted ([email_identity.go L245-L263][tf-v2-del]).
- Tags on identities go through the generic `TagResource`, `UntagResource` and `ListTagsForResource` with `ResourceArn` (provider, [tags_gen.go L26-L30, L104-L130][tf-v2-tags]).
- UNVERIFIED: the `NotFoundException` message for `GetEmailIdentity` and `DeleteEmailIdentity` of an unknown identity, and whether `CreateEmailIdentity` of an existing identity fails `AlreadyExistsException` (SES-11).

### 5.5 What verification needs on AWS

Verification is an act outside the API: a person opens a link in an email SES sent (address), or SES finds DNS records the owner published (domain). No API call completes it. Section 11.3 turns this into an emulator mechanism that keeps the act explicit.

## 6. Sandbox and quotas

### 6.1 Sandbox rules, per send

From [request-production-access][d-sandbox] and [troubleshoot-error-messages][d-err]:

- New accounts are in the sandbox, separately in each Region. In the sandbox: mail only to verified addresses and domains or the mailbox simulator; at most 200 messages per 24-hour period; at most 1 message per second; sending-authorization delegates cannot reach unverified recipients either.
- Out of the sandbox, recipients are unrestricted, but "you still have to verify all identities that you use as 'From', 'Source', 'Sender', or 'Return-Path' addresses".
- The documented failure: "Email address is not verified. The following identities failed the check in region *region*: *identity1*, *identity2*, *identity3*". It "could apply to the 'From', 'Source', 'Sender', or 'Return-Path' address"; in the sandbox it also covers every recipient except mailbox simulator addresses. "If Amazon SES is not able to show all of the failed identities, the error message ends with an ellipsis." Verification status is separate per Region.
- Region spelling: upper case in a fixture copied from an API response, `... in region US-EAST-1: WordPress <wordpress@example.com>`, Code `MessageRejected`, status 400 (capture, [SesClientTest.php L74, L100][c-ymir]), and in a real bounce from the SES SMTP endpoint, `554 Message rejected: Email address is not verified. The following identities failed the check in region AP-NORTHEAST-1: nekochan@example.jp` (capture, 2022-08-05, [sisimai lhost-postfix-77.eml L54-L57][c-smtp]). The fixture also shows the identity printed as written in the request, display name included.
- UNVERIFIED: the bytes of this error on v1 and v2 (SES-01, SES-02); which identities are listed when sender and recipients both fail, and in what order (SES-03).

### 6.2 Mailbox simulator

From [send-an-email-from-console, mailbox simulator][d-sim]:

| Address | Simulated outcome |
|---|---|
| `success@simulator.amazonses.com` | delivered; a Delivery notification if configured |
| `bounce@simulator.amazonses.com` | SMTP 550 5.1.1 hard bounce (RFC 3464); not added to the suppression list |
| `ooto@simulator.amazonses.com` | delivered, then an automatic reply (RFC 3834) |
| `complaint@simulator.amazonses.com` | delivered, then a complaint (RFC 5965) |
| `suppressionlist@simulator.amazonses.com` | hard bounce as if on the global suppression list |

- Usable from the sandbox; limited by the maximum sending rate but not counted against the daily quota; billed like any email; supports labels (`bounce+label1@simulator.amazonses.com`); multiple bounces in one request are combined. It has no Reject address. Simulator sends do not count toward the sending quota or bounce and complaint rates (same page).
- UNVERIFIED: whether other local parts at `simulator.amazonses.com` count as simulator addresses, and whether the local part matches case-insensitively (SES-19).

### 6.3 Quotas and account

- Quotas count recipients, not messages, per Region ([quotas][d-quotas]). Sandbox: 200 per 24 hours and 1 per second; production: "varies based on your specific use case".
- v1 `GetSendQuota` output `Max24HourSend`, `MaxSendRate`, `SentLast24Hours`, all `double` with default 0; "A value of -1 signifies an unlimited quota" (model). A sandbox account recorded `Max24HourSend` `200.0`, `MaxSendRate` `1.0` (third-party-recorded 2024-11-27, [snapshot L905-L918][ls-quota], [validation L68-L70][ls-tags-val]); the same test's attempt to see `SentLast24Hours` rise was not reproducible on AWS ("the Quota would not go up, maybe due to Sandbox account", [test_ses.py L234][ls-quota-test]). The test is marked `needs_fixing`, not `validated`; the `get-quota-0` snapshot is taken before the assertion that failed, and the validation file still dates it, so only those two values count as recorded. They agree with the documented sandbox limits ([quotas][d-quotas]).
- v1 `GetSendStatistics`: 15-minute data points over the last two weeks, each `Timestamp`, `DeliveryAttempts`, `Bounces`, `Complaints`, `Rejects` (model).
- v2 `GetAccount` output: `DedicatedIpAutoWarmupEnabled`, `EnforcementStatus` (a string: `HEALTHY`, `PROBATION`, `SHUTDOWN` per its documentation), `ProductionAccessEnabled` ("If the value is false, then your account is in the sandbox"), `SendQuota` (same three members as v1), `SendingEnabled`, `SuppressionAttributes`, `Details`, `VdmAttributes`, `PricingAttributes` (model).
- Over-quota texts: `Daily message quota exceeded`, `Maximum sending rate exceeded` ([troubleshoot-error-messages][d-err]); the rate case is Code `Throttling` with `Maximum sending rate exceeded.` (3.3), SMTP `454 Throttling failure: Maximum sending rate exceeded` ([AWS blog][d-blog]). UNVERIFIED: the rate limiter's burst behavior and the status code (SES-21); the daily-quota Code and status (SES-22); the raw `GetAccount` JSON of a sandbox account (SES-15); production quota values (SES-26).
- A failed send is not billed ([troubleshoot-error-messages][d-err]); outbound mail costs USD 0.10 per 1,000 emails, simulator mail included ([pricing][d-price]).

### 6.4 Check order

Recorded: tag validation precedes the sender check (4.5). Everything else about order is UNVERIFIED: model validation, then address syntax, recipient count, configuration-set existence, account sending enabled, sender and sandbox checks, quotas is the plausible order (SES-17 records configuration set against an unverified sender).

## 7. Templates

- v1 `com.amazonaws.ses#Template`: `TemplateName` (required), `SubjectPart`, `TextPart`, `HtmlPart`; no constraint traits (model). `CreateTemplate` errors `AlreadyExists` (member `Name`), `InvalidTemplate` ("may occur when a template refers to a partial that does not exist"), `LimitExceeded`; `GetTemplate` and `UpdateTemplate` raise `TemplateDoesNotExist`; `DeleteTemplate` declares none ([API_CreateTemplate][d1-ct], [API_GetTemplate][d1-gt], model).
- Recorded v1 behavior (third-party-recorded 2023-08-25, [snapshot L736-L807][ls-tmpl]): `CreateTemplate` and `DeleteTemplate` answer with only `ResponseMetadata`; `ListTemplates` returns `TemplatesMetadata` of `{"CreatedTimestamp", "Name"}`, and `[]` when there are none; a template disappears from the list after `DeleteTemplate`.
- v2 `CreateEmailTemplate` (`POST /v2/email/templates`): `TemplateName` (min 1), `TemplateContent` {`Subject`, `Text`, `Html`} required, `Tags`; errors `AlreadyExistsException`, `BadRequestException`, `LimitExceededException`, `TooManyRequestsException`. `GetEmailTemplate` returns `TemplateName`, `TemplateContent`, `Tags`; errors include `NotFoundException` (model).
- Limits: 20,000 templates per Region; 500 KB per template (text and HTML parts together); unlimited replacement values; 50 destinations per templated send ([quotas][d-quotas], [send-personalized-email-api][d-tmpl]). Terraform accepts names of 1 to 64 characters and `html` and `text` of up to 512,000 characters, and does not constrain `subject` ([template.go L44-L63][tf-tmpl]).
- Rendering: tags are `{{tagname}}`; `TemplateData` is an escaped JSON object of replacement values ([send-personalized-email-api][d-tmpl]). Stored templates use Handlebars: nested paths, `{{#each}}`, `{{#if}}`, inline partials, "Handlebars includes additional features beyond those documented in this section" ([send-personalized-email-advanced][d-tmpl-adv]).
- UNVERIFIED: the messages of `TemplateDoesNotExist`, `AlreadyExists`, v2 `NotFoundException` for a template (SES-18); whether v1 and v2 templates are one store (SES-12).

## 8. Configuration sets (scope only)

- Per Region; names "up to 64 alphanumeric characters", plus `-` and `_`; 10,000 per Region; 10 event destinations per set ([quotas][d-quotas]). A set applies rules (event destinations, dedicated IP pools) to the mail sent with it, named in the request, in `X-SES-CONFIGURATION-SET`, or as an identity's default ([using-configuration-sets][d-cs]). A default set "will only be applied if the identity is verified", and an address's default overrides its domain's ([managing-configuration-sets][d-cs-manage]); "any configuration set defined in the email sending request takes precedence" (model, `com.amazonaws.sesv2#CreateEmailIdentityRequest$ConfigurationSetName`).
- Recorded v1 texts, Type `Sender`, 400 (third-party-recorded 2023-08-25, [snapshot L494-L590][ls-cs]): `ConfigurationSetDoesNotExist` `Configuration set <NAME> does not exist.` (from `DeleteConfigurationSet`, `CreateConfigurationSetEventDestination` and `DeleteConfigurationSetEventDestination`); `EventDestinationDoesNotExist` `No EventDestination found for NAME`; `InvalidSNSDestination` `SNS topic <ARN> not found.`. In the first and third, the angle brackets are part of AWS's text: the snapshot shows `<<config-set>>` and `<<arn>>`, where the inner pair is LocalStack's placeholder for the replaced value, and one entry keeps the raw `<non-existent-configuration-set>`. The event-destination text has no brackets, and `NAME` there is the configuration set's name, not the event destination's (snapshot `<config-set>`; [test_ses.py L840-L854][ls-cs-test]).
- Event publishing to SNS produced, for one `SendEmail`, a topic-validation message and then `Send` and `Delivery` events whose `mail` object carries `source`, `sourceArn`, `sendingAccountId`, `messageId`, `destination`, `headers`, `commonHeaders` and the auto-tags (third-party-recorded 2023-08-25, [snapshot L2-L186][ls-event]). Recorded for a later item; journal-only SES publishes no events (11.8).
- UNVERIFIED: the text for a send naming a missing configuration set on v1 and v2, and how an inheriting address (5.1) fails with a configuration set (SES-17, SES-25).

## 9. Terraform

### 9.1 aws_ses_email_identity

- Create: `VerifyEmailIdentity` with the configured `email` minus one trailing `.`, then Read ([email_identity.go L46-L48, L55-L73][tf-ei]).
- Read: `GetIdentityVerificationAttributes` with `Identities=[id]`; the identity is gone when its key is missing from `VerificationAttributes` (`NotFoundError`, removed from state on refresh); `arn` is built client-side as `arn:<partition>:ses:<region>:<account>:identity/<id>` ([L75-L96, L115-L148][tf-ei-find]). The status is not read, so a `Pending` identity is a complete resource.
- Delete: `DeleteIdentity` ([L98-L113][tf-ei-del]).
- The `aws_ses_email_identity` data source calls `GetIdentityNotificationAttributes`, not the verification call, and only checks that the identity's key is in `NotificationAttributes` ([email_identity_data_source.go L48][tf-ei-ds], [identity_notification_topic.go L196-L225][tf-notif]). The model marks `BounceTopic`, `ComplaintTopic`, `DeliveryTopic` and `ForwardingEnabled` required in `com.amazonaws.ses#IdentityNotificationAttributes`. UNVERIFIED: what AWS returns for an identity with no topics set (empty topic elements or none; `ForwardingEnabled` true plausibly) (SES-08).
- Needs: the map key equal to the request string byte for byte; a stable answer across reads; the key absent after delete.

### 9.2 aws_ses_domain_identity and aws_ses_domain_identity_verification

- `aws_ses_domain_identity`: `VerifyDomainIdentity`, then `GetIdentityVerificationAttributes`, storing `verification_token` from the map; `domain` must not end with a period ([domain_identity.go L41-L98][tf-di]). The token must not change between reads.
- `aws_ses_domain_identity_verification` polls `GetIdentityVerificationAttributes` until `VerificationStatus` is `Success`, default create timeout 45 minutes, and on later reads treats any other status as "not found" ([domain_identity_verification.go L47-L49, L53-L106][tf-div]). Against an emulator whose domains stay `Pending`, apply waits 45 minutes and fails.

### 9.3 aws_sesv2_email_identity

- Create: `CreateEmailIdentity` with `EmailIdentity`, `Tags`, optional `ConfigurationSetName` and `DkimSigningAttributes`; empty output is an error ([email_identity.go L137-L166][tf-v2]).
- Read: `GetEmailIdentity` (`NotFoundException` means gone) and `ListTagsForResource(ResourceArn=arn)` for tags; sets `configuration_set_name`, `dkim_signing_attributes` (only when `DkimAttributes` is non-null, keeping the configured private key and selector), `identity_type`, `verification_status`, `verified_for_sending_status` ([L168-L204, L265-L291][tf-v2-read]).
- Update: `PutEmailIdentityConfigurationSetAttributes`, `PutEmailIdentityDkimSigningAttributes`, `TagResource`/`UntagResource` ([L206-L243][tf-v2-upd]). Delete: `DeleteEmailIdentity`, `NotFoundException` ignored.
- Needs, for an empty second plan: `DkimAttributes` non-null with the values in 5.4 and stable tokens; `ConfigurationSetName` echoed; tags round-tripped through `ListTagsForResource`.

### 9.4 aws_ses_template

`CreateTemplate`, `GetTemplate` (`TemplateDoesNotExistException` means gone), `UpdateTemplate`, `DeleteTemplate`; `html`, `subject`, `text` are Optional and not Computed, so `GetTemplate` must echo each part exactly and omit unset ones; `arn` is client-side `template/<name>` ([template.go L27-L210][tf-tmpl-all]).

## 10. Conflicts between sources

| Topic | Source A | Source B | Resolution |
|---|---|---|---|
| Tag value charset | model docs: letters, digits, `_`, `-` | recorded message admits `.` and `@` | follow the recording; SES-16 step 7 tests a value with both |
| Tag length | model docs: 256 or fewer | recorded "cannot exceed 255 characters" | 255, switch `ses.tags.max-length` (SES-16) |
| Which addresses need verification | DG: From, Source, Sender, Return-Path | boto (v2) comment: an unverified Reply-To raised "Email address is not verified." | follow the current DG and leave Reply-To unchecked. UNVERIFIED: whether today's SES checks Reply-To (plausible: no, as the DG list omits it; alternative: the boto-era check remains) (SES-03) |
| Unverified-sender text | DG template (6.1) | moto: `Email address not verified <source>` | moto is not evidence; follow the DG |
| `ListEmailIdentities` binding | pinned model: `POST /v2/email/list-identities` | SDKs before 2026-09-29: `GET /v2/email/identities` | accept both (11.6) |

## 11. Implications for understudy

### 11.1 Operations and coverage

Service ids `ses` (v1) and `sesv2` (v2), sharing one state model: identities, templates, account settings and the sent-message store, per account and Region (2.3, switch `ses.store.shared-v1-v2`). Coverage notes below are the exact registry text.

First slice (the notification path):

| Operation | Level | Coverage note |
|---|---|---|
| v1 `SendEmail` | Partial | `journal-only: validates and applies identity and sandbox rules, returns a MessageId and records the message for GET /_understudy/v1/ses/messages; no mail is sent, and no Send, Delivery, Bounce or Complaint event or notification is produced; ConfigurationSetName and the sending-authorization ARNs answer NotImplemented; chosen without a recording (docs/research/ses.md 11.4): address, recipient-count, size and daily-quota answers, tag boundaries and ses: names, check order, mailbox-simulator and label matching` |
| v1 `SendRawEmail` | Partial | `journal-only as SendEmail; the raw MIME is recorded as received, without the Message-ID and Date headers SES would substitute; recipients come from Destinations, else the To, Cc and Bcc headers; X-SES-MESSAGE-TAGS is recorded; ConfigurationSetName, X-SES-CONFIGURATION-SET, the ARN members and the X-SES-*-ARN headers answer NotImplemented` |
| v1 `SendTemplatedEmail` | Partial | `journal-only as SendEmail; the stored template and TemplateData are recorded, and rendered subject and bodies only for {{name}} and dotted-path tags; other Handlebars constructs record a render gap, and the send is still accepted, as SES accepts before rendering` |
| v2 `SendEmail` | Partial | `journal-only as the v1 sends, for Simple (with Headers and Attachments) and Raw content; FeedbackForwardingEmailAddress must be verified, as ReturnPath (inferred); Template content, ConfigurationSetName, ConfigurationOverrides, EndpointId, TenantName, ListManagementOptions and the identity ARN members answer NotImplemented` |
| v1 `VerifyEmailIdentity`, `VerifyEmailAddress` | Partial | `the identity stays Pending until its verification link under /_understudy/ses/VerificationLink is opened, the admin API verifies it or understudy.json lists it; the verification email is recorded, never sent; Failed after 24 hours on the emulator clock` |
| v1 `VerifyDomainIdentity` | Partial | `the domain stays Pending until the admin API verifies it or understudy.json lists it; DNS is never queried; Failed after 72 hours on the emulator clock` |
| v1 `GetIdentityVerificationAttributes`, `ListIdentities`, `DeleteIdentity`, `ListVerifiedEmailAddresses`, `DeleteVerifiedEmailAddress` | Full | |
| v1 `GetIdentityNotificationAttributes` | Partial | `every identity reports no SNS topics, ForwardingEnabled true and headers disabled; SetIdentityNotificationTopic and the other setters are not implemented` |
| v1 `GetSendQuota` | Partial | `Max24HourSend and MaxSendRate from the sandbox or production setting; SentLast24Hours counts accepted recipients on the emulator clock, mailbox simulator excluded; the send rate is not enforced; production values -1 and 14 are unrecorded (SES-26)` |
| v1 `CreateTemplate`, `GetTemplate`, `UpdateTemplate`, `DeleteTemplate`, `ListTemplates` | Full | |
| v2 `CreateEmailIdentity` | Partial | `email identities start PENDING with DKIM NOT_STARTED; domains get three random Easy DKIM tokens and stay PENDING; DkimSigningAttributes are stored, never used; verification as VerifyEmailIdentity; ConfigurationSetName answers NotImplemented` |
| v2 `GetEmailIdentity`, `ListEmailIdentities` (both bindings), `DeleteEmailIdentity` | Full | Until the GET binding lands, `ListEmailIdentities` is Partial: `the GET /v2/email/identities binding sent by SDKs generated before 2026-09-29 is not routed and answers sesv2's unknown operation` |
| v2 `GetAccount` | Partial | `ProductionAccessEnabled and SendQuota from understudy state; SendingEnabled true, since account sending cannot be paused yet; EnforcementStatus HEALTHY; no dedicated IP, VDM, pricing, suppression or account details` |
| v2 `CreateEmailTemplate`, `GetEmailTemplate`, `UpdateEmailTemplate`, `DeleteEmailTemplate`, `ListEmailTemplates` | Full | |
| v2 `TagResource`, `UntagResource`, `ListTagsForResource` | Partial | `identity ARNs only; a template ARN answers NotFoundException until the v2 template operations land; an identity ARN with an empty account field names the caller's account, as Terraform sends it under skip_requesting_account_id (what AWS answers is unrecorded)` (Full, for identity and template ARNs, once they land; the empty-account clause stays). Amended 2026-10-30: `aws_sesv2_email_identity` builds the identity ARN from the provider's account, which is empty under `skip_requesting_account_id`, the setting `conformance/terraform/provider.tf` needs at an IP endpoint (apigateway-http.md 13.6); run against understudy built on 2026-10-30 with provider 6.68.0, every read failed `ListTagsForResource` on `arn:aws:ses:us-east-1::identity/<name>` until the empty account was read as the caller's |

Until its stage lands, each of `SendTemplatedEmail`, the v2 email templates and `VerifyEmailAddress`, `ListVerifiedEmailAddresses`, `DeleteVerifiedEmailAddress` is unbound (501), and v2 `Template` content answers NotImplemented. Configuration sets are not in the first slice: `ConfigurationSetName` on a send or on `CreateEmailIdentity`, and the raw `X-SES-CONFIGURATION-SET` header, answer NotImplemented naming the member or header. So do the sending-authorization members `SourceArn`, `FromArn`, `ReturnPathArn`, `FromEmailAddressIdentityArn`, `FeedbackForwardingEmailAddressIdentityArn` and the raw `X-SES-SOURCE-ARN`, `X-SES-FROM-ARN`, `X-SES-RETURN-PATH-ARN` headers, and v2 `ConfigurationOverrides` and `EndpointId`, whatever the signature (2.1 leaves the SigV4A path inferred). v2 `FeedbackForwardingEmailAddress` is sender-checked as `ReturnPath` and recorded as `returnPath` (inferred: both name where bounces and complaints go, and 6.1 names Return-Path).

Second slice, deferred to a later item and unbound (501) until then: v1 `CreateConfigurationSet`, `DeleteConfigurationSet`, `DescribeConfigurationSet`, `ListConfigurationSets` and v2 `CreateConfigurationSet`, `GetConfigurationSet`, `DeleteConfigurationSet`, `ListConfigurationSets` (both bindings) as StoredOnly with `stored and checked by name on sends; no event destinations, tracking, reputation, delivery or suppression options take effect`; v2 `PutEmailIdentityConfigurationSetAttributes` Full (it selects the default set recorded on sends); v2 `PutEmailIdentityDkimSigningAttributes` StoredOnly `stored; DKIM status never changes on its own`; v1 `GetAccountSendingEnabled`/`UpdateAccountSendingEnabled` Full (they gate sends with `AccountSendingPausedException`). Trace `aws_sesv2_configuration_set` and `aws_ses_configuration_set` reads before binding them.

Unbound (501): `GetSendStatistics` (it reports delivery attempts, bounces and complaints that journal-only sending cannot have), `SendBulkTemplatedEmail`, `SendBulkEmail`, `SendCustomVerificationEmail` and custom verification templates, `TestRenderTemplate`/`TestRenderEmailTemplate`, event destinations, the identity notification setters (`SetIdentityNotificationTopic`, `SetIdentityFeedbackForwardingEnabled`, `SetIdentityHeadersInNotificationsEnabled`), receipt rules and filters (inbound mail), DKIM operations (`VerifyDomainDkim`, `GetIdentityDkimAttributes`, `SetIdentityDkimEnabled`), MAIL FROM, identity policies and sending authorization, suppression lists, contact lists, dedicated IPs, VDM, deliverability, export and import jobs, tenants, multi-region endpoints.

### 11.2 Journal-only: what is recorded and how tests read it

- An accepted send records one sent message in SES state, keyed by `MessageId`, per account and Region: operation and API (`ses` or `sesv2`), request id, emulator acceptance time, `Source`/`FromEmailAddress` as given, the parsed sender addresses, `ReplyTo`, `ReturnPath`, To, Cc, Bcc, the envelope recipients the sandbox check used, subject and text and HTML bodies with charsets (Simple), v2 `Headers` and `Attachments` (name, content type, base64 content), the raw MIME bytes (Raw), template name, ARN or inline content plus `TemplateData` and the rendered parts or the render gap (Template), configuration set, tags, and the mailbox simulator scenario per recipient when one applies. Simple content is recorded as fields, not as a MIME message SES never showed: AWS's assembled bytes are not known.
- State, not journal: the journal stores every input as its generated `Redact()` (`internal/kernel/doc.go`; there is no `journal.bodies` key), evicts from a bounded ring, and snapshots omit it unless `--include-journal` ([ARCHITECTURE.md](../ARCHITECTURE.md) 10). The pinned models mark no send content sensitive, so both generated `SendEmailInput.Redact()` would return their input unchanged (`internal/api/sesapi/redact_gen.go`, `internal/api/sesv2api/redact_gen.go`); overlay `smithy.api#sensitive` traits on the content shapes (v1 `SendEmailRequest$Message`, `RawMessage$Data`, `SendTemplatedEmailRequest$TemplateData`; v2 `Message$Subject`, `Message$Body`, `Attachment$RawContent`, `RawMessage$Data`, `Template$TemplateContent`, `Template$TemplateData`) keep subjects, bodies, attachments, raw MIME and template data out of the journal. Tests assert on bodies, so the messages live in SES state, bounded (10,000 per account and Region, oldest evicted, eviction counted in the listing), exported in snapshots and cleared by `understudy reset --service ses`.
- Read path: `GET /_understudy/v1/ses/messages` with filters `account`, `region`, `messageId`, `from`, `to` (any recipient), `since` (emulator time), `limit` and a cursor, oldest first; `DELETE /_understudy/v1/ses/messages` with the same filters; `admin.Client.SESMessages` and `admin.Client.ClearSESMessages`; CLI `understudy ses messages [--to ADDR] [--from ADDR] [--message-id ID] [--follow]`. Correlation: each record carries the request id, so `understudy journal --request-id` finds the call.
- The journal keeps its normal `APICall` event per send, touching the sender identity ARN (and the configuration set ARN once configuration sets are bound); identity verification transitions are `StateChange` events (`Pending` to `Success` or `Failed`) on the identity ARN.
- Message ids, domain verification tokens and DKIM tokens are new `internal/ids` kinds, seeded under `--seed` ([ADR 0018](../adr/0018-ids-and-determinism.md)).

### 11.3 Verification and sandbox controls

- Identities start `Pending` / `PENDING`, as on AWS (5.2). Three explicit ways complete verification, each journaled as a `StateChange`:
  1. The verification email for an address identity is itself a recorded sent message, subject `Amazon Web Services - Email Address Verification Request in region <RegionName>` (5.2), whose body carries a link `/_understudy/ses/VerificationLink?token=<token>` (unversioned and AWS-visible, as the SNS confirm links are; edge rule 1 names the raw operation after `/_understudy/<service>/`, as `snsLink` in `internal/edge/claim.go` does for SNS). Opening it verifies, mirroring the owner's click. It works for 24 hours of emulator time.
  2. `POST /_understudy/v1/ses/identities/{identity}/verification` with `{"account", "region", "status": "Success" | "Failed" | "TemporaryFailure"}`, CLI `understudy ses verify IDENTITY [--status S]`. This is the only path for domains, which have no email to click.
  3. A file key `ses.verifiedIdentities`: a list of exact addresses, domains and `*@domain` patterns verified at creation, so Terraform's `aws_ses_domain_identity_verification` (9.2) completes. Configured identities are a statement by the user that they own them; the guide says so.
- Expiry on the emulator clock: `Pending` email identities become `Failed` 24 hours after the verification email, domains 72 hours after creation (5.2; switch `ses.verification.expiry`).
- Sandbox: each account and Region starts in the sandbox (6.1). Production access comes from the file key `ses.productionAccess` (default false; a list of `{account, region}` or `true` for all) and, in a later stage, from the admin endpoint `PUT /_understudy/v1/ses/account` `{"account", "region", "productionAccess": bool}`. Account sending is always enabled: `UpdateAccountSendingEnabled` is second slice. v2 `PutAccountDetails` stays unbound: on AWS it only files a request for human review (request-production-access).
- Sender check on every send, sandbox or not: `Source`/`FromEmailAddress`, the raw `From` and `Sender` headers, and `ReturnPath` or the raw `Return-Path` must each be a verified address (case-sensitive, labels stripped) or be in a verified domain or a subdomain of one (case-insensitive) (5.1). In the sandbox, each envelope recipient must pass the same test or be a mailbox simulator address (6.1, 6.2).

### 11.4 Fidelity switches

Six switches (`fidelity.Switch`), each asserted by a test that fails when its value flips:

| Name | Default | Label | Ground | Record |
|---|---|---|---|---|
| `ses.store.shared-v1-v2` | `shared`: one identity, template and configuration-set store for both APIs | inferred | 2.3 | SES-12 |
| `ses.message-id.format` | `01` + region code + 12 hex digits of emulator-clock epoch ms + `-` + seeded UUIDv4 + `-000000`; codes us-east-1 `00`, us-west-2 `01`, eu-west-1 `02`, any other Region `00` | inferred | 4.7 | SES-07 |
| `ses.error.unverified.text` | `Email address is not verified. The following identities failed the check in region <REGION>: <ids>`, Region upper case, identities as written in the request, senders first, then recipients in request order, joined by `, ` | inferred | 6.1 | SES-01, SES-03 |
| `ses.verification.unknown-identity` | omitted from `VerificationAttributes` | inferred | 5.3 | SES-08 |
| `ses.raw.envelope` | `Destinations` when non-empty, else `To`, `Cc`, `Bcc` headers | inferred | 4.2 | SES-20 |
| `ses.denial.identity` | v1 `AccessDenied`, Type `Sender`, 403, the IAM grammar (as STS, aws-recorded, and IAM, inferred); v2 `AccessDeniedException` in `x-amzn-ErrorType`, 403, body `{"message": "<grammar>"}` (`message` is the v2 member name, 3.2) | inferred ([iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 12.3 requires a switch for every inferred denial identity) | IS 12.3 | SES-28 |

Chosen without a switch: each is an exported constant, or an ordered table, in `internal/service/ses` whose comment names its UNVERIFIED case, and tests read the constant rather than a literal. Recording one of these cases means searching the code for its id, since `coverage-check` cannot flag them:

| Name | Value | Label | Ground | Record |
|---|---|---|---|---|
| `ses.tags.max-length` | 255 for names and values | inferred from the third-party-recorded text; the recordings used 2,100 and 2,200 characters, so the boundary is unrecorded | 4.5 | SES-16 |
| `ses.tags.ses-prefix` | only `ses:feedback-id-a` and `ses:feedback-id-b`; other `ses:` names fail the charset rule | inferred | 4.5 | SES-16 |
| `ses.verification.expiry` | email `Failed` exactly 24 h after the verification email, domain exactly 72 h after creation | aws-doc for the 24 h and 72 h; inferred that the change is exact | 5.2 | SES-27 |
| `ses.simulator.addresses` | the five documented local parts, optional `+label`, local part exact, domain case-insensitive; other `simulator.amazonses.com` addresses are ordinary recipients | inferred | 6.2 | SES-19 |
| `ses.sandbox.recipient-labels` | a labeled recipient passes when the unlabeled address is verified | inferred | 5.1 | SES-19 |
| `ses.quota.send-rate` | `not-enforced` | inferred | 6.3 | SES-21 |
| `ses.quota.production` | `Max24HourSend` -1, `MaxSendRate` 14 | inferred | 6.3 | SES-26 |
| `ses.send.check-order` | model validation, NotImplemented members, tags, addresses, recipient count, sender, sandbox recipients, daily quota (configuration sets and account sending are not bound, so their steps are absent) | inferred (tags before sender: third-party-recorded) | 6.4 | SES-17 |

Overlay entries rather than switches: the v1 `QueryErrorEnvelope` `{Namespace: true, Detail: false}` (inferred, 3.1, SES-01); throttle error v1 `Throttling` (aws-doc, status 400 inferred, SES-21) and v2 `TooManyRequestsException` 429 (model); validation identities v1 `InvalidParameterValue` 400 and v2 `BadRequestException` 400.

### 11.5 Limits and validation texts

- Recorded texts, exact after XML decoding (the snapshots are SDK-parsed): the nine tag messages (4.5); `Configuration set <NAME> does not exist.`, brackets literal, for configuration-set operations (8). Recorded values: sandbox quota 200 and 1 (6.3); their wire spelling (`200.0` or `200`) is not recorded (SES-15).
- Documented limits to enforce: 50 recipients per message across To, Cc, Bcc (4.1); at least one recipient; message size 10 MB on v1 and 40 MB on v2, after base64 (2.2, [quotas][d-quotas]); raw lines up to 1,000 characters and 500 MIME parts (4.2); `TemplateData` up to 262,144 (model); 15 v2 headers with their patterns (4.4); 10,000 identities, 20,000 templates, 10,000 configuration sets per Region; template 500 KB; `GetIdentityVerificationAttributes` up to 100 identities; `ListIdentities` `MaxItems` 1 to 1000, `ListEmailIdentities` `PageSize` 0 to 1000.
- Texts to use until recorded, each behind its switch or recording: the unverified-identity text (6.1); `Daily message quota exceeded.` (boto-era wording, 4.6) with Code `Throttling` (inferred from the rate case, 3.3; UNVERIFIED, SES-22); for invalid addresses, Code `InvalidParameterValue` (inferred) with the boto-era wording (`Illegal address`, `Domain ends with dot.`, `Local address contains control or whitespace`) and `Missing final '@domain'` (4.6, SES-04).

### 11.6 Registry, edge and codegen

1. Done: both models are vendored, and `internal/cmd/vendor/reference.go` maps `ses` and `sesv2` to IAM prefix `ses` (2.1).
2. Registry: settled by [ADR 0019](../adr/0019-shared-signing-names.md). `BySigningName` returns the name's services in registration order, and an awsQuery plus REST pair is always accepted; `ses` is wired before `sesv2` (3.4).
3. Edge: rule 7 claims v1 by `(Action, 2010-12-01)`; rule 8 tries `sesv2`'s route table, and a request signed for `ses` that neither claims is `sesv2`'s unknown operation until SES-24 says otherwise (ADR 0019).
4. Overlay for `sesv2`: extra routes `GET /v2/email/identities` (query `NextToken`, `PageSize`) to `ListEmailIdentities` and `GET /v2/email/configuration-sets` to `ListConfigurationSets`, with the 3.2 history as evidence. `POST /v2/email/identities` stays `CreateEmailIdentity`.
5. Address parsing with `net/mail` for the RFC 5322 mailbox forms; AWS's parser behaves like Jakarta Mail (4.6), so differences are expected and SES-04 decides the texts. MIME parsing of raw messages with `net/mail` and `mime/multipart` (stdlib, ADR 0016).
6. Template rendering: substitution of `{{name}}` and dotted paths from `TemplateData`; any other Handlebars construct records a render gap rather than a guess (7). A full Handlebars engine is a dependency question for ADR 0016, not this item.

### 11.7 Terraform

- `aws_ses_email_identity`: apply and an empty second plan need `VerifyEmailIdentity`, `GetIdentityVerificationAttributes` keyed by the exact request string, and `DeleteIdentity`; its data source needs `GetIdentityNotificationAttributes` keyed the same way (9.1).
- `aws_ses_domain_identity`: a token minted once and returned unchanged on every read (9.2).
- `aws_ses_domain_identity_verification`: completes only for a domain in `ses.verifiedIdentities` or verified through the admin API during apply; otherwise it waits its 45-minute timeout. The guide must say so.
- `aws_sesv2_email_identity`: the 5.4 attribute values, stable DKIM tokens, `ListTagsForResource` on the identity ARN, and `NotFoundException` after delete (9.3). With `configuration_set_name` it needs the second slice.
- `aws_ses_template`: byte-exact parts, unset parts omitted (9.4).
- No provider-side wall-clock waits apart from the domain verification poller.

### 11.8 Never fake

- No SMTP and no outbound connection of any kind ([ADR 0015](../adr/0015-cross-service-delivery.md) keeps runtime network access closed).
- No `Delivery`, `Bounce`, `Complaint`, `Reject` or `DeliveryDelay` event, no `smtpResponse`, no suppression-list entry, no bounce notification for the simulator addresses: nothing was delivered. `GetSendStatistics` stays unbound for the same reason.
- Verification never completes on its own: no DNS lookup, no timer to `Success`, no automatic `Success` outside `ses.verifiedIdentities`.
- No synthesized MIME for Simple messages and no rewritten raw bytes presented as what SES sent.
- No rendered output for template constructs outside the supported subset.
- Quotas count what was accepted, and the sandbox check runs unless production access is configured.

## 12. Recording queue

Agents never run these. A maintainer runs them in the recording-kit shell ([recording-kit.md](../recording-kit.md) sections 1 and 2: `$P`, `$R`, `$A`, `rec`, `recurl`). Captures go to `$REC/SES-NN/`. Unless a step says otherwise, sends fail before acceptance, so they cost nothing ([troubleshoot-error-messages][d-err]); accepted sends go to the mailbox simulator at USD 0.10 per 1,000 ([pricing][d-price]). If the account is a free-plan project and an SES call returns an explicit deny from a service control policy, mark the step `n/a (free-plan SCP)`.

### 12.1 Setup and shared teardown

```sh
export M=<an address whose inbox you can read>   # the one verified sender
q() {  # q <item>/<step> <Action> [Key=Value ...]: a raw SES v1 Query call
  local out=$1 action=$2; shift 2
  local args=(--data-urlencode "Action=$action" --data-urlencode "Version=2010-12-01")
  for kv in "$@"; do args+=(--data-urlencode "$kv"); done
  SVC=ses recurl "$out" -H 'Content-Type: application/x-www-form-urlencoded' "${args[@]}" "https://email.$R.amazonaws.com/"
}
v2() {  # v2 <item>/<step> <METHOD> <path> [json]: a raw SES v2 call
  local body=(); [ -n "${4:-}" ] && body=(-H 'Content-Type: application/json' -d "$4")
  SVC=ses recurl "$1" -X "$2" "${body[@]}" "https://email.$R.amazonaws.com$3"
}
MSG='Subject={Data=s},Body={Text={Data=b}}'
b64() { base64 < "$1" | tr -d '\n'; }   # one-line base64 of a file, on Linux and macOS
rec SES-00/01-verify-me aws ses verify-email-identity --email-address "$M"
# open the link in the email "Amazon Web Services - Email Address Verification Request in region ...", then:
rec SES-00/02-me-verified aws ses get-identity-verification-attributes --identities "$M"   # expect Success
```

Shared teardown, after the last case:

```sh
for i in $(aws ses list-identities --query "Identities[?contains(@, '$P')]" --output text); do aws ses delete-identity --identity "$i"; done
for t in $(aws ses list-templates --query "TemplatesMetadata[?starts_with(Name, '$P')].Name" --output text); do aws ses delete-template --template-name "$t"; done
aws ses delete-identity --identity "$M"   # optional: keep it for the next session
```

### 12.2 Cases

**SES-01: v1 unverified-sender error bytes.** Settles 3.1 (envelope, `xmlns`, `Detail`), 6.1 (Region case, identity list), switch `ses.error.unverified.text`, overlay `QueryErrorEnvelope`. Expected: Code `MessageRejected`, 400, text of 6.1.

```sh
rec SES-01/01-cli aws ses send-email --from "$P-nobody@example.com" --destination ToAddresses=success@simulator.amazonses.com --message "$MSG"
q SES-01/02-raw SendEmail "Source=Nobody <$P-nobody@example.com>" Destination.ToAddresses.member.1=success@simulator.amazonses.com Message.Subject.Data=s Message.Body.Text.Data=b
```
Cost 0, 3 min. Teardown: none.

**SES-02: v2 unverified-sender error bytes.** Settles 3.2 (error header and body), the v2 side of `ses.error.unverified.text`.

```sh
rec SES-02/01-cli aws sesv2 send-email --from-email-address "$P-nobody@example.com" --destination ToAddresses=success@simulator.amazonses.com --content "Simple={$MSG}"
v2 SES-02/02-raw POST /v2/email/outbound-emails "{\"FromEmailAddress\":\"$P-nobody@example.com\",\"Destination\":{\"ToAddresses\":[\"success@simulator.amazonses.com\"]},\"Content\":{\"Simple\":{\"Subject\":{\"Data\":\"s\"},\"Body\":{\"Text\":{\"Data\":\"b\"}}}}}"
```
Cost 0, 3 min. Teardown: none.

**SES-03: which identities the sandbox error lists.** Settles 6.1 and the order part of `ses.error.unverified.text`. Needs the account in the sandbox (`aws sesv2 get-account --query ProductionAccessEnabled` is false).

```sh
rec SES-03/01-rcpt-only aws ses send-email --from "$M" --destination "ToAddresses=$P-r1@example.com,$P-r2@example.com" --message "$MSG"
rec SES-03/02-both aws ses send-email --from "$P-nobody@example.com" --destination "ToAddresses=$P-r1@example.com,CcAddresses=$P-r2@example.com" --return-path "$P-rp@example.com" --message "$MSG"
rec SES-03/03-v2-both aws sesv2 send-email --from-email-address "$P-nobody@example.com" --destination "ToAddresses=$P-r1@example.com" --content "Simple={$MSG}"
rec SES-03/04-reply-to aws ses send-email --from "$M" --destination ToAddresses=success@simulator.amazonses.com --reply-to-addresses "$P-rt@example.com" --message "$MSG"
```
Step 4 settles whether an unverified Reply-To is checked (section 10). Cost at most USD 0.0001 (step 4, if accepted), 3 min. Teardown: none.

**SES-04: invalid address texts.** Settles 4.6 and 11.5 address texts.

```sh
for a in 'not-an-email' 'a b@example.com' 'user@example..com' 'user@example.com.' "$(printf '\303\274ser@example.com')"; do
  n=$(printf %s "$a" | tr -c 'a-z' _); rec "SES-04/v1-to-$n" aws ses send-email --from "$M" --destination "ToAddresses=$a" --message "$MSG"
  rec "SES-04/v2-to-$n" aws sesv2 send-email --from-email-address "$M" --destination "ToAddresses=$a" --content "Simple={$MSG}"
done
q SES-04/v1-empty-source SendEmail Source= Destination.ToAddresses.member.1=success@simulator.amazonses.com Message.Subject.Data=s Message.Body.Text.Data=b
rec SES-04/v1-verify-bad aws ses verify-email-identity --email-address not-an-email
```
Cost 0 (all rejected; if one is accepted, USD 0.0001), 10 min. Teardown: none.

**SES-05: no recipients, empty body.** Settles 4.1 and 4.6 (no recipients) and the empty-`Body` question.

```sh
q SES-05/01-v1-none SendEmail "Source=$M" Message.Subject.Data=s Message.Body.Text.Data=b
rec SES-05/02-v2-none aws sesv2 send-email --from-email-address "$M" --content "Simple={$MSG}"
q SES-05/03-v1-empty-body SendEmail "Source=$M" Destination.ToAddresses.member.1=success@simulator.amazonses.com Message.Subject.Data=s
```
Cost at most USD 0.0001, 3 min. Teardown: none.

**SES-06: 51 recipients.** Settles the 50-recipient text (4.1).

```sh
to=$(seq -f "success+%g@simulator.amazonses.com" 1 51 | paste -sd, -)
rec SES-06/01-v1-51 aws ses send-email --from "$M" --destination "ToAddresses=$to" --message "$MSG"
rec SES-06/02-v2-51 aws sesv2 send-email --from-email-address "$M" --destination "ToAddresses=$to" --content "Simple={$MSG}"
```
Cost 0 if rejected, USD 0.0102 if both are accepted; 3 min. Teardown: none.

**SES-07: MessageId format.** Settles 4.7 and switch `ses.message-id.format`. Compare the first 16 hex digits with the response `Date` header, for each API.

```sh
rec SES-07/01-v1 aws ses send-email --from "$M" --destination ToAddresses=success@simulator.amazonses.com --message "$MSG"
printf 'From: %s\nTo: success@simulator.amazonses.com\nSubject: raw\n\nbody\n' "$M" > "$REC/raw.eml"
rec SES-07/02-v1-raw aws ses send-raw-email --raw-message "{\"Data\":\"$(b64 "$REC/raw.eml")\"}"
rec SES-07/03-v2 aws sesv2 send-email --from-email-address "$M" --destination ToAddresses=success@simulator.amazonses.com --content "Simple={$MSG}"
```
Cost USD 0.0003, 5 min. Teardown: none.

**SES-08: GetIdentityVerificationAttributes edges.** Settles 5.3 and switch `ses.verification.unknown-identity`.

```sh
rec SES-08/01-mixed aws ses get-identity-verification-attributes --identities "$M" "$P-unknown@example.com" "$P-unknown.example.com"
rec SES-08/02-101 aws ses get-identity-verification-attributes --identities $(seq -f "$P-x%g@example.com" 1 101)
q SES-08/03-notif-raw GetIdentityNotificationAttributes "Identities.member.1=$M" "Identities.member.2=$P-unknown@example.com"
```

Also settles 9.1 (notification attributes of an identity with no topics, and of an unknown identity).
Cost 0, 3 min. Teardown: none.

**SES-09: VerifyEmailIdentity repeats and the legacy list.** Settles 5.3 (repeat behavior, `ListVerifiedEmailAddresses`). Run last among the sending cases, because step 3 may set `$M` back to `Pending` until you click again.

```sh
rec SES-09/01-new aws ses verify-email-identity --email-address "$P-pending@example.com"
rec SES-09/02-again aws ses verify-email-identity --email-address "$P-pending@example.com"
rec SES-09/03-verified-again aws ses verify-email-identity --email-address "$M"
rec SES-09/04-attrs aws ses get-identity-verification-attributes --identities "$M" "$P-pending@example.com"
rec SES-09/05-legacy-list aws ses list-verified-email-addresses
```
Cost 0, 5 min (plus clicking the new link for `$M` if step 4 shows `Pending`). Teardown: shared.

**SES-10: domain token stability.** Settles 5.3 (token repeat), 9.2.

```sh
rec SES-10/01-verify aws ses verify-domain-identity --domain "$P.example.com"
rec SES-10/02-again aws ses verify-domain-identity --domain "$P.example.com"
rec SES-10/03-attrs aws ses get-identity-verification-attributes --identities "$P.example.com"
```
Cost 0, 2 min. Teardown: shared.

**SES-11: missing and duplicate identities.** Settles 5.3 (`DeleteIdentity` unknown) and 5.4 (v2 texts, and the `CreateEmailIdentity` response for an address in step 4).

```sh
rec SES-11/01-v1-delete-unknown aws ses delete-identity --identity "$P-never@example.com"
rec SES-11/02-v2-get-unknown aws sesv2 get-email-identity --email-identity "$P-never@example.com"
rec SES-11/03-v2-delete-unknown aws sesv2 delete-email-identity --email-identity "$P-never@example.com"
rec SES-11/04-v2-create aws sesv2 create-email-identity --email-identity "$P-dup@example.com"
rec SES-11/05-v2-create-again aws sesv2 create-email-identity --email-identity "$P-dup@example.com"
```
Cost 0, 3 min. Teardown: shared.

**SES-12: one store or two.** Settles 2.3, 7 and switch `ses.store.shared-v1-v2`.

```sh
rec SES-12/01-v1-create aws ses verify-email-identity --email-address "$P-v1@example.com"
rec SES-12/02-v2-read aws sesv2 get-email-identity --email-identity "$P-v1@example.com"
rec SES-12/03-v2-create aws sesv2 create-email-identity --email-identity "$P-v2@example.com"
rec SES-12/04-v1-read aws ses get-identity-verification-attributes --identities "$P-v2@example.com"
rec SES-12/05-v1-tmpl aws ses create-template --template "TemplateName=$P-t1,SubjectPart=s,TextPart=t"
rec SES-12/06-v2-tmpl-read aws sesv2 get-email-template --template-name "$P-t1"
rec SES-12/07-v2-tmpl aws sesv2 create-email-template --template-name "$P-t2" --template-content 'Subject=s,Text=t'
rec SES-12/08-v1-tmpl-read aws ses get-template --template-name "$P-t2"
```
Cost 0, 5 min. Teardown: shared, plus `aws sesv2 delete-email-template --template-name "$P-t2"` if the v1 listing does not show it.

**SES-13: GetEmailIdentity raw JSON.** Settles 5.4 (absent versus empty members) and 9.3.

```sh
v2 SES-13/01-email GET "/v2/email/identities/$P-v2@example.com"
rec SES-13/02-domain-create aws sesv2 create-email-identity --email-identity "$P.example.org"
v2 SES-13/03-domain GET "/v2/email/identities/$P.example.org"
v2 SES-13/04-domain-again GET "/v2/email/identities/$P.example.org"
```
Cost 0, 3 min. Teardown: shared (`$P.example.org` matches the prefix filter).

**SES-14: listing order, tokens, both v2 bindings.** Settles 5.3 (order, `NextToken`) and 3.2 (the server still accepts the GET form).

```sh
q SES-14/01-v1-page ListIdentities MaxItems=2
v2 SES-14/02-v2-post POST /v2/email/list-identities '{"PageSize":2}'
v2 SES-14/03-v2-get GET '/v2/email/identities?PageSize=2'
v2 SES-14/04-v2-filter POST /v2/email/list-identities "{\"Filter\":{\"IDENTITY_NAME_CONTAINS\":\"$P\"}}"
```
Cost 0, 3 min. Teardown: none.

**SES-15: GetAccount of a sandbox account.** Settles 6.3.

```sh
v2 SES-15/01-account GET /v2/email/account
rec SES-15/02-quota aws ses get-send-quota
```
Cost 0, 1 min. Teardown: none.

**SES-16: tag boundaries and `ses:` names.** Settles 4.5 and switches `ses.tags.max-length`, `ses.tags.ses-prefix`. An unverified sender makes a tag that passes fail `MessageRejected`, so nothing is sent.

```sh
n255=$(printf 'a%.0s' $(seq 255)); n256=${n255}a
i=0   # steps 1-7: name 255, name 256, value 255, value 256, ses:feedback-id-c, ses:custom, value with . and @
for t in "Name=$n255,Value=v" "Name=$n256,Value=v" "Name=k,Value=$n255" "Name=k,Value=$n256" 'Name=ses:feedback-id-c,Value=v' 'Name=ses:custom,Value=v' 'Name=k,Value=a.b@c'; do
  i=$((i+1)); rec "SES-16/0$i" aws ses send-email --from "$P-nobody@example.com" --destination ToAddresses=success@simulator.amazonses.com --message "$MSG" --tags "$t"
done
rec SES-16/v2-256 aws sesv2 send-email --from-email-address "$P-nobody@example.com" --destination ToAddresses=success@simulator.amazonses.com --content "Simple={$MSG}" --email-tags "Name=$n256,Value=v"
```
Cost 0, 5 min. Teardown: none.

**SES-17: missing configuration set and check order.** Settles 8 and switch `ses.send.check-order` (configuration set against sender).

```sh
rec SES-17/01-verified aws ses send-email --from "$M" --destination ToAddresses=success@simulator.amazonses.com --message "$MSG" --configuration-set-name "$P-missing"
rec SES-17/02-unverified aws ses send-email --from "$P-nobody@example.com" --destination ToAddresses=success@simulator.amazonses.com --message "$MSG" --configuration-set-name "$P-missing"
rec SES-17/03-v2 aws sesv2 send-email --from-email-address "$M" --destination ToAddresses=success@simulator.amazonses.com --content "Simple={$MSG}" --configuration-set-name "$P-missing"
rec SES-17/04-bad-tag-and-set aws ses send-email --from "$M" --destination ToAddresses=success@simulator.amazonses.com --message "$MSG" --configuration-set-name "$P-missing" --tags 'Name=a:b,Value=v'
```
Cost 0, 3 min. Teardown: none.

**SES-18: template errors.** Settles 7.

```sh
rec SES-18/01-create aws ses create-template --template "{\"TemplateName\":\"$P-t3\",\"SubjectPart\":\"Hi {{name}}\",\"TextPart\":\"Dear {{name}}\"}"
rec SES-18/02-dup aws ses create-template --template "{\"TemplateName\":\"$P-t3\",\"SubjectPart\":\"s\"}"
rec SES-18/03-get-missing aws ses get-template --template-name "$P-none"
rec SES-18/04-send-missing aws ses send-templated-email --source "$M" --destination ToAddresses=success@simulator.amazonses.com --template "$P-none" --template-data '{}'
rec SES-18/05-v2-get-missing aws sesv2 get-email-template --template-name "$P-none"
rec SES-18/06-v2-dup aws sesv2 create-email-template --template-name "$P-t3" --template-content 'Subject=s'
rec SES-18/07-send-ok aws ses send-templated-email --source "$M" --destination ToAddresses=success@simulator.amazonses.com --template "$P-t3" --template-data '{"name":"x"}'
```
Cost USD 0.0001, 4 min. Teardown: shared.

**SES-19: simulator matching and labeled recipients.** Settles 6.2 and switches `ses.simulator.addresses`, `ses.sandbox.recipient-labels`. Sandbox account.

```sh
for r in "success+$P@simulator.amazonses.com" 'SUCCESS@simulator.amazonses.com' 'success@SIMULATOR.AMAZONSES.COM' "other-$P@simulator.amazonses.com"; do
  rec "SES-19/$(printf %s "$r" | tr -c 'a-zA-Z0-9' _)" aws ses send-email --from "$M" --destination "ToAddresses=$r" --message "$MSG"
done
lab=$(printf %s "$M" | sed "s/@/+$P@/")
rec SES-19/labeled-me aws ses send-email --from "$M" --destination "ToAddresses=$lab" --message "$MSG"
```
Cost at most USD 0.0005 (the labeled-me step lands in your inbox), 3 min. Teardown: delete the test mail from your inbox.

**SES-20: raw envelope and missing sender.** Settles 4.2 and switch `ses.raw.envelope`.

```sh
printf 'From: %s\nTo: success@simulator.amazonses.com\nSubject: r1\n\nb\n' "$M" > "$REC/r1.eml"
printf 'From: %s\nTo: %s-rcpt@example.com\nSubject: r2\n\nb\n' "$M" "$P" > "$REC/r2.eml"
printf 'To: success@simulator.amazonses.com\nSubject: r3\n\nb\n' > "$REC/r3.eml"
rec SES-20/01-headers-only aws ses send-raw-email --raw-message "{\"Data\":\"$(b64 "$REC/r1.eml")\"}"
rec SES-20/02-dest-overrides aws ses send-raw-email --destinations success@simulator.amazonses.com --raw-message "{\"Data\":\"$(b64 "$REC/r2.eml")\"}"
rec SES-20/03-no-sender aws ses send-raw-email --raw-message "{\"Data\":\"$(b64 "$REC/r3.eml")\"}"
rec SES-20/04-v2-headers-only aws sesv2 send-email --content "{\"Raw\":{\"Data\":\"$(b64 "$REC/r1.eml")\"}}"
```
Cost at most USD 0.0003, 4 min. Teardown: none.

**SES-21: send-rate throttling.** Settles 3.3 (status), 6.3 (burst) and switch `ses.quota.send-rate`. Sandbox account (1 per second).

```sh
for i in $(seq 1 10); do
  q "SES-21/burst-$i" SendEmail "Source=$M" Destination.ToAddresses.member.1=success@simulator.amazonses.com Message.Subject.Data=s Message.Body.Text.Data=b &
done; wait
grep -h -E '^HTTP/|<Code>|<Message>' "$REC"/SES-21/*.http
```
Cost at most USD 0.001, 3 min. Teardown: none.

**SES-22 (optional): daily quota exceeded.** Settles the daily-quota Code and status (6.3). Sends 201 messages to your own inbox at one per second.

```sh
for i in $(seq 1 201); do aws ses send-email --from "$M" --destination "ToAddresses=$M" --message "$MSG" >/dev/null 2>&1; sleep 1.1; done
rec SES-22/01-over aws ses send-email --from "$M" --destination "ToAddresses=$M" --message "$MSG"
```
Cost USD 0.02, 5 min. Teardown: delete the 201 messages from the inbox; the quota resets within 24 hours.

**SES-23: v2 content shape errors.** Settles 4.4. Run after SES-20: step 2 reuses `$REC/r1.eml`.

```sh
v2 SES-23/01-none POST /v2/email/outbound-emails "{\"FromEmailAddress\":\"$M\",\"Destination\":{\"ToAddresses\":[\"success@simulator.amazonses.com\"]},\"Content\":{}}"
v2 SES-23/02-two POST /v2/email/outbound-emails "{\"FromEmailAddress\":\"$M\",\"Destination\":{\"ToAddresses\":[\"success@simulator.amazonses.com\"]},\"Content\":{\"Simple\":{\"Subject\":{\"Data\":\"s\"},\"Body\":{\"Text\":{\"Data\":\"b\"}}},\"Raw\":{\"Data\":\"$(b64 "$REC/r1.eml")\"}}}"
v2 SES-23/03-no-from POST /v2/email/outbound-emails '{"Destination":{"ToAddresses":["success@simulator.amazonses.com"]},"Content":{"Simple":{"Subject":{"Data":"s"},"Body":{"Text":{"Data":"b"}}}}}'
```
Cost at most USD 0.0002, 3 min. Teardown: none.

**SES-24: unknown action and unknown path.** Settles 3.1 and 3.2 unknown-operation answers and 11.6 step 3.

```sh
q SES-24/01-v1-unknown FooBar
v2 SES-24/02-v2-unknown GET /v2/email/nonexistent
```
Cost 0, 1 min. Teardown: none.

**SES-25 (optional, needs a domain whose DNS you control): inherited verification with a configuration set.** Settles 5.1 and 8. Publish the three DKIM CNAMEs that `aws sesv2 get-email-identity --email-identity <your-domain>` lists, wait for `SUCCESS`, then:

```sh
aws sesv2 create-configuration-set --configuration-set-name "$P-cs"
rec SES-25/01-inherited-with-set aws ses send-email --from "$P@<your-domain>" --destination ToAddresses=success@simulator.amazonses.com --message "$MSG" --configuration-set-name "$P-cs"
rec SES-25/02-inherited-plain aws ses send-email --from "$P@<your-domain>" --destination ToAddresses=success@simulator.amazonses.com --message "$MSG"
```
Cost USD 0.0002, 15 min to 72 h (DNS). Teardown: `aws sesv2 delete-configuration-set --configuration-set-name "$P-cs"`, `aws sesv2 delete-email-identity --email-identity <your-domain>`, remove the CNAMEs.

**SES-26 (needs an account with production access, not the throwaway sandbox account): production quota.** Settles switch `ses.quota.production`.

```sh
rec SES-26/01-quota aws ses get-send-quota
v2 SES-26/02-account GET /v2/email/account
```
Cost 0, 1 min. Teardown: none. Mask the account id before sharing.

**SES-27 (optional, 25 hours): verification expiry.** Settles 5.2 timing and switch `ses.verification.expiry`.

```sh
rec SES-27/01-start aws ses verify-email-identity --email-address "$P-expire@example.com"
date -u +%FT%TZ > "$REC/SES-27/started"
# 25 hours later, in a new recording shell:
rec SES-27/02-after aws ses get-identity-verification-attributes --identities "$P-expire@example.com"
```
Cost 0, 2 min over 25 h. Teardown: shared.

**SES-28: denial identity and the action v2 Raw checks.** Settles switch `ses.denial.identity` ([iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 12.3, the SES v1 and v2 rows) and which action v2 `SendEmail` with `Content.Raw` is authorized as (the denial message names it). Uses the `$P-enf-none` session of [iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 13.1 (`enf_assume`, `as_role`, `enf_curl`), which holds no permissions; run that setup first. Run after SES-20: step 4 reuses `$REC/r1.eml`.

```sh
enf_assume "$P-enf-none"
rec SES-28/01-v1-send as_role ses send-email --from "$M" --destination ToAddresses=success@simulator.amazonses.com --message "$MSG"
SVC=ses enf_curl SES-28/02-v1-raw -H 'Content-Type: application/x-www-form-urlencoded' --data-urlencode Action=SendEmail --data-urlencode Version=2010-12-01 --data-urlencode "Source=$M" --data-urlencode Destination.ToAddresses.member.1=success@simulator.amazonses.com --data-urlencode Message.Subject.Data=s --data-urlencode Message.Body.Text.Data=b "https://email.$R.amazonaws.com/"
SVC=ses enf_curl SES-28/03-v2-simple -X POST -H 'Content-Type: application/json' -d "{\"FromEmailAddress\":\"$M\",\"Destination\":{\"ToAddresses\":[\"success@simulator.amazonses.com\"]},\"Content\":{\"Simple\":{\"Subject\":{\"Data\":\"s\"},\"Body\":{\"Text\":{\"Data\":\"b\"}}}}}" "https://email.$R.amazonaws.com/v2/email/outbound-emails"
SVC=ses enf_curl SES-28/04-v2-raw -X POST -H 'Content-Type: application/json' -d "{\"Content\":{\"Raw\":{\"Data\":\"$(b64 "$REC/r1.eml")\"}}}" "https://email.$R.amazonaws.com/v2/email/outbound-emails"
```
Record status, `x-amzn-ErrorType`, `x-amzn-query-error`, and the body (`<Code>`, `<Type>`, message key and text). Cost 0 (every call is denied), 5 min. Teardown: [iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 13.2.

**SES-29: size, line, part, paging and template limits.** Settles the error identities and texts of the 11.5 limits that no source records: message size (10 MB v1, 40 MB v2), the 1,000-character line, 500 MIME parts, `ListIdentities` `MaxItems` outside 1 to 1,000, and a template over 500 KB. Every call is expected to be rejected; one that is accepted goes to the simulator.

```sh
hdr() { printf 'From: %s\r\nTo: success@simulator.amazonses.com\r\nSubject: s\r\n' "$M"; }
{ hdr; printf 'Content-Type: text/plain\r\n\r\n'; head -c $((10*1024*1024 + 1)) /dev/zero | tr '\0' a | fold -w 76; } > "$REC/big10.eml"
{ hdr; printf 'Content-Type: text/plain\r\n\r\n'; head -c $((40*1024*1024 + 1)) /dev/zero | tr '\0' a | fold -w 76; } > "$REC/big40.eml"
{ hdr; printf 'Content-Type: text/plain\r\n\r\n'; head -c 1001 /dev/zero | tr '\0' a; printf '\r\n'; } > "$REC/line.eml"
{ hdr; printf 'MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n'; for i in $(seq 501); do printf -- '--B\r\nContent-Type: text/plain\r\n\r\np%s\r\n' "$i"; done; printf -- '--B--\r\n'; } > "$REC/parts.eml"
for f in big10 line parts; do printf '{"Data":"%s"}' "$(b64 "$REC/$f.eml")" > "$REC/$f.json"; rec "SES-29/v1-$f" aws ses send-raw-email --raw-message "file://$REC/$f.json"; done
printf '{"Content":{"Raw":{"Data":"%s"}}}' "$(b64 "$REC/big40.eml")" > "$REC/big40.json"
rec SES-29/v2-big40 aws sesv2 send-email --cli-input-json "file://$REC/big40.json"
rec SES-29/list-max-0 aws ses list-identities --max-items 0 --no-paginate
q SES-29/list-max-0-raw ListIdentities MaxItems=0
q SES-29/list-max-1001-raw ListIdentities MaxItems=1001
head -c $((500*1024 + 1)) /dev/zero | tr '\0' a > "$REC/tmpl.txt"
printf '{"Template":{"TemplateName":"%s-big","SubjectPart":"s","TextPart":"%s"}}' "$P" "$(cat "$REC/tmpl.txt")" > "$REC/tmpl.json"
rec SES-29/template-over aws ses create-template --cli-input-json "file://$REC/tmpl.json"
```
The `--max-items` of the CLI is client-side paging, so the raw calls are the evidence for `MaxItems`. Whether 500 KB means 512,000 or 500,000 bytes is part of what this settles; if the 512,001-byte template is accepted, rerun with 500,001 bytes and record both. Cost 0 if all are rejected, else USD 0.0001 per accepted send, 10 min. Teardown: `aws ses delete-template --template-name "$P-big"` if it was created, and the shared teardown.

[m1]: https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/ses/service/2010-12-01/ses-2010-12-01.json
[m2]: https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sesv2/service/2019-09-27/sesv2-2019-09-27.json
[m1-sig]: https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/ses/service/2010-12-01/ses-2010-12-01.json#L6495
[m2-sig]: https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sesv2/service/2019-09-27/sesv2-2019-09-27.json#L13539
[m2-old]: https://github.com/aws/api-models-aws/blob/a877f97c8964bc3d6a195c68188665ae636acb65/models/sesv2/service/2019-09-27/sesv2-2019-09-27.json
[m2-new]: https://github.com/aws/api-models-aws/blob/430b75cb8ac99333756768b446fafc02ce5a356c/models/sesv2/service/2019-09-27/sesv2-2019-09-27.json
[go-cl]: https://github.com/aws/aws-sdk-go-v2/blob/404700d41c3f8becfc6188a279a1cdee192badc3/service/sesv2/CHANGELOG.md
[go-schema]: https://github.com/aws/aws-sdk-go-v2/blob/404700d41c3f8becfc6188a279a1cdee192badc3/service/sesv2/schemas/schemas.go#L459-L464
[go-schema-76]: https://github.com/aws/aws-sdk-go-v2/blob/service/sesv2/v1.76.0/service/sesv2/schemas/schemas.go#L459-L463
[tf-gomod]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/go.mod#L262
[tf-v2-sweep]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sesv2/sweep.go#L26-L29
[tf-ei]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/ses/email_identity.go#L46-L73
[tf-ei-find]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/ses/email_identity.go#L75-L148
[tf-ei-del]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/ses/email_identity.go#L98-L113
[tf-ei-arn]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/ses/email_identity.go#L146-L148
[tf-di]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/ses/domain_identity.go#L41-L98
[tf-div]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/ses/domain_identity_verification.go#L47-L106
[tf-ei-ds]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/ses/email_identity_data_source.go#L48
[tf-notif]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/ses/identity_notification_topic.go#L196-L225
[tf-tmpl]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/ses/template.go#L44-L63
[tf-tmpl-all]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/ses/template.go#L27-L210
[tf-v2]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sesv2/email_identity.go#L137-L166
[tf-v2-read]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sesv2/email_identity.go#L168-L291
[tf-v2-upd]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sesv2/email_identity.go#L206-L243
[tf-v2-del]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sesv2/email_identity.go#L245-L263
[tf-v2-arn]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sesv2/email_identity.go#L358-L360
[tf-v2-test]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sesv2/email_identity_test.go#L23-L89
[tf-v2-tags]: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sesv2/tags_gen.go#L26-L130
[lss]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.snapshot.json
[lsv]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.validation.json
[ls-tags]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.snapshot.json#L592-L735
[ls-tags-val]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.validation.json#L35-L85
[ls-tags-test]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.py#L886-L923
[ls-special]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.py#L925-L953
[ls-raw-test]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.py#L1162-L1170
[ls-raw-val]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.validation.json#L89-L97
[ls-quota]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.snapshot.json#L905-L918
[ls-quota-test]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.py#L234-L289
[ls-tmpl]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.snapshot.json#L736-L807
[ls-cs]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.snapshot.json#L494-L590
[ls-cs-test]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.py#L840-L854
[ls-event]: https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/ses/test_ses.snapshot.json#L2-L186
[boto2]: https://github.com/boto/boto/blob/5506aba84638f921bc84b125f78d5e1c600d14fb/boto/ses/connection.py#L130-L170
[c-ymir]: https://github.com/ymirapp/wordpress-plugin/blob/9b795d6ce73e2cc2777bd0f6fb1f6f4f90a15f09/tests/Unit/CloudProvider/Aws/SesClientTest.php#L74-L100
[c-smtp]: https://github.com/sisimai/p5-sisimai/blob/783a1d307ee276e56bd23057bd6bde61eacc2edc/set-of-emails/maildir/bsd/lhost-postfix-77.eml#L54-L57
[c-pdx]: https://github.com/sisimai/p5-sisimai/blob/ebc1e516dcb7dd8d221bafd3a7a8a3ebbe3191d8/set-of-emails/maildir/bsd/lhost-amazonses-13.eml#L40
[c-iad]: https://github.com/akhan619/laravel-ses-event-manager/blob/1c3bccd1f8cd94eddfd1e1b65824d7a24d87f0db/tests/Feature/Implementations/EventManagerSubscriptionTest.php#L95-L99
[c-dub]: https://github.com/Aerendir/bundle-aws-ses-monitor/blob/2f54e15e08e83c29923fbd333b9849e75aea674d/tests/Helper/MessageHelperTest.php#L47-L48
[ex-js]: https://github.com/awsdocs/aws-doc-sdk-examples/blob/a352b0479dfad38b850d4daed9e28de4020aa30e/javascriptv3/example_code/ses/src/ses_sendemail.js#L17-L64
[ex-js-dir]: https://github.com/awsdocs/aws-doc-sdk-examples/tree/a352b0479dfad38b850d4daed9e28de4020aa30e/javascriptv3/example_code/ses/src
[ex-js-att]: https://github.com/awsdocs/aws-doc-sdk-examples/blob/a352b0479dfad38b850d4daed9e28de4020aa30e/javascriptv3/example_code/ses/src/send-with-attachments.js#L7-L14
[ex-py]: https://github.com/awsdocs/aws-doc-sdk-examples/tree/a352b0479dfad38b850d4daed9e28de4020aa30e/python/example_code
[d-sandbox]: https://docs.aws.amazon.com/ses/latest/dg/request-production-access.html
[d-err]: https://docs.aws.amazon.com/ses/latest/dg/troubleshoot-error-messages.html
[d-sim]: https://docs.aws.amazon.com/ses/latest/dg/send-an-email-from-console.html#send-email-simulator
[d-ident]: https://docs.aws.amazon.com/ses/latest/dg/creating-identities.html
[d-ident-email]: https://docs.aws.amazon.com/ses/latest/dg/creating-identities.html#default-config-set-at-create-api
[d-quotas]: https://docs.aws.amazon.com/ses/latest/dg/quotas.html
[d-raw]: https://docs.aws.amazon.com/ses/latest/dg/send-email-raw.html
[d-xses]: https://docs.aws.amazon.com/ses/latest/dg/event-publishing-send-email.html
[d-tmpl]: https://docs.aws.amazon.com/ses/latest/dg/send-personalized-email-api.html
[d-tmpl-adv]: https://docs.aws.amazon.com/ses/latest/dg/send-personalized-email-advanced.html
[d-tags]: https://docs.aws.amazon.com/ses/latest/dg/monitor-using-event-publishing.html
[d-notif]: https://docs.aws.amazon.com/ses/latest/dg/notification-examples.html
[d-cs]: https://docs.aws.amazon.com/ses/latest/dg/using-configuration-sets.html
[d-cs-manage]: https://docs.aws.amazon.com/ses/latest/dg/managing-configuration-sets.html
[d-ce1]: https://docs.aws.amazon.com/ses/latest/APIReference/CommonErrors.html
[d1-list]: https://docs.aws.amazon.com/ses/latest/APIReference/API_ListIdentities.html
[d1-ct]: https://docs.aws.amazon.com/ses/latest/APIReference/API_CreateTemplate.html
[d1-gt]: https://docs.aws.amazon.com/ses/latest/APIReference/API_GetTemplate.html
[d2-send]: https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_SendEmail.html
[d-blog]: https://aws.amazon.com/blogs/messaging-and-targeting/how-to-handle-a-throttling-maximum-sending-rate-exceeded-error/
[d-price]: https://aws.amazon.com/ses/pricing/
