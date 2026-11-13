# SES

understudy emulates SES v1 (`aws ses`) and v2 (`aws sesv2`) over one store, so an identity created with one API is read by the other. It sends no mail. An accepted send is recorded instead, and a test reads it back through the admin API or `understudy ses messages`. No delivery, bounce or complaint event or notification is produced, because nothing was delivered.

What each operation supports is on [the coverage page](../coverage.md#ses). Configuration sets, receipt rules and sending authorization answer 501.

The `aws` commands below run with `eval "$(understudy env)"` in effect, against `understudy serve --clock frozen@2026-10-30T09:00:00Z` with this `understudy.json`:

```json
{"ses": {"verifiedIdentities": ["sender@example.com", "example.org"]}}
```

## Verifying an address

A new identity is `Pending`, as on AWS, and the verification email SES would send is recorded as a message of kind `verification`:

```sh
aws ses verify-email-identity --email-address me@example.com
aws ses get-identity-verification-attributes --identities me@example.com
understudy ses messages --to me@example.com
```

```text
{
    "VerificationAttributes": {
        "me@example.com": {
            "VerificationStatus": "Pending"
        }
    }
}
ACCEPTED              KIND          OPERATION                MESSAGE ID                                                    FROM                     TO              SUBJECT
2026-10-30T09:00:00Z  verification  ses.VerifyEmailIdentity  010001a18c099280-9716e924-ab4f-46d0-9631-b7bf8834b094-000000  no-reply-aws@amazon.com  me@example.com  Amazon Web Services - Email Address Verification Request in region us-east-1
```

Its text body carries a link under `/_understudy/ses/VerificationLink`. Opening it is the owner clicking the link in the email:

```sh
link=$(understudy ses messages --to me@example.com --output json \
  | jq -r '.messages[0].text.data' | grep -o 'http://[^ ]*VerificationLink[^ ]*')
curl -s "$link"
aws ses get-identity-verification-attributes --identities me@example.com
```

```text
understudy verified the SES identity me@example.com.
{
    "VerificationAttributes": {
        "me@example.com": {
            "VerificationStatus": "Success"
        }
    }
}
```

The link is built from the address the client called, as SNS's confirmation links are, so a test in a container reaches it the same way it reaches understudy.

Verification never completes on its own. An address left `Pending` becomes `Failed` 24 hours after its verification email, and a domain 72 hours after it was created, both on the emulator clock:

```sh
aws ses verify-email-identity --email-address late@example.com
understudy clock advance 24h
aws ses get-identity-verification-attributes --identities late@example.com
```

```text
2026-10-31T09:00:00Z frozen
fired 1 timer(s):
  ses.verification-expiry ses arn:aws:ses:us-east-1:000000000000:identity/late@example.com
{
    "VerificationAttributes": {
        "late@example.com": {
            "VerificationStatus": "Failed"
        }
    }
}
```

Each change of status is a `state-change` event in [the journal](journal.md) on the identity's ARN.

## Identities you own: `ses.verifiedIdentities`

Domains have no email to click, and understudy never looks up DNS. The `ses.verifiedIdentities` file key lists identities that start `Success` when they are created, in any account and region: exact addresses (case-sensitive), domains (case-insensitive) and `*@domain` patterns. Listing an identity is your statement that you own it; understudy checks nothing else.

```sh
aws ses verify-domain-identity --domain example.org
aws ses get-identity-verification-attributes --identities example.org
```

```text
{
    "VerificationToken": "1//B3aLVzs7DZfYtlErMe9uEmebwIV2jTN3Zdn8r0VA="
}
{
    "VerificationAttributes": {
        "example.org": {
            "VerificationStatus": "Success",
            "VerificationToken": "1//B3aLVzs7DZfYtlErMe9uEmebwIV2jTN3Zdn8r0VA="
        }
    }
}
```

The list applies when an identity is created, and an identity verified this way gets no verification email. An entry in any other form stops `serve`, which names it.

### Terraform's 45-minute wait

`aws_ses_domain_identity_verification` polls until the domain reads `Success`, for up to 45 minutes. A domain understudy cannot verify stays `Pending`, so the apply waits out the whole timeout and then fails. List every domain such a resource names in `ses.verifiedIdentities`, and the resource completes at once. `aws_ses_email_identity` and `aws_sesv2_email_identity` do not wait: the provider accepts a `Pending` identity.

## The sandbox

Each account and region starts in the sandbox, as on AWS. The sender must be verified, and so must every recipient unless it is a mailbox simulator address such as `success@simulator.amazonses.com`:

```sh
aws ses verify-email-identity --email-address sender@example.com
aws ses send-email --from sender@example.com --destination ToAddresses=someone@example.net \
  --message 'Subject={Data=Hi},Body={Text={Data=Hello}}'
aws ses send-email --from sender@example.com --destination ToAddresses=success@simulator.amazonses.com \
  --message 'Subject={Data=Hi},Body={Text={Data=Hello}}'
```

```text
An error occurred (MessageRejected) when calling the SendEmail operation: Email address is not verified. The following identities failed the check in region US-EAST-1: someone@example.net
{
    "MessageId": "010001a18c099280-7f90af8b-7d6c-4688-a51f-191e0eacd8d0-000000"
}
```

A simulator address is accepted and recorded with its scenario, but does not count toward the quota. In the sandbox the quota is 200 recipients a day at 1 a second; the rate is not enforced.

```sh
aws ses get-send-quota
```

```text
{
    "Max24HourSend": 200.0,
    "MaxSendRate": 1.0,
    "SentLast24Hours": 0.0
}
```

## Production access

`"productionAccess": true` under `ses` takes every account and region out of the sandbox. The recipient check stops, while the sender check still applies:

```json
{"ses": {"verifiedIdentities": ["sender@example.com"], "productionAccess": true}}
```

```sh
aws ses verify-email-identity --email-address sender@example.com
aws ses send-email --from sender@example.com --destination ToAddresses=someone@example.net \
  --message 'Subject={Data=Hi},Body={Text={Data=Hello}}'
aws ses get-send-quota
```

```text
{
    "MessageId": "010001a18c099280-9716e924-ab4f-46d0-9631-b7bf8834b094-000000"
}
{
    "Max24HourSend": -1.0,
    "MaxSendRate": 14.0,
    "SentLast24Hours": 1.0
}
```

To lift the sandbox for some accounts and regions only, give `productionAccess` a list of `{"account", "region"}` pairs. Every other pair keeps the sandbox recipient check and quota, and `get-send-quota` and the v2 `GetAccount` answer per pair:

```json
{"ses": {"productionAccess": [{"account": "000000000000", "region": "us-east-1"}]}}
```

An entry with a missing or malformed account or region, or any value that is not a bool or a list, is refused at start with an error naming `ses.productionAccess` and the entry.

The production quota values, -1 and 14, are not recorded from AWS. `aws sesv2 get-account` reports the same quota with `ProductionAccessEnabled`.

## Reading messages

`understudy ses messages` lists recorded messages oldest first, filtered by `--to` (any To, Cc, Bcc or envelope recipient), `--from`, `--message-id`, `--account`, `--region` and `--since` (an RFC 3339 emulator time). `--limit` caps how many print, 100 by default.

```sh
understudy ses messages --from sender@example.com
understudy ses messages --from sender@example.com --output json
```

`--follow` prints the matching messages, with no `--limit`, then each new one that matches the same filters as it is accepted, until interrupted (exit 0). With `--output json` it prints one message per line. In a second terminal, after one send already recorded:

```sh
understudy ses messages --to success@simulator.amazonses.com --follow
aws ses send-email --from sender@example.com --to success@simulator.amazonses.com --subject Later --text body
```

```text
ACCEPTED              KIND  OPERATION      MESSAGE ID                                                    FROM                TO                               SUBJECT
2026-10-30T09:00:00Z  send  ses.SendEmail  010001a18c099280-00f1ab18-46fb-4a96-bf56-04d85ceacdf6-000000  sender@example.com  success@simulator.amazonses.com  Hi
2026-10-30T09:00:00Z  send  ses.SendEmail  010001a18c099280-3f3e4793-3187-4fa8-8baf-4391b9d1d782-000000  sender@example.com  success@simulator.amazonses.com  Later
```

```text
ACCEPTED              KIND  OPERATION      MESSAGE ID                                                    FROM                TO                               SUBJECT
2026-10-30T09:00:00Z  send  ses.SendEmail  010001a18c099280-7f90af8b-7d6c-4688-a51f-191e0eacd8d0-000000  sender@example.com  success@simulator.amazonses.com  Hi
{
  "messages": [
    {
      "messageId": "010001a18c099280-7f90af8b-7d6c-4688-a51f-191e0eacd8d0-000000",
      "api": "ses",
      "operation": "SendEmail",
      "requestId": "b7462fb7-0aff-4488-9a2b-ee7e1824e469",
      "account": "000000000000",
      "region": "us-east-1",
      "accepted": "2026-10-30T09:00:00Z",
      "kind": "send",
      "source": "sender@example.com",
      "from": [
        "sender@example.com"
      ],
      "to": [
        "success@simulator.amazonses.com"
      ],
      "envelope": [
        "success@simulator.amazonses.com"
      ],
      "subject": {
        "data": "Hi"
      },
      "text": {
        "data": "Hello"
      },
      "simulator": {
        "success@simulator.amazonses.com": "success"
      }
    }
  ],
  "evicted": 0
}
```

A raw send records its MIME as received (`raw`, base64), and a v2 send its `headers` and `attachments`. `requestId` finds the call with `understudy journal --request-id`. The journal itself keeps no subject, body, attachment or raw MIME.

From a test, read the same messages with `GET /_understudy/v1/ses/messages`, which takes the filters as `to`, `from`, `messageId`, `account`, `region`, `since`, `limit` and `cursor`, or with `admin.Client.SESMessages` in Go. `DELETE` with the same filters clears messages between tests (`admin.Client.ClearSESMessages`). It leaves the quota counter alone, since the mail counts as sent:

```sh
curl -s -X DELETE http://127.0.0.1:4566/_understudy/v1/ses/messages
aws ses get-send-quota
```

```text
{"deleted":1}
{
    "Max24HourSend": -1.0,
    "MaxSendRate": 14.0,
    "SentLast24Hours": 1.0
}
```

The store keeps 10,000 messages per account and region and evicts the oldest; `evicted` counts them. `understudy reset --service ses` clears identities, templates, messages and the counter for both APIs.

## Templated sends

`aws ses send-templated-email`, and `aws sesv2 send-email` with `Template` content (a stored template's name or inline content), render `{{name}}` and dotted paths such as `{{order.id}}` from the template data. SES renders with Handlebars; understudy renders only that subset. Each of these leaves its part unrendered and is listed under `template.gaps`:

- Any other construct: `{{#if}}`, `{{#each}}`, `{{{raw}}}`, partials.
- A value the data lacks.
- A string holding a character Handlebars escapes: `&`, `<`, `>`, `"`, `'`, `=` and the backtick.
- A value that is not a string, boolean or integer.

What SES renders for the last three is not recorded. The send is accepted either way, as on AWS, which accepts before it renders:

```sh
aws ses create-template --template '{"TemplateName":"welcome","SubjectPart":"Hi {{name}}","TextPart":"Your order {{order.id}} has shipped.","HtmlPart":"{{#if vip}}<p>Thanks!</p>{{/if}}"}'
aws ses send-templated-email --source sender@example.com --destination ToAddresses=success@simulator.amazonses.com \
  --template welcome --template-data '{"name":"Ana","order":{"id":"A-17"}}'
understudy ses messages --output json | jq '.messages[0] | {subject, text, html, gaps: [.template.gaps[] | {part, tag}]}'
```

```text
{
    "MessageId": "010001a18c099280-9716e924-ab4f-46d0-9631-b7bf8834b094-000000"
}
{
  "subject": {
    "data": "Hi Ana"
  },
  "text": {
    "data": "Your order A-17 has shipped."
  },
  "html": null,
  "gaps": [
    {
      "part": "html",
      "tag": "{{#if vip}}"
    },
    {
      "part": "html",
      "tag": "{{/if}}"
    }
  ]
}
```

`template` also carries the template's parts as used and the data as given. A template the account and region do not hold is `TemplateDoesNotExist` (v2: `NotFoundException`), and nothing is recorded.

v1 and v2 templates are one store: `aws sesv2 create-email-template` makes a template `aws ses send-templated-email` can use, and `aws ses create-template` one that v2 `Template` content can name. v2 templates also carry tags, read and changed through the template's ARN:

```sh
aws sesv2 create-email-template --template-name welcome \
  --template-content '{"Subject":"Hi {{name}}","Text":"Dear {{name}}"}' --tags Key=team,Value=mail
aws sesv2 list-tags-for-resource --resource-arn arn:aws:ses:us-east-1:000000000000:template/welcome
```

```json
{
    "Tags": [
        {
            "Key": "team",
            "Value": "mail"
        }
    ]
}
```

A v2 template operation on a template the account and region do not hold is `NotFoundException`, `DeleteEmailTemplate` included; v1 `DeleteTemplate` of one succeeds.

## Not implemented

A member or header understudy would have to fake answers `NotImplemented` and names it:

- `ConfigurationSetName` and the `X-SES-CONFIGURATION-SET` header.
- The sending-authorization ARNs and their `X-SES-*-ARN` headers.
- `TemplateArn` (v1) and `Template.TemplateArn` (v2).
- v2 `ConfigurationOverrides`, `EndpointId`, `TenantName` and `ListManagementOptions`.

The SDKs and the AWS CLI sign a send with `EndpointId` (`--endpoint-id`) with SigV4A, which understudy does not accept, so that send answers `UnknownOperationException` before it reaches SES. Only a SigV4-signed request gets the `NotImplemented` naming `EndpointId`.

Operations the [coverage page](../coverage.md#ses) lists as unbound answer 501:

```text
An error occurred (NotImplemented) when calling the CreateConfigurationSet operation: understudy does not implement ses.CreateConfigurationSet. Run 'understudy coverage --service ses' for what is supported; to ask for it, see https://github.com/sustinbebustin/understudy/issues?q=ses.CreateConfigurationSet
```
