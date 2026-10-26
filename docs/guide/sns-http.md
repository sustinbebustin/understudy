# SNS HTTP delivery

understudy delivers SNS `http` and `https` subscriptions as AWS does: a signed SubscriptionConfirmation when you subscribe, a signed Notification for each message, and an UnsubscribeConfirmation when the `UnsubscribeURL` is visited. It sends them only to the hosts you list. Nothing is listed by default, so an emulator that anyone on the machine can call does not become a way to make requests elsewhere.

## A first receiver

Any server that answers a POST with a 2xx can receive SNS messages. This one prints each message's type and saves its body as `<type>.json`:

```python
# receiver.py PORT: answer every POST 200, print its message type and save
# its body as <type>.json.
import http.server
import sys


class Receiver(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        kind = self.headers["x-amz-sns-message-type"]
        with open(kind + ".json", "wb") as f:
            f.write(body)
        print(kind, self.path, flush=True)
        self.send_response(200)
        self.end_headers()

    def log_message(self, *args):
        pass


http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Receiver).serve_forever()
```

Start understudy with the receiver's host and port in the allow-list and the receiver, each in a terminal of its own, then subscribe the receiver. The `aws` commands run with `eval "$(understudy env)"` in effect:

```sh
understudy serve --sns-http-delivery-allow 127.0.0.1:8080
python3 receiver.py 8080
aws sns create-topic --name orders
aws sns subscribe --topic-arn arn:aws:sns:us-east-1:000000000000:orders \
  --protocol http --notification-endpoint http://127.0.0.1:8080/orders
understudy settle
```

Deliveries run in the background, as they do on AWS; `understudy settle` returns once none is pending. The receiver prints `SubscriptionConfirmation /orders`, and the subscription stays pending until it is confirmed, either with a GET of its `SubscribeURL` or with `aws sns confirm-subscription --token`:

```sh
curl -s "$(jq -r .SubscribeURL SubscriptionConfirmation.json)"
```

```text
<ConfirmSubscriptionResponse xmlns="http://sns.amazonaws.com/doc/2010-03-31/"><ConfirmSubscriptionResult><SubscriptionArn>arn:aws:sns:us-east-1:000000000000:orders:10a888a2-a034-4896-b192-2eeb8aacae62</SubscriptionArn></ConfirmSubscriptionResult><ResponseMetadata><RequestId>67c1b0e0-4008-40bc-bab9-da95398a1a89</RequestId></ResponseMetadata></ConfirmSubscriptionResponse>
```

A message published while the subscription is pending is not delivered, and is not held for later. Once it is confirmed, publish:

```sh
aws sns publish --topic-arn arn:aws:sns:us-east-1:000000000000:orders --message 'order 42 shipped'
understudy settle
cat Notification.json
```

```text
{
  "Type" : "Notification",
  "MessageId" : "487ea094-5339-49ae-b8b1-c243689cffd2",
  "TopicArn" : "arn:aws:sns:us-east-1:000000000000:orders",
  "Message" : "order 42 shipped",
  "Timestamp" : "2026-10-26T23:44:17.389Z",
  "SignatureVersion" : "1",
  "Signature" : "JVBqeN0oS+99MibP1fqbllF83Iv3hWEq...",
  "SigningCertURL" : "http://127.0.0.1:4566/_understudy/sns/SigningCert/aws/us-east-1/000000000000/SimpleNotificationService-ab0bb0eff66122f6f0333ec8f86a1624.pem",
  "UnsubscribeURL" : "http://127.0.0.1:4566/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn=arn:aws:sns:us-east-1:000000000000:orders:10a888a2-a034-4896-b192-2eeb8aacae62"
}
```

The body has AWS's members, order and layout, and the headers are AWS's: `x-amz-sns-message-type`, `x-amz-sns-message-id`, `x-amz-sns-topic-arn`, `x-amz-sns-subscription-arn`, and `x-amz-sns-rawdelivery: true` for a subscription with `RawMessageDelivery`, whose body is the message alone. Credentials in the endpoint URL (`http://user:password@host/`) are sent as Basic authentication on every request. The links point at understudy, never at an AWS host: on the address the client that subscribed or published used, or on the TLS listener while one serves ([below](#validating-signatures)).

## What the allow-list allows

`--sns-http-delivery-allow` (`UNDERSTUDY_SNS_HTTP_DELIVERY_ALLOW`, file key `sns.httpDelivery.allow`) takes `host:port` entries: the host `localhost`, an IP literal (`[::1]` for IPv6) or a DNS name, and a port, or `*` for a receiver on an ephemeral port. [The configuration reference](../configuration.md#outbound-http-delivery) has the full grammar and matching rules. An entry understudy could never use stops `serve`:

```sh
understudy serve --sns-http-delivery-allow sns.us-east-1.amazonaws.com:443
```

```text
understudy serve: --sns-http-delivery-allow: entry 0: the host "sns.us-east-1.amazonaws.com" names an AWS endpoint, which understudy never reaches, so no entry may name it; entries are host:port, the host localhost, an IP literal ([v6] for IPv6) or a DNS name, and the port 1-65535 or *
```

The address is checked again after the name resolves, and the address checked is the one dialed:

- A `localhost` or loopback IP entry reaches loopback. A DNS-name entry never does.
- A DNS-name entry may reach a private address (RFC 1918 or IPv6 ULA). That is how a receiver in Docker Compose (`receiver:8080`) or Kubernetes (`receiver.default.svc.cluster.local:8080`) is listed by its service name.
- Whatever the list says, understudy never connects to a name under an AWS domain (in the URL or its CNAME chain), the instance metadata and ECS credentials addresses, any link-local, unspecified or multicast address, its own listeners, or a `tcp://` Docker Engine.

An endpoint that is not listed can still subscribe: `Subscribe` succeeds, but every delivery to it is refused, so it stays pending and receives nothing. A refusal is never retried and never goes to a dead-letter queue. The journal names it and the entry that would allow it:

```sh
aws sns subscribe --topic-arn arn:aws:sns:us-east-1:000000000000:orders \
  --protocol http --notification-endpoint http://127.0.0.1:9000/orders
understudy settle
understudy journal --kind delivery
```

```text
4      2026-10-26T23:44:16.976404024Z  delivery       000000000000/us-east-1 sns-to-http orders -> 127.0.0.1:8080 attempt 1 delivered
8      2026-10-26T23:44:17.389171568Z  delivery       000000000000/us-east-1 sns-to-http orders -> 127.0.0.1:8080 attempt 1 delivered
11     2026-10-26T23:44:17.764060058Z  delivery       000000000000/us-east-1 sns-to-http orders -> 127.0.0.1:9000 attempt 1 refused: egress: refused to send to 127.0.0.1:9000 (not-allowed): no entry of sns.httpDelivery.allow allows it; nothing was sent. To allow it, add "127.0.0.1:9000" or "127.0.0.1:*" to sns.httpDelivery.allow.
```

With `--output json`, each attempt also carries `"http": {"status": 200, "class": "success", "duration": 876453}`: the status, how it was classed, and the wall time the exchange took in nanoseconds.

## Retries and dead letters

A 2xx answer delivers. A 429, a 5xx, a failed connection and no answer within 15 seconds are retried; any other status is a permanent failure. Retries follow the subscription's `DeliveryPolicy`, over its topic's `http.defaultHealthyRetryPolicy`, over AWS's default of three retries 20 seconds apart. They run on the [emulator clock](clock.md) and resend the same bytes. A [fault rule](faults.md#delivery-faults) can answer an attempt without a receiver:

```sh
understudy faults add --id unavailable --phase delivery --mechanism sns-to-http --http-status 503 --next 1
understudy faults add --id slow --phase delivery --mechanism sns-to-http --http-timeout --next 1
aws sns publish --topic-arn arn:aws:sns:us-east-1:000000000000:orders --message 'order 42 shipped'
understudy settle
understudy clock advance 20s && understudy settle
understudy clock advance 20s && understudy settle
understudy journal --kind delivery
```

```text
9      2026-10-26T23:46:28.217920171Z  delivery       000000000000/us-east-1 sns-to-http orders -> 127.0.0.1:8080 attempt 1 retrying, next attempt at 2026-10-26T23:46:48.217920171Z
12     2026-10-26T23:46:48.217920171Z  delivery       000000000000/us-east-1 sns-to-http orders -> 127.0.0.1:8080 attempt 2 retrying, next attempt at 2026-10-26T23:47:08.217920171Z
14     2026-10-26T23:47:08.217920171Z  delivery       000000000000/us-east-1 sns-to-http orders -> 127.0.0.1:8080 attempt 3 delivered
```

- The phases `numNoDelayRetries`, `numMinDelayRetries`, the backoff retries and `numMaxDelayRetries` run in that order. AWS does not document its backoff curves, so every `backoffFunction` steps linearly from `minDelayTarget` to `maxDelayTarget`, without jitter.
- `throttlePolicy.maxReceivesPerSecond` spaces a subscription's attempts at least `1/maxReceivesPerSecond` seconds apart, and a delayed attempt uses up no retry.
- A Notification that fails permanently or runs out of retries goes to the subscription's `RedrivePolicy` queue: the envelope, or for raw delivery the message with its attributes as SQS message attributes. Without a queue it is dropped. A SubscriptionConfirmation or UnsubscribeConfirmation is retried the same way but never goes to a queue.
- Redirects are not followed: a 3xx is a permanent failure.
- `sicklyRetryPolicy` and `guaranteed` are stored and never applied, and the 3,600-second total-retry limit is not checked.

## https endpoints

An `https` endpoint's certificate is always verified. One from a public CA chains to the system roots. For a self-signed certificate, or one from your own CA, pass that certificate or CA as a PEM file:

```sh
understudy serve --sns-http-delivery-allow 127.0.0.1:8443 --sns-http-delivery-ca-bundle receiver.crt
```

The receiver's certificate must name the host the endpoint URL uses, such as `IP:127.0.0.1` for `https://127.0.0.1:8443/`. A delivery to a certificate understudy does not trust fails like an unreachable endpoint and is retried. A bundle that cannot be read, or holds no certificate, stops `serve`:

```text
understudy serve: --sns-http-delivery-ca-bundle: cannot read the CA bundle "missing.crt": open missing.crt: no such file or directory; check the path, or remove the setting
```

## Validating signatures

understudy signs each message with an RSA key of its own per account and region, over the field list AWS signs, with SHA1withRSA for `SignatureVersion` 1 and SHA256withRSA for 2. AWS's validators fetch the certificate at `SigningCertURL`, and they all require `https`. Start the TLS listener, so the links an HTTP delivery carries are on `https://localhost:4567`, and trust its certificate:

```sh
understudy serve --tls --sns-http-delivery-allow 127.0.0.1:8080
eval "$(understudy env --tls)"
aws sns create-topic --name orders --attributes SignatureVersion=2
aws sns subscribe --topic-arn arn:aws:sns:us-east-1:000000000000:orders \
  --protocol http --notification-endpoint http://127.0.0.1:8080/orders
understudy settle
curl -s --cacert "$AWS_CA_BUNDLE" "$(jq -r .SubscribeURL SubscriptionConfirmation.json)"
aws sns publish --topic-arn arn:aws:sns:us-east-1:000000000000:orders --message 'order 42 shipped'
understudy settle
curl -s --cacert "$AWS_CA_BUNDLE" "$(jq -r .UnsubscribeURL Notification.json)"
understudy settle
```

`env --tls` sets `AWS_CA_BUNDLE`, and `NODE_EXTRA_CA_CERTS` for Node, to understudy's certificate, which is new at every start. The receiver now holds all three message types, each with `"SigningCertURL" : "https://localhost:4567/_understudy/sns/SigningCert/..."`.

**JavaScript, `sns-validator`.** Its host pattern is tested against the host with its port:

```js
// validate.mjs FILE...: check each saved SNS message with sns-validator.
import { readFileSync } from "node:fs";
import MessageValidator from "sns-validator";

const validator = new MessageValidator(/^localhost:4567$/);
for (const file of process.argv.slice(2)) {
  const message = JSON.parse(readFileSync(file, "utf8"));
  validator.validate(message, (err) => console.log(file, err ? err.message : "valid"));
}
```

```sh
node validate.mjs SubscriptionConfirmation.json Notification.json UnsubscribeConfirmation.json
```

```text
SubscriptionConfirmation.json valid
Notification.json valid
UnsubscribeConfirmation.json The message signature is invalid.
```

The UnsubscribeConfirmation fails because `sns-validator` 0.3.5 leaves its `Token` out of the string it checks, while AWS signs it with the `Token`. The validator rejects AWS's own UnsubscribeConfirmations the same way. understudy signs AWS's field list and does not change it to suit a validator.

**PHP, `aws-php-sns-message-validator`.** Its pattern is tested against the host without a port: `new MessageValidator(null, '/^localhost$/')`. PHP must trust understudy's certificate, or the first argument must be a certificate client that fetches `SigningCertURL` trusting it. This page did not run the PHP validator; its arguments come from the validator's source.

**Ruby, `Aws::SNS::MessageVerifier`.** It cannot validate understudy's messages: its host check is a fixed pattern of AWS host names. understudy never puts an AWS host in `SigningCertURL` to pass it, since a validator would then fetch AWS's real certificate.

**openssl.** This checks a message against the field list you give it, in AWS's `name\nvalue\n` form:

```sh
verify() {  # verify MESSAGE.json FIELD...: openssl's verdict on the string to sign of FIELD...
  curl -s --cacert "$AWS_CA_BUNDLE" "$(jq -r .SigningCertURL "$1")" | openssl x509 -pubkey -noout > signing.pub
  jq -r .Signature "$1" | base64 -d > signature.bin
  local alg=-sha1; [ "$(jq -r .SignatureVersion "$1")" = 2 ] && alg=-sha256
  local f=$1; shift
  jq -j '. as $m | $ARGS.positional[] | select($m[.] != null) | "\(.)\n\($m[.])\n"' --args "$@" < "$f" |
    openssl dgst "$alg" -verify signing.pub -signature signature.bin
}
verify Notification.json Message MessageId Subject Timestamp TopicArn Type
verify SubscriptionConfirmation.json Message MessageId SubscribeURL Timestamp Token TopicArn Type
verify UnsubscribeConfirmation.json Message MessageId SubscribeURL Timestamp Token TopicArn Type
```

Each prints `Verified OK`. The UnsubscribeConfirmation checked over `sns-validator`'s list, without `Token`, prints `Verification failure`.

## Unsubscribing

A GET of a Notification's `UnsubscribeURL` removes the subscription and sends it an UnsubscribeConfirmation. Its `Token` and `SubscribeURL` do not restore the subscription: whether AWS's do is not recorded, and understudy answers that token as it answers any unknown one:

```sh
aws sns confirm-subscription --topic-arn arn:aws:sns:us-east-1:000000000000:orders \
  --token "$(jq -r .Token UnsubscribeConfirmation.json)"
```

```text
aws: [ERROR]: An error occurred (InvalidParameter) when calling the ConfirmSubscription operation: Invalid parameter: Token
```

A signed `aws sns unsubscribe` sends nothing to the endpoint.

## What is inferred

Where AWS's behavior is not recorded, understudy follows the most likely reading of its documentation, and each choice is a fidelity switch: the status classes, redirects, identical retry bodies, the dead letter's form, the backoff curves, no jitter, dropped messages while pending, a repeated `Subscribe` resending the same token, the UnsubscribeConfirmation only for an unsigned unsubscribe, no dead letter for confirmations, `text/plain` for confirmations whatever `headerContentType` says, and Basic credentials sent before any challenge. `understudy coverage --service sns --switches` lists them with the evidence behind each.
